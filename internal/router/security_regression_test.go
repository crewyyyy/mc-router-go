package router

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

type regressionLogBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *regressionLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *regressionLogBuffer) snapshot() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.b.Bytes()...)
}

func TestRegressionHostControlsCannotForgeJSONLogRecords(t *testing.T) {
	backend, _ := testBackend(t, func(c *net.TCPConn) { _, _ = io.Copy(c, c) })
	var output regressionLogBuffer
	injected := "value\n{\"level\":\"ERROR\",\"msg\":\"forged\"}\x1b[31m"
	logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})).With("escape_probe", injected)
	r, err := New(fastConfig(backend), logger)
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
		case <-time.After(2 * time.Second):
			t.Error("logged router did not stop")
		}
	})
	for _, host := range []string{"safe.example\nFORGED", "safe.example\x1b[31m", "proxy.invalid?_o=safe%0aFORGED.example"} {
		client := dial(t, ln.Addr().String())
		if _, err := client.Write(frame(host)); err != nil {
			t.Fatal(err)
		}
		if _, err := client.Read(make([]byte, 1)); err == nil {
			t.Fatal("unsafe hostname was relayed rather than rejected")
		} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
			t.Fatal("unsafe hostname hung instead of being rejected")
		}
		_ = client.Close()
	}
	// Forge metadata can contain arbitrary non-routing data. It must survive
	// replay, but it must not appear in the structured hostname log field.
	client := dial(t, ln.Addr().String())
	wire := frame("EXAMPLE.COM.\x00FML3\x00secret-metadata\nFORGED\x1b[31m")
	if _, err := client.Write(wire); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(wire))
	if _, err := io.ReadFull(client, got); err != nil || !bytes.Equal(got, wire) {
		t.Fatalf("Forge metadata did not survive replay: %v", err)
	}
	_ = client.Close()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("router did not join its logging workers")
	}
	logs := output.snapshot()
	if bytes.ContainsRune(logs, '\x1b') || !bytes.Contains(logs, []byte(`\n`)) || !bytes.Contains(logs, []byte(`\u001b`)) {
		t.Fatalf("JSON log controls were not escaped: %q", logs)
	}
	if bytes.Contains(logs, []byte("secret-metadata")) {
		t.Fatal("untrusted Forge metadata was emitted into logs")
	}
	connected, rejected := 0, 0
	for _, line := range bytes.Split(bytes.TrimSpace(logs), []byte{'\n'}) {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("not exactly one JSON record per log line: %q: %v", line, err)
		}
		if record["escape_probe"] != injected {
			t.Fatalf("JSON escaping changed field value: %#v", record)
		}
		switch record["msg"] {
		case "connected":
			connected++
			if record["hostname"] != "example.com" {
				t.Fatalf("raw hostname logged instead of safe canonical key: %#v", record)
			}
		case "handshake rejected":
			rejected++
		case "forged":
			t.Fatal("injected field forged a new JSON log record")
		}
	}
	if connected != 1 || rejected != 3 {
		t.Fatalf("unexpected log records: connected=%d rejected=%d", connected, rejected)
	}
}

func TestConfiguredDNSBackendOnlyInStandardMode(t *testing.T) {
	cfg := fastConfig("forge:25568")
	cfg.Routes["horizon.example"] = "horizon:25567"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("configured Docker service names must work in standard mode: %v", err)
	}
	cfg.Transparent = true
	if err := cfg.Validate(); err == nil {
		t.Fatal("transparent DNS target accepted; source-preserving sockets require literal IPv4")
	}
}

func TestRegressionMalformedConfigurationRejected(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Config)
	}{
		{"zero_total", func(c *Config) { c.MaxConnections = 0 }},
		{"excessive_total", func(c *Config) { c.MaxConnections = 1000001 }},
		{"per_ip_exceeds_total", func(c *Config) { c.MaxConnectionsPerIP = c.MaxConnections + 1 }},
		{"zero_ip_table", func(c *Config) { c.MaxIPEntries = 0 }},
		{"infinite_rate", func(c *Config) { c.ConnectionRate = math.Inf(1) }},
		{"nan_ip_rate", func(c *Config) { c.IPConnectionRate = math.NaN() }},
		{"negative_rate", func(c *Config) { c.ConnectionRate = -1 }},
		{"zero_global_burst", func(c *Config) { c.ConnectionBurst = 0 }},
		{"zero_ip_burst", func(c *Config) { c.IPConnectionBurst = 0 }},
		{"negative_write_timeout", func(c *Config) { c.WriteTimeout = -1 }},
		{"zero_idle_timeout", func(c *Config) { c.IdleTimeout = 0 }},
		{"unbounded_half_close", func(c *Config) { c.HalfCloseTimeout = time.Duration(25 * time.Hour) }},
		{"zero_ip_ttl", func(c *Config) { c.IPEntryTTL = 0 }},
		{"transparent_dns_backend", func(c *Config) { c.Transparent = true; c.DefaultBackend = "backend.example:12345" }},
		{"invalid_backend_port", func(c *Config) { c.DefaultBackend = "backend.example:http" }},
		{"zero_backend_port", func(c *Config) { c.DefaultBackend = "127.0.0.1:0" }},
		{"unspecified_backend", func(c *Config) { c.DefaultBackend = "0.0.0.0:12345" }},
		{"multicast_backend", func(c *Config) { c.DefaultBackend = "224.0.0.1:12345" }},
		{"transparent_loopback", func(c *Config) { c.Transparent = true }},
		{"transparent_ipv6", func(c *Config) { c.Transparent = true; c.DefaultBackend = "[2001:db8::1]:12345" }},
		{"route_query", func(c *Config) { c.Routes["example.com?_o=elsewhere.example"] = c.DefaultBackend }},
		{"route_forge", func(c *Config) { c.Routes["example.com\x00FML3\x00"] = c.DefaultBackend }},
		{"route_newline", func(c *Config) { c.Routes["example.com\nFORGED"] = c.DefaultBackend }},
		{"route_esc", func(c *Config) { c.Routes["example.com\x1b[31m"] = c.DefaultBackend }},
		{"duplicate_canonical_route", func(c *Config) {
			c.Routes["BÜCHER.example."] = c.DefaultBackend
			c.Routes["xn--bcher-kva.example"] = c.DefaultBackend
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := fastConfig("127.0.0.1:1")
			tc.mutate(&cfg)
			if _, err := New(cfg, nil); err == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
	for _, input := range []string{
		"[default]\nbackend='127.0.0.1:1'\nunknown=true\n",
		"[default]\nbackend='127.0.0.1:1'\n\n[bad-section]\nkey=1\n",
		"[default]\nbackend='127.0.0.1:1'\n[limits]\nwrite_timeout=5\n",
		"[default]\nbackend='127.0.0.1:1'\n[limits]\nwrite_timeout='0s'\n",
		"[default]\nbackend='127.0.0.1:1'\n[limits]\nmax_pending_handshakes=0\n",
		"[default]\nbackend='127.0.0.1:1'\n[limits]\nmax_pending_handshakes=5\n[limits]\nmax_connections=4\n",
		"[default]\nbackend='127.0.0.1:1'\n[transparent]\nenabled='yes'\n",
		// An input limiter must reject bytes beyond the limit rather than
		// manufacturing EOF and hiding trailing TOML.
		"[default]\nbackend='127.0.0.1:1'\n" + strings.Repeat(" ", 1024*1024) + "bad = [\n",
	} {
		if _, err := LoadConfig(strings.NewReader(input)); err == nil {
			t.Errorf("malformed config TOML accepted (%d bytes): %.80q", len(input), input)
		}
	}
	valid := "[default]\nbackend='127.0.0.1:1'\n[limits]\nmax_connections=8\nmax_pending_handshakes=4\nhandshake_timeout='2s'\n"
	if cfg, err := LoadConfig(strings.NewReader(valid)); err != nil || cfg.MaxConnections != 8 || cfg.MaxPendingHandshakes != 4 || cfg.HandshakeTimeout != 2*time.Second {
		t.Fatalf("valid TOML config: cfg=%+v err=%v", cfg, err)
	}
}
