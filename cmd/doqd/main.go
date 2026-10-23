// doqd — DNS-over-QUIC форвардер для Keenetic и OpenWRT (слушает обычный
// DNS, резолвит через DoQ-апстримы). На Keenetic регистрируется в KeeneticOS
// как name-server на <LAN-IP>:5354 вместо штатных DoT/DoH (при включённых
// DoT/DoH KeeneticOS его не спрашивает): loopback KeeneticOS в name-server
// не принимает, а 5353 занят avahi. На OpenWRT слушает 127.0.0.1:5354 и
// стоит единственным сервером dnsmasq.
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

	// Корневые сертификаты на случай, если в прошивке их нет: без них не
	// проверить TLS ни одного апстрима. В KeeneticOS они лежат в /etc/ssl/certs,
	// а на OpenWRT пакет ca-bundle есть не в каждой сборке. Системные корни,
	// если они есть, важнее встроенных.
	_ "golang.org/x/crypto/x509roots/fallback"

	"github.com/necronicle/keenetic-doq/internal/cache"
	"github.com/necronicle/keenetic-doq/internal/cli"
	"github.com/necronicle/keenetic-doq/internal/config"
	"github.com/necronicle/keenetic-doq/internal/platform"
	"github.com/necronicle/keenetic-doq/internal/resolver"
	"github.com/necronicle/keenetic-doq/internal/server"
	"github.com/necronicle/keenetic-doq/internal/smartdns"
	"github.com/necronicle/keenetic-doq/internal/upstream"
)

var version = "dev" // подставляется при сборке через -ldflags "-X main.version=..."

func main() {
	// Всё, что не флаг, — обращение к утилите управления; неизвестная
	// подкоманда должна получить usage, а не подняться демоном.
	if !cli.IsDaemonArgs(os.Args) {
		os.Exit(cli.Run(os.Args[1:]))
	}

	host := platform.Detect()
	confPath := flag.String("c", host.Conf, "path to config file")
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
	// резолвер: на Keenetic тот указывает на ndnproxy, на OpenWRT — на
	// dnsmasq, и в списке серверов обоих прописан сам doqd — запрос вернулся
	// бы нам же.
	boot := upstream.NewBootstrap(cfg.Bootstrap)
	// Reuse one DoQ connection for each configured URL.
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

	pool := upstream.NewPicker(build(cfg.Upstreams), build(cfg.Fallbacks)...)
	pool.StartHealthCheck(ctx, 30*time.Second)
	c := cache.New(cfg.CacheSize, cfg.MinTTL, cfg.MaxTTL)
	res, err := smartdns.New(cfg.Hosts, resolver.New(c, pool))
	if err != nil {
		slog.Error("bad hosts source", "source", cfg.Hosts, "err", err)
		os.Exit(1)
	}
	// SIGHUP reloads only the hosts source; a failed parse retains the old table.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	go reloadHosts(ctx, res, hup)

	srv := server.New(cfg.Listen, res)
	// StartWait, а не Start: на загрузке роутера адрес интерфейса может ещё
	// не подняться, а второго шанса init-скрипт не даёт.
	if err := srv.StartWait(ctx); err != nil {
		slog.Error("listen failed", "addr", cfg.Listen, "err", err)
		os.Exit(1)
	}
	slog.Info("doqd started", "version", version, "listen", srv.Addr(), "hosts", cfg.Hosts,
		"upstreams", cfg.Upstreams, "fallbacks", cfg.Fallbacks,
		"bootstrap", cfg.Bootstrap, "platform", host.String())

	<-ctx.Done()
	slog.Info("shutting down")
	srv.Shutdown()
}

func reloadHosts(ctx context.Context, res *smartdns.Resolver, signals <-chan os.Signal) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-signals:
			if !ok {
				return
			}
			if err := res.Reload(); err != nil {
				slog.Error("hosts reload failed; retaining previous snapshot", "err", err)
			} else {
				slog.Info("hosts reloaded")
			}
		}
	}
}
