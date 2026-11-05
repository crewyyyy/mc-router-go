package router

import (
	"io"
	"net"
	"net/netip"
	"testing"
	"time"
)

func regressionAdmissionConfig() Config {
	cfg := fastConfig("127.0.0.1:1")
	cfg.MaxConnections = 4
	cfg.MaxPendingHandshakes = 4
	cfg.MaxConnectionsPerIP = 2
	cfg.MaxIPEntries = 3
	cfg.ConnectionRate, cfg.IPConnectionRate = 100, 100
	cfg.ConnectionBurst, cfg.IPConnectionBurst = 100, 100
	cfg.IPEntryTTL = time.Second
	return cfg
}

func TestRegressionAdmissionLimitsAndRecovery(t *testing.T) {
	cfg := regressionAdmissionConfig()
	a := newAdmission(cfg)
	now := time.Unix(1000, 0)
	ips := []netip.Addr{netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("192.0.2.2"), netip.MustParseAddr("192.0.2.3")}
	for _, ip := range ips[:2] {
		if !a.admit(ip, now) || !a.admit(ip, now) {
			t.Fatal("valid admission rejected")
		}
	}
	if a.admit(ips[0], now) || a.admit(ips[2], now) {
		t.Fatal("total connection limit exceeded")
	}
	a.release(ips[1], now)
	if a.admit(ips[0], now) {
		t.Fatal("per-IP connection limit exceeded while total capacity was available")
	}
	if !a.admit(ips[2], now) {
		t.Fatal("one IP's limit blocked an independent IP")
	}
	if active, tracked := a.counts(); active != 4 || tracked > cfg.MaxIPEntries {
		t.Fatalf("unexpected bounded state: active=%d tracked=%d", active, tracked)
	}
	a.release(ips[0], now)
	if !a.admit(ips[0], now) {
		t.Fatal("released per-IP capacity did not recover")
	}
}

func TestRegressionPendingHandshakeLimitReleasesOnParse(t *testing.T) {
	cfg := regressionAdmissionConfig()
	cfg.MaxConnections = 4
	cfg.MaxPendingHandshakes = 2
	cfg.MaxConnectionsPerIP = 4
	cfg.MaxPendingHandshakes = 2
	a := newAdmission(cfg)
	now := time.Unix(1000, 0)
	ips := []netip.Addr{netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("192.0.2.2"), netip.MustParseAddr("192.0.2.3")}
	for _, ip := range ips[:2] {
		if !a.admitHandshake(ip, now) {
			t.Fatal("pending handshake rejected below limit")
		}
	}
	if a.admitHandshake(ips[2], now) {
		t.Fatal("pending handshake limit exceeded")
	}
	if a.pendingCount() != 2 || a.active != 2 {
		t.Fatalf("wrong reservations: pending=%d active=%d", a.pendingCount(), a.active)
	}
	a.finishHandshake()
	if !a.admitHandshake(ips[2], now) {
		t.Fatal("handshake reservation was not released after parsing")
	}
	a.release(ips[0], now, false)
	a.release(ips[1], now, true)
	a.release(ips[2], now, true)
	if a.pendingCount() != 0 || a.active != 0 {
		t.Fatalf("admission leaked: %+v", a)
	}
}

func TestRegressionAdmissionRateLimits(t *testing.T) {
	for _, global := range []bool{false, true} {
		name := "per_ip"
		if global {
			name = "global"
		}
		t.Run(name, func(t *testing.T) {
			cfg := regressionAdmissionConfig()
			if global {
				cfg.ConnectionRate, cfg.ConnectionBurst = 2, 2
			} else {
				cfg.IPConnectionRate, cfg.IPConnectionBurst = 2, 2
			}
			a := newAdmission(cfg)
			now := time.Unix(1000, 0)
			ip := netip.MustParseAddr("192.0.2.1")
			for i := 0; i < 2; i++ {
				if !a.admit(ip, now) {
					t.Fatal("burst token unavailable")
				}
				a.release(ip, now)
			}
			if global {
				ip = netip.MustParseAddr("192.0.2.2")
			}
			if a.admit(ip, now) || a.admit(ip, now.Add(499*time.Millisecond)) {
				t.Fatal("rate limit allowed an admission without a complete token")
			}
			if !a.admit(ip, now.Add(500*time.Millisecond)) {
				t.Fatal("rate token did not recover at the configured rate")
			}
			a.release(ip, now.Add(500*time.Millisecond))
		})
	}
}

func TestRegressionAdmissionTableBoundAndTTL(t *testing.T) {
	cfg := regressionAdmissionConfig()
	cfg.MaxIPEntries = 2
	a := newAdmission(cfg)
	now := time.Unix(1000, 0)
	activeIP := netip.MustParseAddr("192.0.2.1")
	idleIP := netip.MustParseAddr("192.0.2.2")
	newIP := netip.MustParseAddr("192.0.2.3")
	if !a.admit(activeIP, now) || !a.admit(idleIP, now) {
		t.Fatal("initial admission failed")
	}
	a.release(idleIP, now.Add(500*time.Millisecond))
	if a.admit(newIP, now.Add(1499*time.Millisecond)) {
		t.Fatal("table entry evicted before its post-release TTL")
	}
	if !a.admit(newIP, now.Add(1500*time.Millisecond)) {
		t.Fatal("expired inactive entry was not reclaimed at table capacity")
	}
	if a.ips[activeIP] == nil || a.ips[activeIP].active != 1 {
		t.Fatal("TTL sweep evicted an active IP")
	}
	if a.ips[idleIP] != nil {
		t.Fatal("expired entry retained")
	}
	if active, tracked := a.counts(); active != 2 || tracked != 2 {
		t.Fatalf("unexpected state: active=%d tracked=%d", active, tracked)
	}
	// A small fixed number of synthetic addresses proves the cap without a flood.
	for i := byte(4); i < 12; i++ {
		if a.admit(netip.AddrFrom4([4]byte{192, 0, 2, i}), now.Add(2*time.Second)) {
			t.Fatal("full table admitted another distinct IP")
		}
		if _, tracked := a.counts(); tracked != 2 {
			t.Fatalf("rejections grew the per-IP table to %d", tracked)
		}
	}
	a.release(activeIP, now.Add(2*time.Second))
	a.release(newIP, now.Add(2*time.Second))
}

func TestRegressionIPEntryTTLStartsWhenSessionEnds(t *testing.T) {
	backend, _ := testBackend(t, func(c *net.TCPConn) { _, _ = io.Copy(c, c) })
	cfg := regressionAdmissionConfig()
	cfg.DefaultBackend = backend
	cfg.MaxIPEntries = 1
	cfg.IPEntryTTL = time.Duration(80 * time.Millisecond)
	r, addr, _, _ := testRouter(t, cfg)
	client := dial(t, addr)
	wire := frame("ttl.example")
	if _, err := client.Write(wire); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(client, make([]byte, len(wire))); err != nil {
		t.Fatal(err)
	}
	// Keep the admitted session alive beyond TTL, then release it. Its IP must
	// remain tracked for a fresh TTL, rather than aging from handler entry time.
	time.Sleep(2 * cfg.IPEntryTTL)
	_ = client.Close()
	deadline := time.Now().Add(time.Second)
	for r.Stats().Active != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if r.Stats().Active != 0 {
		t.Fatal("session did not release its admission")
	}
	other := netip.MustParseAddr("192.0.2.99")
	if r.admission.admit(other, time.Now()) {
		r.admission.release(other, time.Now())
		t.Fatal("session release backdated lastSeen, allowing immediate TTL eviction")
	}
}
