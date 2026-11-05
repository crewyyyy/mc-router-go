package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/crewyyyy/mc-router-go/internal/router"
)

var version = "dev"
var commit = "none"
var buildDate = "unknown"

const upstream = "itzg/mc-router@63534c550395a4b54ac4372fb8f2d108ee1bd213"

func main() { os.Exit(run()) }

func run() int {
	fs := flag.NewFlagSet("mc-router", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	path := fs.String("config", "/etc/mc-router/config.toml", "TOML configuration file")
	check := fs.Bool("check-config", false, "validate configuration and exit without opening sockets")
	showVersion := fs.Bool("version", false, "print version and upstream provenance")
	if err := fs.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments")
		return 2
	}
	if *showVersion {
		fmt.Printf("mc-router %s (commit %s, built %s, %s)\nupstream: %s\n", version, commit, buildDate, runtime.Version(), upstream)
		return 0
	}
	file, err := os.Open(*path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	cfg, err := router.LoadConfig(file)
	_ = file.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid configuration:", err)
		return 2
	}
	if *check {
		fmt.Println("configuration valid")
		return 0
	}

	level := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	if cfg.Transparent {
		if err := router.CheckTransparentSupport(); err != nil {
			logger.Error("transparent setup failed", "error", err.Error())
			return 1
		}
	}
	srv, err := router.New(cfg, logger)
	if err != nil {
		logger.Error("initialization failed", "error", err.Error())
		return 1
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		logger.Error("listen failed", "error", err.Error())
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	statsDone := make(chan struct{})
	defer close(statsDone)
	go func() {
		ticker := time.NewTicker(time.Duration(cfg.StatsInterval))
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				logger.Info("stats", "stats", srv.Stats())
			case <-statsDone:
				return
			}
		}
	}()
	if err := srv.Serve(ctx, ln); err != nil {
		logger.Error("serve failed", "error", err.Error())
		return 1
	}
	return 0
}
