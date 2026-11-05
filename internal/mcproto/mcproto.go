// Package mcproto inspects only the initial Minecraft Java handshake.
// Adapted from itzg/mc-router mcproto/decode.go and server/routes.go at
// 63534c550395a4b54ac4372fb8f2d108ee1bd213. See THIRD_PARTY_NOTICES.md.
package mcproto

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

const (
	MaxHandshakeBytes = 4096
	MaxAddressUnits   = 255 // Java String(255): UTF-16 code units, NOT UTF-8 bytes.
	MaxAddressBytes   = MaxAddressUnits * 3
)

// Handshake retains the original framing and Forge/query metadata for replay.
// No login, encryption, compression, player identity, or subsequent packets are decoded.
type Handshake struct {
	ProtocolVersion int32
	Hostname        string
	ServerPort      uint16
	NextState       int32
	Wire            []byte
}

// readVarInt implements the wire's signed 32-bit VarInt. The fifth byte may
// contain only four data bits. Reads consume exactly one byte, never read ahead.
func readVarInt(r io.Reader) (int32, []byte, error) {
	var value uint32
	var raw [5]byte
	for i := range raw {
		if _, err := io.ReadFull(r, raw[i:i+1]); err != nil {
			return 0, nil, err
		}
		b := raw[i]
		if i == 4 && b&0xf0 != 0 {
			return 0, nil, errors.New("VarInt exceeds 32 bits")
		}
		value |= uint32(b&0x7f) << (7 * i)
		if b&0x80 == 0 {
			return int32(value), raw[:i+1], nil
		}
	}
	return 0, nil, errors.New("VarInt exceeds five bytes")
}

// ReadHandshake reads a bounded frame without consuming any following login
// or status bytes. The caller must install one absolute socket deadline first.
func ReadHandshake(r io.Reader) (*Handshake, error) {
	return ReadHandshakeLimit(r, MaxHandshakeBytes)
}

// ReadHandshakeLimit decodes the first frame with a caller-selected allocation
// limit. The absolute time deadline remains the caller's responsibility.
func ReadHandshakeLimit(r io.Reader, maxBytes int) (*Handshake, error) {
	if maxBytes < 1 || maxBytes > MaxHandshakeBytes {
		return nil, errors.New("handshake limit must be between 1 and protocol maximum")
	}
	n, prefix, err := readVarInt(r)
	if err != nil {
		return nil, fmt.Errorf("frame length: %w", err)
	}
	if n < 1 || n > MaxHandshakeBytes {
		return nil, errors.New("handshake frame length out of bounds")
	}
	if n > int32(maxBytes) {
		return nil, errors.New("handshake frame exceeds configured byte limit")
	}
	payload := make([]byte, int(n))
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, fmt.Errorf("handshake frame: %w", err)
	}
	b := bytes.NewReader(payload)
	packetID, _, err := readVarInt(b)
	if err != nil || packetID != 0 {
		return nil, errors.New("first packet is not a handshake")
	}
	version, _, err := readVarInt(b)
	if err != nil {
		return nil, fmt.Errorf("protocol version: %w", err)
	}
	length, _, err := readVarInt(b)
	if err != nil || length < 1 || length > MaxAddressBytes || int(length) > b.Len() {
		return nil, errors.New("handshake hostname byte length out of bounds")
	}
	rawHost := make([]byte, int(length))
	if _, err := io.ReadFull(b, rawHost); err != nil {
		return nil, err
	}
	if !utf8.Valid(rawHost) {
		return nil, errors.New("handshake hostname is not UTF-8")
	}
	units := 0
	for _, r := range string(rawHost) {
		units++
		if r > 0xffff {
			units++
		}
	}
	if units > MaxAddressUnits {
		return nil, errors.New("handshake hostname exceeds UTF-16 length limit")
	}
	host, err := NormalizeServerAddress(string(rawHost))
	if err != nil {
		return nil, err
	}
	var port [2]byte
	if _, err := io.ReadFull(b, port[:]); err != nil {
		return nil, fmt.Errorf("server port: %w", err)
	}
	state, _, err := readVarInt(b)
	if err != nil || (state != 1 && state != 2 && state != 3) {
		return nil, errors.New("invalid handshake next state")
	}
	if b.Len() != 0 {
		return nil, errors.New("unexpected trailing handshake data")
	}
	wire := make([]byte, 0, len(prefix)+len(payload))
	wire = append(wire, prefix...)
	wire = append(wire, payload...)
	return &Handshake{ProtocolVersion: version, Hostname: host,
		ServerPort: uint16(port[0])<<8 | uint16(port[1]), NextState: state, Wire: wire}, nil
}

// NormalizeServerAddress follows upstream's Forge, infinity-filter, trailing
// root-dot and modern _o query handling. None of that metadata is trusted as a
// client IP or identity. It selects ONLY a preconfigured static route.
func NormalizeServerAddress(address string) (string, error) {
	address, _, _ = strings.Cut(address, "\x00")
	address, _, _ = strings.Cut(address, "\\")
	if host, query, found := strings.Cut(address, "?"); found {
		address = host
		values, err := url.ParseQuery(query)
		if err != nil {
			return "", errors.New("invalid handshake hostname query")
		}
		if origin := values.Get("_o"); origin != "" {
			if originHost, _, err := net.SplitHostPort(origin); err == nil {
				origin = originHost
			}
			address = origin
		}
	}
	return CanonicalHost(address)
}

// CanonicalHost is shared by route configuration and handshake lookup. Unicode
// IDNs and their ASCII punycode forms resolve to the same key. Control characters
// are rejected, not removed (removal would silently change the selected route).
func CanonicalHost(host string) (string, error) {
	if !utf8.ValidString(host) || host == "" {
		return "", errors.New("empty or invalid hostname")
	}
	for _, r := range host {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return "", errors.New("control character or whitespace in hostname")
		}
	}
	host = strings.TrimSuffix(host, ".")
	ipHost := strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if ip, err := netip.ParseAddr(ipHost); err == nil && ip.Zone() == "" {
		return ip.Unmap().String(), nil
	}
	ascii, err := idna.Lookup.ToASCII(host)
	if err != nil || ascii == "" || len(ascii) > 253 {
		return "", errors.New("invalid IDNA hostname")
	}
	ascii = strings.ToLower(ascii)
	for _, label := range strings.Split(ascii, ".") {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("invalid DNS label")
		}
		for _, c := range label {
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
				return "", errors.New("invalid hostname character")
			}
		}
	}
	return ascii, nil
}
