package router

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func regressionStartRelay(t *testing.T, frontend, backend net.Conn, cfg Config) <-chan relayResult {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan relayResult, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		results <- relay(ctx, frontend, backend, cfg)
	}()
	t.Cleanup(func() {
		cancel()
		_ = frontend.Close()
		_ = backend.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("relay goroutines did not join")
		}
	})
	return results
}

func regressionRelayResult(t *testing.T, results <-chan relayResult, timeout time.Duration) relayResult {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(timeout):
		t.Fatal("relay did not finish within its deadline")
		return relayResult{}
	}
}

func regressionTCPPair(t *testing.T) (*net.TCPConn, *net.TCPConn) {
	t.Helper()
	ln, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if err := ln.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	peer := dial(t, ln.Addr().String())
	local, err := ln.AcceptTCP()
	if err != nil {
		t.Fatal(err)
	}
	if err := local.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		local.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = local.Close() })
	return local, peer
}

func TestRegressionHalfCloseAllowsReverseDrain(t *testing.T) {
	frontend, client := regressionTCPPair(t)
	backend, server := regressionTCPPair(t)
	cfg := fastConfig("127.0.0.1:1")
	cfg.HalfCloseTimeout = time.Duration(time.Second)
	results := regressionStartRelay(t, frontend, backend, cfg)
	request := []byte("request before EOF")
	if _, err := client.Write(request); err != nil {
		t.Fatal(err)
	}
	if err := client.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(server)
	if err != nil || !bytes.Equal(got, request) {
		t.Fatalf("backend did not receive request and propagated EOF: %q, %v", got, err)
	}
	// The reverse response is sent only AFTER the backend observed EOF.
	reply := []byte("response after client half-close")
	if _, err := server.Write(reply); err != nil {
		t.Fatal("reverse write was lost after the forward EOF:", err)
	}
	if err := server.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	got, err = io.ReadAll(client)
	if err != nil || !bytes.Equal(got, reply) {
		t.Fatalf("reverse drain changed or truncated response: %q, %v", got, err)
	}
	result := regressionRelayResult(t, results, 2*time.Second)
	if result.reason != "eof" || result.toBackend != int64(len(request)) || result.toClient != int64(len(reply)) {
		t.Fatalf("unexpected drain result: %+v", result)
	}
}

func TestRegressionHalfCloseDrainHasAbsoluteTimeout(t *testing.T) {
	frontend, client := regressionTCPPair(t)
	backend, server := regressionTCPPair(t)
	cfg := fastConfig("127.0.0.1:1")
	cfg.HalfCloseTimeout = time.Duration(120 * time.Millisecond)
	cfg.IdleTimeout = time.Duration(time.Second)
	results := regressionStartRelay(t, frontend, backend, cfg)
	started := time.Now()
	if err := client.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if _, err := server.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("backend did not receive propagated half-close: %v", err)
	}
	// Keep the reverse direction open indefinitely. No sleeps or large buffers
	// are needed to show that the half-close retention is bounded.
	result := regressionRelayResult(t, results, 2*time.Second)
	if result.reason != "half_close_timeout" {
		t.Fatalf("wrong termination reason: %+v", result)
	}
	if elapsed := time.Since(started); elapsed < 90*time.Millisecond || elapsed > time.Second {
		t.Fatalf("half-close timeout outside expected bounds: %v", elapsed)
	}
	if _, err := client.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("half-close timeout left client socket open: %v", err)
	}
}

func TestRegressionIdleTimeoutTracksEitherDirection(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		name := "client_to_backend_only"
		if reverse {
			name = "backend_to_client_only"
		}
		t.Run(name, func(t *testing.T) {
			frontend, client := net.Pipe()
			backend, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			_ = client.SetDeadline(time.Now().Add(3 * time.Second))
			_ = server.SetDeadline(time.Now().Add(3 * time.Second))
			cfg := fastConfig("127.0.0.1:1")
			cfg.IdleTimeout = time.Duration(200 * time.Millisecond)
			cfg.WriteTimeout = time.Duration(time.Second)
			results := regressionStartRelay(t, frontend, backend, cfg)
			sender, receiver := client, server
			if reverse {
				sender, receiver = server, client
			}
			// Run for more than two idle periods. Only one direction is active;
			// no artificial echo masks a timer wrongly scoped to one pump.
			for i := 0; i < 9; i++ {
				written := make(chan error, 1)
				go func() { _, err := sender.Write([]byte{byte(i)}); written <- err }()
				var b [1]byte
				if _, err := io.ReadFull(receiver, b[:]); err != nil {
					t.Fatal("one-way activity failed to keep the session alive:", err)
				}
				if err := <-written; err != nil || b[0] != byte(i) {
					t.Fatalf("one-way transfer corrupted: %x, %v", b, err)
				}
				if i < 8 {
					time.Sleep(60 * time.Millisecond)
				}
			}
			select {
			case result := <-results:
				t.Fatalf("active one-direction session closed: %+v", result)
			default:
			}
			// Now BOTH sides are silent. The same shared timer must close them.
			started := time.Now()
			result := regressionRelayResult(t, results, 2*time.Second)
			if result.reason != "idle_timeout" || time.Since(started) > time.Second {
				t.Fatalf("silent session did not idle-close promptly: %+v", result)
			}
			if reverse && result.toClient != 9 || !reverse && result.toBackend != 9 {
				t.Fatalf("incorrect one-direction accounting: %+v", result)
			}
		})
	}
}

func TestRegressionBlockedWriteDoesNotBlockReverseDirection(t *testing.T) {
	frontend, client := net.Pipe()
	backend, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	_ = server.SetDeadline(time.Now().Add(3 * time.Second))
	cfg := fastConfig("127.0.0.1:1")
	cfg.WriteTimeout = time.Duration(400 * time.Millisecond)
	cfg.IdleTimeout = time.Duration(2 * time.Second)
	results := regressionStartRelay(t, frontend, backend, cfg)
	// net.Pipe has zero buffering: this one byte blocks the backend write
	// deterministically, without filling TCP buffers or generating a load.
	if _, err := client.Write([]byte{0x41}); err != nil {
		t.Fatal(err)
	}
	reverseWritten := make(chan error, 1)
	go func() {
		_, err := server.Write([]byte("reverse progress"))
		reverseWritten <- err
	}()
	got := make([]byte, len("reverse progress"))
	if _, err := io.ReadFull(client, got); err != nil {
		t.Fatal("blocked forward write stalled reverse direction:", err)
	}
	if string(got) != "reverse progress" {
		t.Fatalf("reverse data changed: %q", got)
	}
	if err := <-reverseWritten; err != nil {
		t.Fatal(err)
	}
	result := regressionRelayResult(t, results, 2*time.Second)
	if result.reason != "io_error" || result.toClient != int64(len(got)) {
		t.Fatalf("blocked write deadline failed to terminate session: %+v", result)
	}
	// This must be EOF, not a deadline on the test's own read.
	if _, err := client.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("write deadline did not close frontend: %v", err)
	}
	if _, err := server.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("write deadline did not close backend: %v", err)
	}
}

type regressionPartialConn struct {
	net.Conn
	deadlines int
	writes    int
}

func (c *regressionPartialConn) SetWriteDeadline(deadline time.Time) error {
	c.deadlines++
	return c.Conn.SetWriteDeadline(deadline)
}

func (c *regressionPartialConn) Write(b []byte) (int, error) {
	c.writes++
	return c.Conn.Write(b[:1])
}

func TestRegressionWriteChunkUsesOneTotalDeadline(t *testing.T) {
	local, peer := net.Pipe()
	defer local.Close()
	defer peer.Close()
	conn := &regressionPartialConn{Conn: local}
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			time.Sleep(25 * time.Millisecond)
			if _, err := peer.Read(make([]byte, 1)); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = peer.Close()
		select {
		case <-readDone:
		case <-time.After(time.Second):
			t.Error("partial-write reader did not stop")
		}
	})
	started := time.Now()
	err := writeChunk(conn, make([]byte, 32), 120*time.Millisecond, nil)
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("partial writes escaped the total deadline: %v", err)
	}
	if conn.deadlines != 1 || conn.writes < 2 {
		t.Fatalf("expected multiple partial writes under one deadline: deadlines=%d writes=%d", conn.deadlines, conn.writes)
	}
	if elapsed := time.Since(started); elapsed > 600*time.Millisecond {
		t.Fatalf("write deadline restarted for each partial write: %v", elapsed)
	}
}
