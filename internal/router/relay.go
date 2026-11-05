package router

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"time"
)

const copyBufferSize = 32 * 1024

// writeChunk bounds the TOTAL write time for this chunk, including all partial
// writes. It never buffers unbounded output. TCP backpressure stays in the
// sending goroutine without blocking reads/writes in the opposite direction.
func writeChunk(dst net.Conn, data []byte, timeout time.Duration, touch func()) error {
	if err := dst.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	for len(data) != 0 {
		n, err := dst.Write(data)
		if n > 0 {
			data = data[n:]
			if touch != nil {
				touch()
			}
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrNoProgress
		}
	}
	return nil
}

type relayResult struct {
	toBackend, toClient int64
	reason              string
}

type pumpResult struct {
	toBackend bool
	n         int64
	err       error
}

// relay retains upstream's two independent directions, but adds bounded writes,
// a shared bidirectional inactivity timer, bounded half-close drain and explicit
// goroutine joining. A busy direction keeps the shared idle timer alive.
func relay(ctx context.Context, frontend, backend net.Conn, cfg Config) relayResult {
	started := time.Now()
	var lastActivity atomic.Int64 // monotonic duration since started
	touch := func() {
		now := time.Since(started).Nanoseconds()
		for old := lastActivity.Load(); now > old; old = lastActivity.Load() {
			if lastActivity.CompareAndSwap(old, now) {
				break
			}
		}
	}
	done := make(chan pumpResult, 2)
	pump := func(dst, src net.Conn, toBackend bool) {
		result := pumpResult{toBackend: toBackend}
		defer func() { done <- result }()
		buffer := make([]byte, copyBufferSize)
		for {
			n, err := src.Read(buffer)
			if n > 0 {
				touch()
				if writeErr := writeChunk(dst, buffer[:n], time.Duration(cfg.WriteTimeout), touch); writeErr != nil {
					result.err = writeErr
					return
				}
				result.n += int64(n)
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					if half, ok := dst.(interface{ CloseWrite() error }); ok {
						result.err = half.CloseWrite()
					}
				} else {
					result.err = err
				}
				return
			}
			if n == 0 {
				result.err = io.ErrNoProgress
				return
			}
		}
	}
	go pump(backend, frontend, true)
	go pump(frontend, backend, false)

	idle := time.NewTimer(time.Duration(cfg.IdleTimeout))
	defer idle.Stop()
	var halfTimer *time.Timer
	var halfDone <-chan time.Time
	defer func() {
		if halfTimer != nil {
			halfTimer.Stop()
		}
	}()
	var result relayResult
	completed := 0
	account := func(p pumpResult) {
		completed++
		if p.toBackend {
			result.toBackend = p.n
		} else {
			result.toClient = p.n
		}
	}
	finish := func(reason string) relayResult {
		result.reason = reason
		_ = frontend.Close()
		_ = backend.Close()
		for completed < 2 {
			account(<-done)
		}
		return result
	}
	for {
		select {
		case p := <-done:
			account(p)
			if p.err != nil {
				return finish("io_error")
			}
			if completed == 2 {
				return finish("eof")
			}
			// EOF can be a deliberate half-close. Permit reverse data briefly,
			// but never leave a worker/socket pair retained without a deadline.
			halfTimer = time.NewTimer(time.Duration(cfg.HalfCloseTimeout))
			halfDone = halfTimer.C
		case <-halfDone:
			return finish("half_close_timeout")
		case <-idle.C:
			remaining := time.Duration(cfg.IdleTimeout) - (time.Since(started) - time.Duration(lastActivity.Load()))
			if remaining <= 0 {
				return finish("idle_timeout")
			}
			idle.Reset(remaining)
		case <-ctx.Done():
			return finish("shutdown")
		}
	}
}
