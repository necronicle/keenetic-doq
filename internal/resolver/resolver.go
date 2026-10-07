// Package resolver соединяет кеш и пул апстримов.
package resolver

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
	"github.com/necronicle/keenetic-doq/internal/cache"
	"github.com/necronicle/keenetic-doq/internal/upstream"
)

// sharedTimeout — сколько живёт запрос к апстримам. Он общий для всех
// одинаковых запросов и не зависит от дедлайна того, кто пришёл первым.
const sharedTimeout = 10 * time.Second

// ednsUDPSize — размер UDP-буфера, который doqd объявляет клиентам
// (рекомендация DNS Flag Day 2020).
const ednsUDPSize = 1232

// staleAnswerDelay — RFC 8767 «client response timer»: если свежий ответ не
// пришёл за это время, а в кеше есть устаревший, клиент получает устаревший,
// а обновление продолжается в фоне. Переменная — для тестов.
var staleAnswerDelay = 1800 * time.Millisecond

// failureRecheck — RFC 8767 «failure recheck timer»: столько после общего
// сбоя апстримов устаревший ответ отдаётся сразу, без ожидания.
const failureRecheck = 30 * time.Second

type Resolver struct {
	cache *cache.Cache
	up    upstream.Exchanger

	mu       sync.Mutex
	inflight map[cache.Key]*call
	// lastFailure — когда запрос к апстримам последний раз кончился ошибкой.
	lastFailure atomic.Int64
}

// call — запрос к апстриму, который ждут все одинаковые запросы.
type call struct {
	done chan struct{}
	resp *dns.Msg
	err  error
}

func New(c *cache.Cache, up upstream.Exchanger) *Resolver {
	return &Resolver{cache: c, up: up, inflight: map[cache.Key]*call{}}
}

func (r *Resolver) Resolve(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
	if len(req.Question) != 1 {
		return r.up.Exchange(ctx, req) // экзотику не кешируем
	}
	k := cache.KeyOf(req)
	if resp := r.cache.Get(k); resp != nil {
		return answer(req, resp), nil
	}
	stale := r.cache.GetStale(k)
	c := r.shared(k, req)
	if stale != nil && time.Since(time.Unix(0, r.lastFailure.Load())) < failureRecheck {
		// Апстримы только что отказали — не заставлять клиента ждать снова;
		// обновление идёт в фоне.
		return answer(req, stale), nil
	}
	var staleTimer <-chan time.Time
	if stale != nil {
		t := time.NewTimer(staleAnswerDelay)
		defer t.Stop()
		staleTimer = t.C
	}
	select {
	case <-c.done:
		// RFC 8767: апстримы недоступны или SERVFAIL — лучше устаревший ответ.
		if stale != nil && (c.err != nil || c.resp.Rcode == dns.RcodeServerFailure) {
			slog.Debug("serving stale answer", "q", req.Question[0].Name, "err", c.err)
			return answer(req, stale), nil
		}
		if c.err != nil {
			return nil, c.err
		}
		return answer(req, c.resp.Copy()), nil
	case <-staleTimer:
		slog.Debug("upstream is slow, serving stale answer", "q", req.Question[0].Name)
		return answer(req, stale), nil
	case <-ctx.Done():
		if stale != nil {
			return answer(req, stale), nil
		}
		return nil, ctx.Err()
	}
}

// shared запускает запрос к апстриму или присоединяется к уже идущему:
// одинаковые одновременные запросы уходят наружу один раз.
func (r *Resolver) shared(k cache.Key, req *dns.Msg) *call {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.inflight[k]; ok {
		return c
	}
	c := &call{done: make(chan struct{})}
	r.inflight[k] = c
	q := req.Copy()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), sharedTimeout)
		defer cancel()
		c.resp, c.err = r.up.Exchange(ctx, q)
		if c.err == nil {
			r.cache.Put(k, c.resp)
		} else {
			r.lastFailure.Store(time.Now().UnixNano())
		}
		r.mu.Lock()
		delete(r.inflight, k)
		r.mu.Unlock()
		close(c.done)
	}()
	return c
}

// answer подгоняет ответ под конкретный запрос: его ID, его же секция
// вопроса, вплоть до регистра (так ответ проверяют клиенты с 0x20), и свой OPT.
// OPT собирается заново, ровно когда он был в запросе (RFC 6891): в кеше мог
// остаться ответ на запрос с EDNS или без, а опции апстрима — COOKIE, padding —
// чужому клиенту не нужны и вредны. Переносится только расширенная ошибка
// (EDE): она объясняет SERVFAIL.
func answer(req, resp *dns.Msg) *dns.Msg {
	resp.Id = req.Id
	resp.Question = append([]dns.Question(nil), req.Question...)
	var ede []dns.EDNS0
	extra := resp.Extra[:0]
	for _, rr := range resp.Extra {
		if opt, ok := rr.(*dns.OPT); ok {
			for _, o := range opt.Option {
				if e, ok := o.(*dns.EDNS0_EDE); ok {
					ede = append(ede, e)
				}
			}
			continue
		}
		extra = append(extra, rr)
	}
	resp.Extra = extra
	reqOpt := req.IsEdns0()
	if reqOpt == nil {
		// Расширенный RCODE (BADVERS, BADCOOKIE) живёт в OPT: без него ответ
		// не собрать, и клиент не получил бы ничего.
		if resp.Rcode > 0xF {
			resp.Rcode = dns.RcodeServerFailure
		}
		return resp
	}
	resp.SetEdns0(ednsUDPSize, reqOpt.Do())
	resp.IsEdns0().Option = ede
	return resp
}
