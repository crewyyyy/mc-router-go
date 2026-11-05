// Package router implements a hardened, deliberately small derivative of
// itzg/mc-router's handshake -> static route -> bidirectional TCP architecture.
package router

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/crewyyyy/mc-router-go/internal/mcproto"
)

type counters struct {
	accepted, rejected, handshakesFailed, dialFailed, connected atomic.Uint64
	bytesClientToBackend, bytesBackendToClient                  atomic.Uint64
}

// Stats exposes aggregate counters, not attacker-controlled hostname/IP labels.
type Stats struct {
	Active               int    `json:"active"`
	PendingHandshakes    int    `json:"pending_handshakes"`
	TrackedIPs           int    `json:"tracked_ips"`
	Accepted             uint64 `json:"accepted"`
	Rejected             uint64 `json:"rejected"`
	HandshakeErrors      uint64 `json:"handshake_errors"`
	DialErrors           uint64 `json:"dial_errors"`
	Connected            uint64 `json:"connected"`
	BytesClientToBackend uint64 `json:"bytes_client_to_backend"`
	BytesBackendToClient uint64 `json:"bytes_backend_to_client"`
}

type Router struct {
	cfg       Config
	routes    map[string]string // immutable after construction
	admission *admission
	logger    *slog.Logger
	metrics   counters
	serving   atomic.Bool
}

func New(cfg Config, logger *slog.Logger) (*Router, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	routes := make(map[string]string, len(cfg.Routes))
	for host, backend := range cfg.Routes {
		key, _ := mcproto.CanonicalHost(host) // validated above
		routes[key] = backend
	}
	return &Router{cfg: cfg, routes: routes, admission: newAdmission(cfg), logger: logger}, nil
}

func (r *Router) Stats() Stats {
	active, tracked := r.admission.counts()
	return Stats{Active: active, PendingHandshakes: r.admission.pendingCount(), TrackedIPs: tracked,
		Accepted: r.metrics.accepted.Load(), Rejected: r.metrics.rejected.Load(),
		HandshakeErrors: r.metrics.handshakesFailed.Load(), DialErrors: r.metrics.dialFailed.Load(),
		Connected: r.metrics.connected.Load(), BytesClientToBackend: r.metrics.bytesClientToBackend.Load(),
		BytesBackendToClient: r.metrics.bytesBackendToClient.Load()}
}

// Serve owns ln. On cancellation it immediately stops admission, drains active
// sessions for shutdown_timeout, then cancels and joins all remaining handlers.
// Go's network poller uses epoll on Linux; there is no FD_SETSIZE=1024 ceiling.
func (r *Router) Serve(ctx context.Context, ln net.Listener) error {
	if !r.serving.CompareAndSwap(false, true) {
		return errors.New("router is already serving")
	}
	defer r.serving.Store(false)
	defer ln.Close()
	stopAccept := context.AfterFunc(ctx, func() { _ = ln.Close() })
	defer stopAccept()
	sessionsCtx, cancelSessions := context.WithCancel(context.Background())
	defer cancelSessions()
	var workers sync.WaitGroup
	var serveErr error
	var backoff time.Duration

	r.logger.Info("listening", "listen", ln.Addr().String(), "transparent", r.cfg.Transparent,
		"max_connections", r.cfg.MaxConnections, "max_connections_per_ip", r.cfg.MaxConnectionsPerIP)
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				break
			}
			var temporary interface{ Temporary() bool }
			if errors.As(err, &temporary) && temporary.Temporary() {
				if backoff == 0 {
					backoff = 5 * time.Millisecond
				} else {
					backoff *= 2
				}
				if backoff > time.Second {
					backoff = time.Second
				}
				timer := time.NewTimer(backoff)
				select {
				case <-timer.C:
				case <-ctx.Done():
				}
				timer.Stop()
				continue
			}
			serveErr = fmt.Errorf("accept: %w", err)
			break
		}
		backoff = 0
		acceptedAt := time.Now()
		r.metrics.accepted.Add(1)
		peer, ok := conn.RemoteAddr().(*net.TCPAddr)
		if !ok || ctx.Err() != nil {
			r.metrics.rejected.Add(1)
			_ = conn.Close()
			continue
		}
		ip := peer.AddrPort().Addr().Unmap()
		if !r.admission.admitHandshake(ip, acceptedAt) {
			// No per-rejection logging or goroutine: counters prevent log/memory
			// amplification from malformed handshakes and connection floods.
			r.metrics.rejected.Add(1)
			_ = conn.Close()
			continue
		}
		if err := conn.SetReadDeadline(acceptedAt.Add(time.Duration(r.cfg.HandshakeTimeout))); err != nil {
			r.admission.release(ip, time.Now(), true)
			_ = conn.Close()
			continue
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			pending := true
			defer func() { r.admission.release(ip, time.Now(), pending) }()
			r.handle(sessionsCtx, conn, ip, func() {
				r.admission.finishHandshake()
				pending = false
			})
		}()
	}

	_ = ln.Close()
	done := make(chan struct{})
	go func() { workers.Wait(); close(done) }()
	timer := time.NewTimer(time.Duration(r.cfg.ShutdownTimeout))
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		cancelSessions() // each handler closes its own sockets on cancellation
		<-done
	}
	r.logger.Info("stopped", "stats", r.Stats())
	return serveErr
}

func (r *Router) handle(ctx context.Context, frontend net.Conn, ip netip.Addr, handshakeComplete func()) {
	defer frontend.Close()
	stopFrontend := context.AfterFunc(ctx, func() { _ = frontend.Close() })
	defer stopFrontend()
	if tcp, ok := frontend.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
		_ = tcp.SetKeepAlive(true)
		_ = tcp.SetKeepAlivePeriod(30 * time.Second)
	}
	h, err := mcproto.ReadHandshakeLimit(frontend, r.cfg.HandshakeMaxBytes)
	if err != nil {
		r.metrics.handshakesFailed.Add(1)
		r.logger.Debug("handshake rejected", "client", frontend.RemoteAddr().String(), "error", err.Error())
		return
	}
	handshakeComplete()
	backendAddress := r.routes[h.Hostname]
	if backendAddress == "" {
		backendAddress = r.cfg.DefaultBackend
	}
	if backendAddress == "" {
		r.metrics.rejected.Add(1)
		return
	}
	backend, err := dialBackend(ctx, ip, backendAddress, r.cfg)
	if err != nil {
		r.metrics.dialFailed.Add(1)
		r.logger.Debug("backend connection failed", "client", frontend.RemoteAddr().String(),
			"backend", backendAddress, "error", err.Error())
		return
	}
	defer backend.Close()
	stopBackend := context.AfterFunc(ctx, func() { _ = backend.Close() })
	defer stopBackend()
	_ = backend.SetNoDelay(true)
	_ = backend.SetKeepAlive(true)
	_ = backend.SetKeepAlivePeriod(30 * time.Second)
	if err := writeChunk(backend, h.Wire, time.Duration(r.cfg.WriteTimeout), nil); err != nil {
		r.metrics.dialFailed.Add(1)
		return
	}
	if err := frontend.SetReadDeadline(time.Time{}); err != nil {
		return
	}
	r.metrics.connected.Add(1)
	r.metrics.bytesClientToBackend.Add(uint64(len(h.Wire)))
	r.logger.Info("connected", "client", frontend.RemoteAddr().String(), "hostname", h.Hostname,
		"backend", backendAddress, "backend_source", backend.LocalAddr().String(),
		"protocol", h.ProtocolVersion, "intent", h.NextState, "transparent", r.cfg.Transparent)
	result := relay(ctx, frontend, backend, r.cfg)
	r.metrics.bytesClientToBackend.Add(uint64(result.toBackend))
	r.metrics.bytesBackendToClient.Add(uint64(result.toClient))
	r.logger.Debug("disconnected", "client", frontend.RemoteAddr().String(), "hostname", h.Hostname,
		"reason", result.reason, "to_backend", result.toBackend, "to_client", result.toClient)
}

func dialBackend(ctx context.Context, source netip.Addr, target string, cfg Config) (*net.TCPConn, error) {
	d := net.Dialer{Timeout: time.Duration(cfg.DialTimeout), KeepAlive: 30 * time.Second}
	network := "tcp"
	if cfg.Transparent {
		addr, _ := netip.ParseAddrPort(target) // immutable validated configuration
		if !source.Is4() || !addr.Addr().Is4() {
			return nil, errors.New("transparent deployment supports only IPv4; refusing IP-losing fallback")
		}
		network = "tcp4"
		// Preserve the observed source IP, not necessarily its original port.
		// An ephemeral source port avoids collisions across concurrent sessions.
		d.LocalAddr = &net.TCPAddr{IP: net.IP(source.AsSlice())}
		d.Control = transparentControl
	}
	conn, err := d.DialContext(ctx, network, target)
	if err != nil {
		return nil, err
	}
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		_ = conn.Close()
		return nil, errors.New("backend is not TCP")
	}
	return tcp, nil
}
