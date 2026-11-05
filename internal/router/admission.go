package router

import (
	"math"
	"net/netip"
	"sync"
	"time"
)

type tokenBucket struct {
	tokens float64
	last   time.Time
}

func (b *tokenBucket) take(now time.Time, rate float64, burst int) bool {
	if b.last.IsZero() {
		b.tokens = float64(burst)
	} else {
		b.tokens = math.Min(float64(burst), b.tokens+now.Sub(b.last).Seconds()*rate)
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

type ipEntry struct {
	active   int
	lastSeen time.Time
	bucket   tokenBucket
}

// Admission happens synchronously in the accept loop, BEFORE any handler is
// spawned. Both active handlers and attacker-controlled per-IP state are bounded.
type admission struct {
	mu        sync.Mutex
	cfg       Config
	active    int
	pending   int
	ips       map[netip.Addr]*ipEntry
	global    tokenBucket
	lastSweep time.Time
}

func newAdmission(cfg Config) *admission {
	return &admission{cfg: cfg, ips: make(map[netip.Addr]*ipEntry)}
}

func (a *admission) admit(ip netip.Addr, now time.Time) bool {
	return a.admitKind(ip, now, false)
}

func (a *admission) admitHandshake(ip netip.Addr, now time.Time) bool {
	return a.admitKind(ip, now, true)
}

func (a *admission) admitKind(ip netip.Addr, now time.Time, handshake bool) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.active >= a.cfg.MaxConnections || (handshake && a.pending >= a.cfg.MaxPendingHandshakes) || !a.global.take(now, a.cfg.ConnectionRate, a.cfg.ConnectionBurst) {
		return false
	}
	if now.Sub(a.lastSweep) >= time.Minute || len(a.ips) >= a.cfg.MaxIPEntries {
		for key, entry := range a.ips {
			if entry.active == 0 && now.Sub(entry.lastSeen) >= time.Duration(a.cfg.IPEntryTTL) {
				delete(a.ips, key)
			}
		}
		a.lastSweep = now
	}
	e := a.ips[ip]
	if e == nil {
		if len(a.ips) >= a.cfg.MaxIPEntries {
			return false
		}
		e = &ipEntry{}
		a.ips[ip] = e
	}
	e.lastSeen = now
	if e.active >= a.cfg.MaxConnectionsPerIP || !e.bucket.take(now, a.cfg.IPConnectionRate, a.cfg.IPConnectionBurst) {
		return false
	}
	e.active++
	a.active++
	if handshake {
		a.pending++
	}
	return true
}

func (a *admission) finishHandshake() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pending--
}

func (a *admission) release(ip netip.Addr, now time.Time, pendingHandshake ...bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.active--
	if len(pendingHandshake) > 0 && pendingHandshake[0] {
		a.pending--
	}
	e := a.ips[ip]
	e.active--
	e.lastSeen = now
}

func (a *admission) pendingCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pending
}

func (a *admission) counts() (active, tracked int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.active, len(a.ips)
}
