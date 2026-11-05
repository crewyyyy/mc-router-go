package mcproto

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"unicode/utf8"
)

func regressionVarInt(n uint32) []byte {
	var out []byte
	for {
		b := byte(n & 0x7f)
		n >>= 7
		if n != 0 {
			b |= 0x80
		}
		out = append(out, b)
		if n == 0 {
			return out
		}
	}
}

func regressionWrap(payload []byte) []byte {
	return append(regressionVarInt(uint32(len(payload))), payload...)
}

func regressionHandshake(host string) []byte {
	payload := []byte{0, 0xfb, 5} // handshake packet, protocol 763
	payload = append(payload, regressionVarInt(uint32(len(host)))...)
	payload = append(payload, host...)
	payload = append(payload, 0x63, 0xdd, 2)
	return regressionWrap(payload)
}

func TestRegressionVarIntTruncationAndOverflow(t *testing.T) {
	for _, tc := range []struct {
		name string
		wire []byte
	}{
		{"empty", nil},
		{"truncated_one", []byte{0x80}},
		{"truncated_four", []byte{0x80, 0x80, 0x80, 0x80}},
		{"fifth_byte_overflow", []byte{0xff, 0xff, 0xff, 0xff, 0x10}},
		{"fifth_byte_continues", []byte{0x80, 0x80, 0x80, 0x80, 0x80, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := bytes.NewReader(tc.wire)
			if _, _, err := readVarInt(r); err == nil {
				t.Fatal("malformed VarInt accepted")
			}
			if consumed := len(tc.wire) - r.Len(); consumed > 5 {
				t.Fatalf("VarInt parser read beyond five-byte bound: %d", consumed)
			}
		})
	}
	for _, tc := range []struct {
		wire []byte
		want int32
	}{
		{[]byte{0}, 0},
		{[]byte{0x80, 0}, 0}, // Valid, non-minimal encoding must retain its wire.
		{[]byte{0xff, 0xff, 0xff, 0xff, 7}, 0x7fffffff},
		{[]byte{0xff, 0xff, 0xff, 0xff, 0x0f}, -1},
	} {
		r := bytes.NewReader(append(append([]byte(nil), tc.wire...), 0x42))
		got, raw, err := readVarInt(r)
		if err != nil || got != tc.want || !bytes.Equal(raw, tc.wire) || r.Len() != 1 {
			t.Fatalf("valid VarInt changed or read ahead: got=%d raw=%x remaining=%d err=%v", got, raw, r.Len(), err)
		}
	}
}

func TestRegressionHandshakeRejectsEveryTruncatedPrefix(t *testing.T) {
	wire := regressionHandshake("example.com")
	for n := 0; n < len(wire); n++ {
		if _, err := ReadHandshake(bytes.NewReader(wire[:n])); err == nil {
			t.Fatalf("accepted truncated handshake of %d/%d bytes", n, len(wire))
		}
	}
}

func TestRegressionHandshakeFrameLengthBoundBeforeBodyRead(t *testing.T) {
	for _, n := range []uint32{0, MaxHandshakeBytes + 1, 0x7fffffff, 0xffffffff} {
		prefix := regressionVarInt(n)
		r := bytes.NewReader(append(append([]byte(nil), prefix...), []byte("unread body")...))
		if _, err := ReadHandshake(r); err == nil {
			t.Fatalf("invalid frame length accepted: %d", n)
		}
		if consumed := len(prefix) + len("unread body") - r.Len(); consumed != len(prefix) {
			t.Fatalf("invalid length %d consumed body bytes: %d", n, consumed)
		}
	}
	// A frame exactly at the byte bound is read, then rejected for its content;
	// a following packet must still remain untouched even on that error path.
	payload := make([]byte, MaxHandshakeBytes)
	r := bytes.NewReader(append(regressionWrap(payload), 0x42))
	if _, err := ReadHandshake(r); err == nil || r.Len() != 1 {
		t.Fatalf("bounded malformed frame read ahead: remaining=%d err=%v", r.Len(), err)
	}
}

func TestConfiguredHandshakeLimitRejectsFrameBeforeBodyRead(t *testing.T) {
	const configuredLimit = 128
	prefix := regressionVarInt(configuredLimit + 1)
	r := bytes.NewReader(append(append([]byte(nil), prefix...), bytes.Repeat([]byte{0xff}, configuredLimit+1)...))
	if _, err := ReadHandshakeLimit(r, configuredLimit); err == nil {
		t.Fatal("frame over configured limit accepted")
	}
	if remaining := r.Len(); remaining != configuredLimit+1 {
		t.Fatalf("configured limit read/allocated body before rejecting: remaining=%d", remaining)
	}

	wire := regressionHandshake("example.com")
	if _, err := ReadHandshakeLimit(bytes.NewReader(wire), configuredLimit); err != nil {
		t.Fatalf("frame below configured limit rejected: %v", err)
	}
}

func TestRegressionHandshakeRejectsNestedVarIntOverflow(t *testing.T) {
	overflow := []byte{0xff, 0xff, 0xff, 0xff, 0x10}
	validSuffix := []byte{1, 'a', 0x63, 0xdd, 2}
	for _, tc := range []struct {
		name    string
		payload []byte
	}{
		{"packet_id", append(append([]byte(nil), overflow...), 0xfb, 5)},
		{"protocol", append(append([]byte{0}, overflow...), validSuffix...)},
		{"hostname_length", append(append([]byte{0, 0xfb, 5}, overflow...), 'a', 0x63, 0xdd, 2)},
		{"next_state", append([]byte{0, 0xfb, 5, 1, 'a', 0x63, 0xdd}, overflow...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ReadHandshake(bytes.NewReader(regressionWrap(tc.payload))); err == nil {
				t.Fatal("overflowing nested VarInt accepted")
			}
		})
	}
}

func TestRegressionHandshakeWirePreservedWithoutReadAhead(t *testing.T) {
	host := "BÜCHER.example.\x00FML3\x00"
	payload := []byte{0x80, 0, 0xfb, 0x85, 0} // non-minimal packet and version
	payload = append(payload, byte(len(host))|0x80, 0)
	payload = append(payload, host...)
	payload = append(payload, 0x63, 0xdd, 0x82, 0) // non-minimal next state
	wire := append([]byte{byte(len(payload)) | 0x80, 0}, payload...)
	nextPacket := []byte{0, 0xff, 0x80, 1, 0x42, 0, 0xfe}
	r := bytes.NewReader(append(append([]byte(nil), wire...), nextPacket...))
	h, err := ReadHandshake(r)
	if err != nil {
		t.Fatal(err)
	}
	if h.Hostname != "xn--bcher-kva.example" || h.ProtocolVersion != 763 || h.NextState != 2 || h.ServerPort != 25565 {
		t.Fatalf("unexpected handshake: %+v", h)
	}
	if !bytes.Equal(h.Wire, wire) {
		t.Fatalf("handshake wire was canonicalized: got=%x want=%x", h.Wire, wire)
	}
	remaining, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(remaining, nextPacket) {
		t.Fatalf("following packet consumed or modified: %x, %v", remaining, err)
	}
}

func TestRegressionLongIDNUsesJavaStringLimitNotByteLimit(t *testing.T) {
	label := strings.Repeat("é", 40)
	host := strings.Join([]string{label, label, label, label}, ".")
	if utf8.RuneCountInString(host) != 163 || len(host) != 323 {
		t.Fatal("long IDN regression fixture changed")
	}
	h, err := ReadHandshake(bytes.NewReader(regressionHandshake(host)))
	if err != nil {
		t.Fatal("valid 163-character, 323-byte Java hostname rejected:", err)
	}
	asciiLabel := "xn--9c" + strings.Repeat("a", 40)
	want := strings.Join([]string{asciiLabel, asciiLabel, asciiLabel, asciiLabel}, ".")
	if h.Hostname != want {
		t.Fatalf("long IDN normalized incorrectly: got=%q want=%q", h.Hostname, want)
	}
}

func TestRegressionAstralHostnameCountsUTF16Units(t *testing.T) {
	// Metadata is retained on wire but excluded from route lookup, allowing
	// astral characters here without relying on IDNA admitting emoji labels.
	atLimit := "a\x00" + strings.Repeat("😀", 126) + "b" // 255 UTF-16 units
	overLimit := "a\x00" + strings.Repeat("😀", 127)     // 256 UTF-16 units
	if utf8.RuneCountInString(overLimit) >= MaxAddressUnits || len(overLimit) > MaxAddressBytes {
		t.Fatal("fixture must fit rune/byte limits but exceed the UTF-16 limit")
	}
	h, err := ReadHandshake(bytes.NewReader(regressionHandshake(atLimit)))
	if err != nil || h.Hostname != "a" {
		t.Fatalf("exact UTF-16 boundary rejected: %+v, %v", h, err)
	}
	if _, err := ReadHandshake(bytes.NewReader(regressionHandshake(overLimit))); err == nil {
		t.Fatal("astral characters were counted as one UTF-16 unit instead of two")
	}
	if _, err := ReadHandshake(bytes.NewReader(regressionHandshake("a\x00" + strings.Repeat("界", 254)))); err == nil {
		t.Fatal("BMP metadata exceeding 255 UTF-16 units accepted")
	}
}

func TestRegressionAddressMetadataNormalization(t *testing.T) {
	for _, tc := range []struct{ address, want string }{
		{"EXAMPLE.COM.\x00FML3\x00", "example.com"},
		{"EXAMPLE.COM.\\infinity-filter", "example.com"},
		{"proxy.invalid?_o=B%C3%9CCHER.example.%3A25565&tag=ignored", "xn--bcher-kva.example"},
		{"proxy.invalid?_o=192.0.2.1%3A25565", "192.0.2.1"},
		{"proxy.invalid?_o=%5B2001%3Adb8%3A%3A1%5D%3A25565", "2001:db8::1"},
		{"EXAMPLE.COM.?tag=x&_o=", "example.com"},
		{"proxy.invalid?_o=EXAMPLE.COM.%3A25565\x00FML2\x00", "example.com"},
	} {
		got, err := NormalizeServerAddress(tc.address)
		if err != nil || got != tc.want {
			t.Errorf("NormalizeServerAddress(%q)=%q,%v; want %q", tc.address, got, err, tc.want)
		}
	}
	for _, address := range []string{"example.com?_o=%zz", "example.com?_o=bad%0ahost", "example.com?_o=bad%1bhost"} {
		if _, err := NormalizeServerAddress(address); err == nil {
			t.Errorf("unsafe query hostname accepted: %q", address)
		}
	}
}

func TestRegressionCanonicalHostRejectsLogControls(t *testing.T) {
	for _, control := range []string{"\n", "\r", "\x1b", "\t", "\x7f", "\u0085", " "} {
		host := "safe" + control + "forged.example"
		if _, err := CanonicalHost(host); err == nil {
			t.Errorf("control/space accepted by canonical hostname: %q", host)
		}
		if _, err := ReadHandshake(bytes.NewReader(regressionHandshake(host))); err == nil {
			t.Errorf("log-injection hostname accepted by handshake parser: %q", host)
		}
	}
	for _, host := range []string{"", ".", "a..example", "-a.example", "a-.example", "bad/name", string([]byte{0xff}), strings.Repeat("a", 64) + ".example"} {
		if _, err := CanonicalHost(host); err == nil {
			t.Errorf("malformed hostname accepted: %q", host)
		}
	}
}

func FuzzReadHandshakeRegression(f *testing.F) {
	for _, wire := range [][]byte{
		nil, {0x80}, {0xff, 0xff, 0xff, 0xff, 0x10},
		regressionVarInt(MaxHandshakeBytes + 1),
		regressionHandshake("example.com"),
		regressionHandshake("BÜCHER.example.\x00FML3\x00"),
		regressionHandshake("proxy.invalid?_o=EXAMPLE.COM.%3A25565"),
		regressionHandshake("a\x00" + strings.Repeat("😀", 126) + "b"),
		regressionHandshake("a\x00" + strings.Repeat("😀", 127)),
		regressionHandshake(strings.Repeat("é", 40) + "." + strings.Repeat("é", 40) + "." + strings.Repeat("é", 40) + "." + strings.Repeat("é", 40)),
	} {
		f.Add(wire)
	}
	f.Fuzz(func(t *testing.T, wire []byte) {
		r := bytes.NewReader(wire)
		h, err := ReadHandshake(r)
		consumed := len(wire) - r.Len()
		if consumed > MaxHandshakeBytes+5 {
			t.Fatalf("unbounded input consumption: %d", consumed)
		}
		if err != nil {
			return
		}
		if consumed != len(h.Wire) || !bytes.Equal(h.Wire, wire[:consumed]) {
			t.Fatal("successful parse changed wire bytes or consumed subsequent packets")
		}
		if canonical, err := CanonicalHost(h.Hostname); err != nil || canonical != h.Hostname {
			t.Fatalf("successful parse returned noncanonical hostname %q: %v", h.Hostname, err)
		}
	})
}
