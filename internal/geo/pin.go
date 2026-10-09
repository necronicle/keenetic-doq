package geo

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"

	"github.com/miekg/dns"
)

// Reselect просит фоновый цикл переоценить серверы обхода.
func (l *Lanes) Reselect(reason string) {
	select {
	case l.reselect <- reason:
	default: // переоценка уже запрошена
	}
}

// notePinned учитывает исход запроса к закреплённому: три ошибки подряд — отказ.
func (l *Lanes) notePinned(url string, r exResult) {
	l.mu.Lock()
	if url != l.pinned {
		l.mu.Unlock()
		return
	}
	trip := false
	switch {
	case r.err == nil && r.resp != nil:
		l.fails = 0
		if r.rtt > 0 {
			if l.rtt == 0 {
				l.rtt = r.rtt
			} else {
				l.rtt = (l.rtt*7 + r.rtt) / 8
			}
		}
	case errors.Is(r.err, context.Canceled):
		// клиент ушёл — сервер не виноват
	case r.err != nil:
		l.fails++
		trip = l.fails == failsToFailover
	}
	l.mu.Unlock()
	if trip {
		go l.failover("pinned server failed 3 queries in a row")
	}
}

// failover переходит на следующий по рейтингу сервер, здоровый на последней
// оценке, и просит полную переоценку.
func (l *Lanes) failover(reason string) {
	l.mu.Lock()
	cur := l.pinned
	next := ""
	for _, r := range l.ranking {
		if r.URL != cur && r.Coverage > 0 && r.Alive > 0 {
			next = r.URL
			break
		}
	}
	l.fails, l.deadCycles = 0, 0
	l.mu.Unlock()
	if next != "" {
		l.pin(next, reason)
	} else {
		slog.Warn("geo: pinned server is failing and there is no healthy spare", "server", cur, "reason", reason)
	}
	l.Reselect(reason)
}

// poolSNI — пул сервера из рейтинга: адрес → SNI для проверок.
func poolSNI(url string, rs []Result) map[netip.Addr]string {
	out := map[netip.Addr]string{}
	for _, r := range rs {
		if r.URL != url {
			continue
		}
		for ip, sni := range r.SNI {
			if a, err := netip.ParseAddr(ip); err == nil {
				out[a] = sni
			}
		}
	}
	return out
}

// pin закрепляет сервер, сохраняет выбор и при смене сбрасывает из кеша
// ответы гео-полосы — старые прокси не должны дожить до конца TTL.
// Всё под замком: выбор виден через Pinned() только после сохранения
// состояния и снимка.
func (l *Lanes) pin(url, reason string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	changed := url != l.pinned
	if changed || l.since.IsZero() {
		l.pinned, l.since = url, l.now()
	}
	if changed {
		l.rtt = 0
	}
	l.fails, l.deadCycles = 0, 0
	st := State{Pinned: l.pinned, Since: l.since, EvaluatedAt: l.evaluatedAt, Ranking: l.ranking}
	l.health.Reset(poolSNI(url, st.Ranking))
	if err := SaveState(l.cfg.StatePath, &st); err != nil {
		slog.Warn("geo: cannot save state", "path", l.cfg.StatePath, "err", err)
	}
	if changed {
		n := l.cfg.Flush(func(m *dns.Msg) bool {
			return len(m.Question) > 0 && l.class.Lookup(m.Question[0].Name) == Geo
		})
		slog.Info("geo: pinned", "server", url, "reason", reason, "flushed", n)
	}
	l.writeSnapshotLocked()
}

func (l *Lanes) Snapshot() Snapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.snapshotLocked()
}

// snapshotLocked — Snapshot для вызывающего, уже держащего l.mu.
func (l *Lanes) snapshotLocked() Snapshot {
	s := Snapshot{Time: l.now(), Pinned: l.pinned, Since: l.since, EvaluatedAt: l.evaluatedAt,
		Evaluating: l.evaluating, Ranking: append([]Result(nil), l.ranking...), Fails: l.fails}
	s.Proxies = l.health.Snapshot()
	s.LearnedGeo = l.class.GeoCount()
	return s
}

func (l *Lanes) writeSnapshot() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.writeSnapshotLocked()
}

func (l *Lanes) writeSnapshotLocked() {
	if l.cfg.SnapshotPath == "" {
		return
	}
	if err := WriteSnapshot(l.cfg.SnapshotPath, l.snapshotLocked()); err != nil {
		slog.Warn("geo: cannot write snapshot", "path", l.cfg.SnapshotPath, "err", err)
	}
}
