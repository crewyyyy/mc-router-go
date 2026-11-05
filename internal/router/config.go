package router

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/crewyyyy/mc-router-go/internal/mcproto"
)

const maxConfigBytes = 1 << 20

// Config is the validated, immutable runtime snapshot. Use LoadConfig to read
// the public TOML format; callers should not populate this from user JSON.
type Config struct {
	Listen               string
	Routes               map[string]string
	DefaultBackend       string
	Transparent          bool
	MaxConnections       int
	MaxPendingHandshakes int
	MaxConnectionsPerIP  int
	MaxIPEntries         int
	ConnectionRate       float64
	ConnectionBurst      int
	IPConnectionRate     float64
	IPConnectionBurst    int
	IPEntryTTL           time.Duration
	HandshakeTimeout     time.Duration
	HandshakeMaxBytes    int
	DialTimeout          time.Duration
	WriteTimeout         time.Duration
	IdleTimeout          time.Duration
	HalfCloseTimeout     time.Duration
	ShutdownTimeout      time.Duration
	StatsInterval        time.Duration
	LogLevel             string
}

type fileConfig struct {
	Server struct {
		Listen string `toml:"listen"`
	} `toml:"server"`
	Routes  map[string]string `toml:"routes"`
	Default struct {
		Backend string `toml:"backend"`
	} `toml:"default"`
	Limits struct {
		MaxConnections       int     `toml:"max_connections"`
		MaxPendingHandshakes int     `toml:"max_pending_handshakes"`
		MaxConnectionsPerIP  int     `toml:"max_connections_per_ip"`
		MaxIPEntries         int     `toml:"max_ip_entries"`
		ConnectionRate       float64 `toml:"connection_rate"`
		ConnectionBurst      int     `toml:"connection_burst"`
		IPConnectionRate     float64 `toml:"ip_connection_rate"`
		IPConnectionBurst    int     `toml:"ip_connection_burst"`
		IPEntryTTL           string  `toml:"ip_entry_ttl"`
		HandshakeTimeout     string  `toml:"handshake_timeout"`
		HandshakeMaxBytes    int     `toml:"handshake_max_bytes"`
		DialTimeout          string  `toml:"dial_timeout"`
		WriteTimeout         string  `toml:"write_timeout"`
		IdleTimeout          string  `toml:"idle_timeout"`
		HalfCloseTimeout     string  `toml:"half_close_timeout"`
		ShutdownTimeout      string  `toml:"shutdown_timeout"`
		StatsInterval        string  `toml:"stats_interval"`
	} `toml:"limits"`
	Transparent struct {
		Enabled bool `toml:"enabled"`
	} `toml:"transparent"`
	Logging struct {
		Level string `toml:"level"`
	} `toml:"logging"`
}

func DefaultConfig() Config {
	return Config{
		Listen: "127.0.0.1:25565", Routes: make(map[string]string),
		MaxConnections: 256, MaxPendingHandshakes: 64, MaxConnectionsPerIP: 64, MaxIPEntries: 4096,
		ConnectionRate: 50, ConnectionBurst: 200,
		IPConnectionRate: 10, IPConnectionBurst: 40,
		IPEntryTTL: 10 * time.Minute, HandshakeTimeout: 5 * time.Second, HandshakeMaxBytes: 2048,
		DialTimeout: 3 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Minute,
		HalfCloseTimeout: 5 * time.Second, ShutdownTimeout: 10 * time.Second,
		StatsInterval: 30 * time.Second, LogLevel: "info",
	}
}

// LoadConfig decodes one bounded TOML document, rejects unknown keys and
// validates all fields before the caller opens any network socket.
func LoadConfig(r io.Reader) (Config, error) {
	cfg := DefaultConfig()
	data, err := io.ReadAll(io.LimitReader(r, maxConfigBytes+1))
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}
	if len(data) > maxConfigBytes {
		return cfg, errors.New("configuration exceeds 1 MiB")
	}

	file := fileConfig{}
	metadata, err := toml.NewDecoder(bytes.NewReader(data)).Decode(&file)
	if err != nil {
		return cfg, fmt.Errorf("decode TOML: %w", err)
	}
	if unused := metadata.Undecoded(); len(unused) != 0 {
		return cfg, fmt.Errorf("unknown configuration key %q", unused[0].String())
	}

	if metadata.IsDefined("server", "listen") {
		cfg.Listen = file.Server.Listen
	}
	if metadata.IsDefined("routes") {
		cfg.Routes = file.Routes
	}
	if metadata.IsDefined("default", "backend") {
		cfg.DefaultBackend = file.Default.Backend
	}
	if metadata.IsDefined("transparent", "enabled") {
		cfg.Transparent = file.Transparent.Enabled
	}

	setInt := func(key string, dst *int, src int) {
		if metadata.IsDefined("limits", key) {
			*dst = src
		}
	}
	setFloat := func(key string, dst *float64, src float64) {
		if metadata.IsDefined("limits", key) {
			*dst = src
		}
	}
	setDuration := func(key, value string, dst *time.Duration) error {
		if !metadata.IsDefined("limits", key) {
			return nil
		}
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("limits.%s must be a duration string such as 5s: %w", key, err)
		}
		*dst = parsed
		return nil
	}
	setInt("max_connections", &cfg.MaxConnections, file.Limits.MaxConnections)
	setInt("max_pending_handshakes", &cfg.MaxPendingHandshakes, file.Limits.MaxPendingHandshakes)
	setInt("max_connections_per_ip", &cfg.MaxConnectionsPerIP, file.Limits.MaxConnectionsPerIP)
	if metadata.IsDefined("limits", "max_connections") {
		if !metadata.IsDefined("limits", "max_pending_handshakes") && cfg.MaxPendingHandshakes > cfg.MaxConnections {
			cfg.MaxPendingHandshakes = cfg.MaxConnections
		}
		if !metadata.IsDefined("limits", "max_connections_per_ip") && cfg.MaxConnectionsPerIP > cfg.MaxConnections {
			cfg.MaxConnectionsPerIP = cfg.MaxConnections
		}
	}
	setInt("max_ip_entries", &cfg.MaxIPEntries, file.Limits.MaxIPEntries)
	setFloat("connection_rate", &cfg.ConnectionRate, file.Limits.ConnectionRate)
	setInt("connection_burst", &cfg.ConnectionBurst, file.Limits.ConnectionBurst)
	setFloat("ip_connection_rate", &cfg.IPConnectionRate, file.Limits.IPConnectionRate)
	setInt("ip_connection_burst", &cfg.IPConnectionBurst, file.Limits.IPConnectionBurst)
	for _, duration := range []struct {
		key, value string
		target     *time.Duration
	}{
		{"ip_entry_ttl", file.Limits.IPEntryTTL, &cfg.IPEntryTTL},
		{"handshake_timeout", file.Limits.HandshakeTimeout, &cfg.HandshakeTimeout},
		{"dial_timeout", file.Limits.DialTimeout, &cfg.DialTimeout},
		{"write_timeout", file.Limits.WriteTimeout, &cfg.WriteTimeout},
		{"idle_timeout", file.Limits.IdleTimeout, &cfg.IdleTimeout},
		{"half_close_timeout", file.Limits.HalfCloseTimeout, &cfg.HalfCloseTimeout},
		{"shutdown_timeout", file.Limits.ShutdownTimeout, &cfg.ShutdownTimeout},
		{"stats_interval", file.Limits.StatsInterval, &cfg.StatsInterval},
	} {
		if err := setDuration(duration.key, duration.value, duration.target); err != nil {
			return cfg, err
		}
	}
	setInt("handshake_max_bytes", &cfg.HandshakeMaxBytes, file.Limits.HandshakeMaxBytes)
	if metadata.IsDefined("logging", "level") {
		cfg.LogLevel = file.Logging.Level
	}

	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	listen, err := netip.ParseAddrPort(c.Listen)
	if err != nil || listen.Port() == 0 || listen.Addr().Zone() != "" || listen.Addr().IsMulticast() {
		return errors.New("server.listen must be a literal IP:nonzero-port")
	}
	if c.Transparent && !listen.Addr().Unmap().Is4() {
		return errors.New("transparent mode requires an IPv4 listener; namespace deployment is IPv4-only")
	}
	if c.MaxConnections < 1 || c.MaxConnections > 1000000 || c.MaxPendingHandshakes < 1 || c.MaxPendingHandshakes > c.MaxConnections {
		return errors.New("limits.max_pending_handshakes must be positive and <= limits.max_connections <= 1000000")
	}
	if c.MaxConnectionsPerIP < 1 || c.MaxConnectionsPerIP > c.MaxConnections {
		return errors.New("limits.max_connections_per_ip must be positive and <= limits.max_connections")
	}
	if c.MaxIPEntries < 1 || c.MaxIPEntries > 1000000 {
		return errors.New("limits.max_ip_entries must be between 1 and 1000000")
	}
	for _, rate := range []float64{c.ConnectionRate, c.IPConnectionRate} {
		if math.IsNaN(rate) || math.IsInf(rate, 0) || rate <= 0 || rate > 1000000 {
			return errors.New("connection rates must be finite and between 0 and 1000000")
		}
	}
	if c.ConnectionBurst < 1 || c.ConnectionBurst > 1000000 || c.IPConnectionBurst < 1 || c.IPConnectionBurst > 1000000 {
		return errors.New("connection bursts must be between 1 and 1000000")
	}
	for name, d := range map[string]time.Duration{
		"limits.handshake_timeout": c.HandshakeTimeout, "limits.dial_timeout": c.DialTimeout,
		"limits.write_timeout": c.WriteTimeout, "limits.idle_timeout": c.IdleTimeout,
		"limits.half_close_timeout": c.HalfCloseTimeout, "limits.shutdown_timeout": c.ShutdownTimeout,
		"limits.ip_entry_ttl": c.IPEntryTTL, "limits.stats_interval": c.StatsInterval,
	} {
		if d <= 0 || d > 24*time.Hour {
			return fmt.Errorf("%s must be positive and <= 24h", name)
		}
	}
	if c.HandshakeMaxBytes < 128 || c.HandshakeMaxBytes > 4096 {
		return errors.New("limits.handshake_max_bytes must be between 128 and 4096")
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return errors.New("logging.level must be debug, info, warn, or error")
	}
	if len(c.Routes) == 0 && c.DefaultBackend == "" {
		return errors.New("at least one [routes] entry or [default].backend is required")
	}
	keys := make(map[string]struct{}, len(c.Routes))
	for host, target := range c.Routes {
		if strings.ContainsAny(host, "?\\\x00") {
			return errors.New("route keys must be hostnames without handshake metadata")
		}
		key, err := mcproto.CanonicalHost(host)
		if err != nil {
			return fmt.Errorf("invalid route key: %w", err)
		}
		if _, exists := keys[key]; exists {
			return errors.New("duplicate normalized route key")
		}
		keys[key] = struct{}{}
		if err := validateBackend(target, c.Transparent); err != nil {
			return fmt.Errorf("route %q: %w", key, err)
		}
	}
	if c.DefaultBackend != "" {
		if err := validateBackend(c.DefaultBackend, c.Transparent); err != nil {
			return fmt.Errorf("default.backend: %w", err)
		}
	}
	return nil
}

func validateBackend(target string, transparent bool) error {
	host, port, err := net.SplitHostPort(target)
	if err != nil || host == "" {
		return errors.New("backend must be host:nonzero-port")
	}
	// Validate the port separately so both DNS names and bracketed IPv6 targets
	// are accepted without letting a malformed port reach DialContext.
	parsedPort, err := strconv.ParseUint(port, 10, 16)
	if err != nil || parsedPort == 0 {
		return errors.New("backend port must be an integer from 1 to 65535")
	}
	address, err := netip.ParseAddr(host)
	if err == nil {
		if address.Zone() != "" || address.IsUnspecified() || address.IsMulticast() {
			return errors.New("backend IP must be a unicast address without a zone")
		}
		if transparent && (!address.Is4() || address.IsLoopback()) {
			return errors.New("transparent mode requires non-loopback IPv4 backends; namespace deployment is IPv4-only")
		}
		return nil
	}
	if transparent {
		return errors.New("transparent mode requires literal IPv4 backend addresses")
	}
	if _, err := mcproto.CanonicalHost(host); err != nil {
		return fmt.Errorf("invalid backend hostname: %w", err)
	}
	return nil
}
