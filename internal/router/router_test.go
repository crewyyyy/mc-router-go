package router

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func frame(host string) []byte {
	varint := func(n int) []byte {
		var out []byte
		for {
			b := byte(n & 127)
			n >>= 7
			if n > 0 {
				b |= 128
			}
			out = append(out, b)
			if n == 0 {
				return out
			}
		}
	}
	payload := []byte{0, 0xfb, 5} // handshake, protocol 763
	payload = append(payload, varint(len([]byte(host)))...)
	payload = append(payload, []byte(host)...)
	payload = append(payload, 0x63, 0xdd, 2)
	return append(varint(len(payload)), payload...)
}

func testBackend(t *testing.T, handle func(*net.TCPConn)) (string, <-chan string) {
	t.Helper()
	ln, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	peers := make(chan string, 4096)
	var workers sync.WaitGroup
	var connections sync.Map
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			conn, err := ln.AcceptTCP()
			if err != nil {
				return
			}
			_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
			peers <- conn.RemoteAddr().String()
			connections.Store(conn, true)
			workers.Add(1)
			go func() { defer workers.Done(); defer conn.Close(); defer connections.Delete(conn); handle(conn) }()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-acceptDone
		connections.Range(func(key, _ any) bool { _ = key.(*net.TCPConn).Close(); return true })
		workers.Wait()
	})
	return ln.Addr().String(), peers
}

func testRouter(t *testing.T, cfg Config) (*Router, string, context.CancelFunc, <-chan error) {
	t.Helper()
	r, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Serve(ctx, ln); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("router did not stop")
		}
	})
	return r, ln.Addr().String(), cancel, done
}

func fastConfig(target string) Config {
	cfg := DefaultConfig()
	cfg.DefaultBackend = target
	cfg.ShutdownTimeout = time.Duration(200 * time.Millisecond)
	cfg.HalfCloseTimeout = time.Duration(150 * time.Millisecond)
	cfg.HandshakeTimeout = time.Duration(250 * time.Millisecond)
	return cfg
}

func dial(t *testing.T, addr string) *net.TCPConn {
	t.Helper()
	conn, err := net.DialTimeout("tcp4", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	tcp := conn.(*net.TCPConn)
	_ = tcp.SetDeadline(time.Now().Add(3 * time.Second))
	t.Cleanup(func() { _ = tcp.Close() })
	return tcp
}

func TestRoutesAndPipelinedBytesPreserved(t *testing.T) {
	first, firstPeers := testBackend(t, func(c *net.TCPConn) { _, _ = io.Copy(c, c) })
	second, secondPeers := testBackend(t, func(c *net.TCPConn) { _, _ = io.Copy(c, c) })
	cfg := fastConfig(first)
	cfg.Routes["horizon.example"] = second
	_, addr, _, _ := testRouter(t, cfg)
	for _, host := range []string{"HORIZON.EXAMPLE.\x00FML3\x00", "unknown.example"} {
		c := dial(t, addr)
		// A login/status packet may already be queued with the handshake.
		wire := append(frame(host), []byte{2, 0, 0xab, 0xcd, 0, 0xff}...)
		if _, err := c.Write(wire); err != nil {
			t.Fatal(err)
		}
		reply := make([]byte, len(wire))
		if _, err := io.ReadFull(c, reply); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(reply, wire) {
			t.Fatal("handshake/pipelined bytes changed")
		}
		_ = c.Close()
	}
	select {
	case <-firstPeers:
	case <-time.After(time.Second):
		t.Fatal("default route not used")
	}
	select {
	case <-secondPeers:
	case <-time.After(time.Second):
		t.Fatal("named route not used")
	}
}

func TestHandshakeUsesAbsoluteDeadline(t *testing.T) {
	backend, _ := testBackend(t, func(c *net.TCPConn) { _, _ = io.Copy(c, c) })
	cfg := fastConfig(backend)
	cfg.HandshakeTimeout = time.Duration(130 * time.Millisecond)
	r, addr, _, _ := testRouter(t, cfg)
	c := dial(t, addr)
	started := time.Now()
	wire := frame("slow.example")
	// Each fragment arrives within 130ms, but the whole frame exceeds it.
	for i := 0; i < 4; i++ {
		_, _ = c.Write(wire[i : i+1])
		time.Sleep(45 * time.Millisecond)
	}
	_, err := c.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("incomplete handshake survived deadline")
	}
	if time.Since(started) > 500*time.Millisecond {
		t.Fatal("deadline restarted on each byte")
	}
	deadline := time.Now().Add(time.Second)
	for r.Stats().Active != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if r.Stats().Active != 0 || r.Stats().HandshakeErrors == 0 {
		t.Fatal(r.Stats())
	}
}

func TestAdmissionBeforeGoroutineAndRecovery(t *testing.T) {
	backend, _ := testBackend(t, func(c *net.TCPConn) { _, _ = io.Copy(c, c) })
	cfg := fastConfig(backend)
	cfg.MaxConnections = 2
	cfg.MaxPendingHandshakes = 2
	cfg.MaxConnectionsPerIP = 2
	cfg.HandshakeTimeout = time.Duration(2 * time.Second)
	r, addr, _, _ := testRouter(t, cfg)
	a := dial(t, addr)
	b := dial(t, addr)
	deadline := time.Now().Add(time.Second)
	for r.Stats().Active < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	c := dial(t, addr)
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("over-capacity connection not closed")
	}
	if r.Stats().Active != 2 || r.Stats().Rejected < 1 {
		t.Fatal(r.Stats())
	}
	_ = a.Close()
	_ = b.Close()
	deadline = time.Now().Add(time.Second)
	for r.Stats().Active != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	d := dial(t, addr)
	wire := frame("ok.example")
	_, _ = d.Write(wire)
	reply := make([]byte, len(wire))
	if _, err := io.ReadFull(d, reply); err != nil {
		t.Fatal("capacity did not recover:", err)
	}
}

func TestGracefulShutdownIsBounded(t *testing.T) {
	backend, _ := testBackend(t, func(c *net.TCPConn) { _, _ = io.Copy(c, c) })
	cfg := fastConfig(backend)
	r, addr, cancel, _ := testRouter(t, cfg)
	c := dial(t, addr)
	wire := frame("ok.example")
	_, _ = c.Write(wire)
	reply := make([]byte, len(wire))
	if _, err := io.ReadFull(c, reply); err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("shutdown left connection open")
	}
	deadline := time.Now().Add(time.Second)
	for r.Stats().Active != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if r.Stats().Active != 0 {
		t.Fatal("handler leaked on shutdown")
	}
}

func TestConcurrentClients(t *testing.T) {
	backend, _ := testBackend(t, func(c *net.TCPConn) { _, _ = io.Copy(c, c) })
	cfg := fastConfig(backend)
	cfg.HandshakeTimeout = time.Duration(3 * time.Second)
	cfg.MaxPendingHandshakes = 128
	cfg.MaxConnectionsPerIP = 128
	cfg.IPConnectionRate = 10000
	cfg.IPConnectionBurst = 128
	cfg.ConnectionRate = 10000
	cfg.ConnectionBurst = 128
	_, addr, _, _ := testRouter(t, cfg)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := net.DialTimeout("tcp4", addr, time.Second)
			if err != nil {
				t.Error(err)
				return
			}
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(3 * time.Second))
			wire := append(frame("x.example"), bytes.Repeat([]byte{0x4e}, 8192)...)
			if _, err := c.Write(wire); err != nil {
				t.Error(err)
				return
			}
			reply := make([]byte, len(wire))
			if _, err := io.ReadFull(c, reply); err != nil {
				t.Error(err)
				return
			}
			if !bytes.Equal(wire, reply) {
				t.Error("corrupt relay")
			}
		}()
	}
	wg.Wait()
}
