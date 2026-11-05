package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func invoke(args ...string) (int, string, string) {
	oldArgs, oldOut, oldErr := os.Args, os.Stdout, os.Stderr
	defer func() { os.Args, os.Stdout, os.Stderr = oldArgs, oldOut, oldErr }()
	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	os.Args = append([]string{"mc-router"}, args...)
	os.Stdout, os.Stderr = outW, errW
	code := run()
	_ = outW.Close()
	_ = errW.Close()
	var out, stderr strings.Builder
	_, _ = outR.WriteTo(&out)
	_, _ = errR.WriteTo(&stderr)
	_ = outR.Close()
	_ = errR.Close()
	return code, out.String(), stderr.String()
}

func TestHelpAndVersionNeedNoConfig(t *testing.T) {
	if code, out, _ := invoke("--help"); code != 0 || !strings.Contains(out, "check-config") || !strings.Contains(out, "config.toml") {
		t.Fatalf("help: code=%d output=%q", code, out)
	}
	if code, out, _ := invoke("--version"); code != 0 || !strings.Contains(out, runtimeVersionPrefix) || !strings.Contains(out, "commit") {
		t.Fatalf("version: code=%d output=%q", code, out)
	}
}

const runtimeVersionPrefix = "mc-router "

func TestCheckConfigDoesNotListenOrDial(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	path := filepath.Join(t.TempDir(), "config.toml")
	config := "[server]\nlisten = \"" + occupied.Addr().String() + "\"\n[default]\nbackend = \"127.0.0.1:9\"\n"
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, stderr := invoke("--check-config", "--config", path); code != 0 || !strings.Contains(out, "configuration valid") {
		t.Fatalf("check config: code=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestInvalidCLIAndConfigReturnFailure(t *testing.T) {
	if code, _, _ := invoke("--unknown"); code == 0 {
		t.Fatal("unknown flag returned success")
	}
	path := filepath.Join(t.TempDir(), "invalid.toml")
	if err := os.WriteFile(path, []byte("[default]\nbackend=\"0.0.0.0:1234\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := invoke("--check-config", "--config", path); code == 0 || !strings.Contains(stderr, "invalid configuration") {
		t.Fatalf("invalid config: code=%d stderr=%q", code, stderr)
	}
}
