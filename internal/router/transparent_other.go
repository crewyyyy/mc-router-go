//go:build !linux

package router

import (
	"errors"
	"syscall"
)

func transparentControl(_, _ string, _ syscall.RawConn) error {
	return errors.New("transparent mode requires Linux")
}

func CheckTransparentSupport() error { return errors.New("transparent mode requires Linux") }
