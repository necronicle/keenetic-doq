// doqd — DNS-over-QUIC форвардер для Keenetic (слушает обычный DNS,
// резолвит через DoQ-апстримы). Регистрируется в KeeneticOS как
// name-server на <LAN-IP>:5354 вместо штатных DoT/DoH (при включённых
// DoT/DoH KeeneticOS его не спрашивает): loopback KeeneticOS в name-server
// не принимает, а 5353 занят avahi.
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

	"github.com/miekg/dns"

	"github.com/necronicle/keenetic-doq/internal/cache"
	"github.com/necronicle/keenetic-doq/internal/cli"
	"github.com/necronicle/keenetic-doq/internal/config"
	"github.com/necronicle/keenetic-doq/internal/geo"
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

	// Имена апстримов резолвятся через bootstrap, а не через системный
	// резолвер: на Keenetic тот указывает на ndnproxy, в списке серверов
	// которого прописан сам doqd — запрос вернулся бы нам же.
	boot := upstream.NewBootstrap(cfg.Bootstrap)
	// Один DoQ на URL: сервер обхода стоит и в быстрой полосе, и в гео-полосе,
	// а соединение у него должно быть одно.
	doqs := map[string]upstream.Exchanger{}
	build := func(urls []string) []upstream.Exchanger {
		var out []upstream.Exchanger
		for _, raw := range urls {
			if ex, ok := doqs[raw]; ok {
				out = append(out, ex)
				continue
			}
			u, err := upstream.NewDoQ(raw)
			if err != nil {
				slog.Error("bad upstream", "err", err)
				os.Exit(1)
			}
			u.SetBootstrap(boot)
			doqs[raw] = u
			out = append(out, u)
		}
		return out
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Быстрая полоса: обычные резолверы и серверы обхода вместе — для обычных
	// доменов серверы обхода отдают настоящие адреса и отвечают быстро.
	fast := upstream.NewPicker(append(build(cfg.Upstreams), build(cfg.Geo)...), build(cfg.Fallbacks)...)
	fast.StartHealthCheck(ctx, 30*time.Second)
	// Эталон для оценки серверов обхода — только обычные резолверы.
	var ref upstream.Exchanger
	if len(cfg.Upstreams)+len(cfg.Fallbacks) > 0 {
		ref = upstream.NewPicker(build(cfg.Upstreams), build(cfg.Fallbacks)...)
	}
	var geoServers []geo.Server
	for i, ex := range build(cfg.Geo) {
		geoServers = append(geoServers, geo.Server{URL: cfg.Geo[i], Ex: ex})
	}

	c := cache.New(cfg.CacheSize, cfg.MinTTL, cfg.MaxTTL)
	lanes := geo.New(geo.Config{
		Fast:         fast,
		Geo:          geoServers,
		Reference:    ref,
		Static:       geo.NewMatcher(geo.BuiltinDomains(), cfg.GeoDomains),
		Prober:       geo.NewTLSProber(2 * time.Second),
		StatePath:    geo.DefaultStatePath,
		SnapshotPath: geo.DefaultSnapshotPath,
		Flush:        c.DeleteIf,
		Stale:        func(m *dns.Msg) *dns.Msg { return c.GetStale(cache.KeyOf(m)) },
	})
	lanes.Start(ctx)
	// `doqd geo reselect` шлёт SIGUSR1 — переоценить серверы обхода.
	usr1 := make(chan os.Signal, 1)
	signal.Notify(usr1, syscall.SIGUSR1)
	go func() {
		for range usr1 {
			lanes.Reselect("requested by doqd geo reselect")
		}
	}()

	res := resolver.New(c, lanes)
	srv := server.New(cfg.Listen, res)
	// StartWait, а не Start: на загрузке роутера адрес интерфейса может ещё
	// не подняться, а второго шанса init-скрипт не даёт.
	if err := srv.StartWait(ctx); err != nil {
		slog.Error("listen failed", "addr", cfg.Listen, "err", err)
		os.Exit(1)
	}
	slog.Info("doqd started", "version", version, "listen", srv.Addr(), "geo", cfg.Geo,
		"upstreams", cfg.Upstreams, "fallbacks", cfg.Fallbacks, "geo_domains", cfg.GeoDomains,
		"bootstrap", cfg.Bootstrap)

	<-ctx.Done()
	slog.Info("shutting down")
	srv.Shutdown()
}
