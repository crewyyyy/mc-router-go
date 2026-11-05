//go:build linux

package router

import (
	"errors"
	"fmt"
	"syscall"
)

// The namespace/routing deployment is IPv4-only. Never silently downgrade a
// transparent session to ordinary NAT/plain proxying if its socket setup fails.
func transparentControl(network, _ string, raw syscall.RawConn) error {
	if network != "tcp4" {
		return errors.New("transparent mode requires tcp4")
	}
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		socketErr = syscall.SetsockoptInt(int(fd), syscall.SOL_IP, syscall.IP_TRANSPARENT, 1)
	}); err != nil {
		return err
	}
	if socketErr != nil {
		return fmt.Errorf("IP_TRANSPARENT: %w", socketErr)
	}
	return nil
}

// CheckTransparentSupport verifies capability availability before opening the
// public listener. It does not change routing or any system-wide sysctl.
func CheckTransparentSupport() error {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	if err := syscall.SetsockoptInt(fd, syscall.SOL_IP, syscall.IP_TRANSPARENT, 1); err != nil {
		return fmt.Errorf("IP_TRANSPARENT requires service-scoped CAP_NET_RAW or CAP_NET_ADMIN: %w", err)
	}
	return nil
}
