// doqd — DNS-over-QUIC форвардер для Keenetic (слушает обычный DNS,
// резолвит через DoQ-апстримы). Регистрируется в KeeneticOS как
// name-server на 127.0.0.1:5353 рядом со штатными DoT/DoH.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/necronicle/keenetic-doq/internal/cache"
	"github.com/necronicle/keenetic-doq/internal/cli"
	"github.com/necronicle/keenetic-doq/internal/config"
	"github.com/necronicle/keenetic-doq/internal/resolver"
	"github.com/necronicle/keenetic-doq/internal/server"
	"github.com/necronicle/keenetic-doq/internal/upstream"
)

var version = "dev" // подставляется при сборке через -ldflags "-X main.version=..."

func main() {
	// Всё, что не флаг, — обращение к утилите управления; неизвестная
	// подкоманда должна получить usage, а не подняться демоном.
	if !cli.IsDaemonArgs(os.Args) {
		os.Exit(cli.Run(os.Args[1:]))
	}

	confPath := flag.String("c", "/opt/etc/doqd.conf", "path to config file")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("doqd", version)
		return
	}

	cfg, err := config.Load(*confPath)
	if err != nil {
		if os.IsNotExist(err) {
			slog.Info("config not found, using defaults", "path", *confPath)
			cfg = config.Default()
		} else {
			slog.Error("bad config", "err", err)
			os.Exit(1)
		}
	}

	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		level = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	var ups []upstream.Exchanger
	for _, raw := range cfg.Upstreams {
		u, err := upstream.NewDoQ(raw)
		if err != nil {
			slog.Error("bad upstream", "err", err)
			os.Exit(1)
		}
		ups = append(ups, u)
	}
	picker := upstream.NewPicker(ups)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	picker.StartHealthCheck(ctx, 30*time.Second)

	res := resolver.New(cache.New(cfg.CacheSize, cfg.MinTTL, cfg.MaxTTL), picker)
	srv := server.New(cfg.Listen, res)
	if err := srv.Start(); err != nil {
		slog.Error("listen failed", "addr", cfg.Listen, "err", err)
		os.Exit(1)
	}
	slog.Info("doqd started", "version", version, "listen", srv.Addr(), "upstreams", cfg.Upstreams)

	<-ctx.Done()
	slog.Info("shutting down")
	srv.Shutdown()
}
