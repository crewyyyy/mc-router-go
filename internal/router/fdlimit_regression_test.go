//go:build linux

package router

import (
	"bytes"
	"io"
	"net"
	"os"
	"syscall"
	"testing"
	"time"
)

// Embedded os.File preserves the actual high descriptor, unlike net.FileConn,
// which duplicates it again and could silently return to a descriptor < 1024.
// The sockets are nonblocking so os.NewFile registers them with Go's poller.
// No descriptor at a fixed number is ever overwritten.
type regressionSocketFile struct{ *os.File }

func (f *regressionSocketFile) LocalAddr() net.Addr {
	return &net.UnixAddr{Name: "regression-local", Net: "unix"}
}

func (f *regressionSocketFile) RemoteAddr() net.Addr {
	return &net.UnixAddr{Name: "regression-peer", Net: "unix"}
}

func TestRegressionRelaySupportsDescriptorAtLeast1024(t *testing.T) {
	// This is the test subprocess's rlimit only. Increase its soft limit only
	// if necessary and restore exactly what it was, without changing hard
	// limits, production processes, sysctls, or allocating 1024 descriptors.
	var original syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &original); err != nil {
		t.Fatal(err)
	}
	if original.Cur < 1025 {
		if original.Max < 1025 {
			t.Skip("hard descriptor limit cannot accommodate one descriptor >= 1024")
		}
		raised := original
		raised.Cur = 1025
		if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &raised); err != nil {
			t.Skipf("cannot temporarily raise test-process soft descriptor limit: %v", err)
		}
		t.Cleanup(func() {
			if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &original); err != nil {
				t.Errorf("restore test-process descriptor limit: %v", err)
			}
		})
	}
	pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	// F_DUPFD_CLOEXEC finds a free descriptor >= 1024 rather than clobbering a
	// potentially occupied descriptor (as dup2/dup3 to a fixed number would).
	high, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(pair[0]), syscall.F_DUPFD_CLOEXEC, 1024)
	_ = syscall.Close(pair[0])
	if errno != 0 {
		_ = syscall.Close(pair[1])
		t.Fatal(errno)
	}
	frontend := &regressionSocketFile{os.NewFile(high, "regression-high-socket")}
	client := &regressionSocketFile{os.NewFile(uintptr(pair[1]), "regression-peer-socket")}
	t.Cleanup(func() { _ = frontend.Close(); _ = client.Close() })
	if high < 1024 {
		t.Fatalf("test did not acquire a high descriptor: %d", high)
	}
	if err := frontend.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal("high socket did not register with the Go poller:", err)
	}
	if err := client.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	backend, server := net.Pipe()
	defer server.Close()
	_ = server.SetDeadline(time.Now().Add(3 * time.Second))
	cfg := fastConfig("127.0.0.1:1")
	cfg.IdleTimeout = time.Duration(100 * time.Millisecond)
	results := regressionStartRelay(t, frontend, backend, cfg)
	request, reply := []byte("high descriptor request"), []byte("high descriptor reply")
	if _, err := client.Write(request); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(request))
	if _, err := io.ReadFull(server, got); err != nil || !bytes.Equal(got, request) {
		t.Fatalf("high descriptor forward relay failed: %q, %v", got, err)
	}
	if _, err := server.Write(reply); err != nil {
		t.Fatal(err)
	}
	got = make([]byte, len(reply))
	if _, err := io.ReadFull(client, got); err != nil || !bytes.Equal(got, reply) {
		t.Fatalf("high descriptor reverse relay failed: %q, %v", got, err)
	}
	if result := regressionRelayResult(t, results, 2*time.Second); result.reason != "idle_timeout" {
		t.Fatalf("high descriptor session did not idle-close: %+v", result)
	}
}
