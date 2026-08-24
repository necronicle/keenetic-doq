package upstream

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

type Exchanger interface {
	Exchange(ctx context.Context, m *dns.Msg) (*dns.Msg, error)
	Address() string
}

const downCooldown = 30 * time.Second

type upstreamState struct {
	ex        Exchanger
	rtt       time.Duration // EWMA; 0 = замеров ещё не было
	downUntil time.Time
}

type Picker struct {
	mu  sync.Mutex
	ups []*upstreamState
	now func() time.Time
}

func NewPicker(ups []Exchanger) *Picker {
	p := &Picker{now: time.Now}
	for _, u := range ups {
		p.ups = append(p.ups, &upstreamState{ex: u})
	}
	return p
}

// ordered: живые по возрастанию EWMA RTT (незамеренные, rtt=0, пробуются
// рано), затем упавшие — последней надеждой.
func (p *Picker) ordered() []*upstreamState {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	var live, down []*upstreamState
	for _, st := range p.ups {
		if st.downUntil.After(now) {
			down = append(down, st)
		} else {
			live = append(live, st)
		}
	}
	sort.SliceStable(live, func(i, j int) bool { return live[i].rtt < live[j].rtt })
	return append(live, down...)
}

func (p *Picker) markSuccess(st *upstreamState, rtt time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st.downUntil = time.Time{}
	if st.rtt == 0 {
		st.rtt = rtt
	} else {
		st.rtt = (st.rtt*7 + rtt) / 8
	}
}

func (p *Picker) markDown(st *upstreamState) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st.downUntil = p.now().Add(downCooldown)
}

func (p *Picker) isDown(ex Exchanger) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, st := range p.ups {
		if st.ex == ex {
			return st.downUntil.After(p.now())
		}
	}
	return false
}

func (p *Picker) downStates() []*upstreamState {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	var down []*upstreamState
	for _, st := range p.ups {
		if st.downUntil.After(now) {
			down = append(down, st)
		}
	}
	return down
}

// Address позволяет использовать Picker всюду, где ждут одиночный Exchanger.
func (p *Picker) Address() string {
	parts := make([]string, 0, len(p.ups))
	for _, st := range p.ups {
		parts = append(parts, st.ex.Address())
	}
	return strings.Join(parts, ",")
}

func (p *Picker) Exchange(ctx context.Context, m *dns.Msg) (*dns.Msg, error) {
	var lastErr error
	for _, st := range p.ordered() {
		start := time.Now()
		resp, err := st.ex.Exchange(ctx, m)
		if err != nil {
			slog.Warn("upstream failed", "upstream", st.ex.Address(), "err", err)
			p.markDown(st)
			lastErr = err
			continue
		}
		p.markSuccess(st, time.Since(start))
		return resp, nil
	}
	return nil, fmt.Errorf("all upstreams failed, last: %w", lastErr)
}

// StartHealthCheck фоново пробует упавшие апстримы запросом ". NS" и
// возвращает ожившие в ротацию раньше их cooldown-а.
func (p *Picker) StartHealthCheck(ctx context.Context, interval time.Duration) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			for _, st := range p.downStates() {
				probe := new(dns.Msg)
				probe.SetQuestion(".", dns.TypeNS)
				cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
				_, err := st.ex.Exchange(cctx, probe)
				cancel()
				if err == nil {
					slog.Info("upstream recovered", "upstream", st.ex.Address())
					p.mu.Lock()
					st.downUntil = time.Time{}
					p.mu.Unlock()
				}
			}
		}
	}()
}
