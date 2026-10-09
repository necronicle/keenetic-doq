package geo

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"reflect"

	"github.com/miekg/dns"

	"github.com/necronicle/keenetic-doq/internal/upstream"
)

// Reselect просит фоновый цикл переоценить серверы обхода.
func (l *Lanes) Reselect(reason string) {
	select {
	case l.reselect <- reason:
	default: // переоценка уже запрошена
	}
}

// notePinned учитывает исход запроса к закреплённому: три неудачи подряд —
// отказ. Считаются только A/AAAA: geohide сбрасывает поток (код 5) на каждый
// HTTPS/SVCB, а Safari и Chrome шлют HTTPS на каждую навигацию — иначе сервер
// «отказывал» бы от обычного сёрфинга. SERVFAIL/REFUSED на A/AAAA — неудача,
// как и ошибка.
func (l *Lanes) notePinned(url string, qtype uint16, r exResult) {
	if qtype != dns.TypeA && qtype != dns.TypeAAAA {
		return
	}
	l.mu.Lock()
	if url != l.pinned {
		l.mu.Unlock()
		return
	}
	trip := false
	switch {
	case r.ok():
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
	default:
		l.fails++
		trip = l.fails == failsToFailover
	}
	l.mu.Unlock()
	if trip {
		go l.failover("pinned server failed 3 queries in a row")
	}
}

// failover переходит на следующий по рейтингу сервер, здоровый на последней
// оценке, и просит полную переоценку. Сначала проверяется сеть: если и быстрая
// полоса молчит, закреплённый не виноват — переключение и переоценка дали бы
// только пустой рейтинг.
func (l *Lanes) failover(reason string) {
	if !l.networkUp() {
		l.mu.Lock()
		cur := l.pinned
		l.fails, l.deadCycles = 0, 0
		l.mu.Unlock()
		slog.Warn("geo: the network looks down (the fast lane does not answer either), keeping the pinned server",
			"server", cur, "reason", reason)
		return
	}
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

// networkUp — отвечает ли быстрая полоса на A-запрос известного имени.
func (l *Lanes) networkUp() bool {
	ctx, cancel := context.WithTimeout(context.Background(), netCheckTimeout)
	defer cancel()
	m := new(dns.Msg)
	m.SetQuestion(netCheckName, dns.TypeA)
	resp, err := l.cfg.Fast.Exchange(ctx, m)
	return err == nil && resp != nil && !upstream.SoftFail(resp)
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
	// Тот же сервер и тот же рейтинг — флешку не трогать (время оценки на
	// диске при этом остаётся прежним).
	if s := l.saved; s == nil || s.Pinned != st.Pinned || !s.Since.Equal(st.Since) || !reflect.DeepEqual(s.Ranking, st.Ranking) {
		if err := SaveState(l.cfg.StatePath, &st); err != nil {
			slog.Warn("geo: cannot save state", "path", l.cfg.StatePath, "err", err)
		} else {
			l.saved = &st
		}
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
		AttemptedAt: l.attemptedAt, Evaluating: l.evaluating, Ranking: append([]Result(nil), l.ranking...),
		Fails: l.fails}
	if l.retryDelay > 0 {
		s.Inconclusive = true
		s.RetryAt = l.attemptedAt.Add(l.retryDelay)
	}
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
