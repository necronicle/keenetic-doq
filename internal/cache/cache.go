// Package cache — TTL+LRU кеш DNS-ответов для doqd.
package cache

import (
	"container/list"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

const emptyAnswerTTL = 60 * time.Second // ответ без RR (напр. NXDOMAIN без SOA)

// Устаревшие ответы (RFC 8767): просроченная запись хранится ещё maxStale и
// отдаётся с TTL staleTTL, только когда свежего ответа получить не удалось.
const (
	maxStale = 24 * time.Hour
	staleTTL = 30 * time.Second
)

// Key — ключ записи: вопрос плюс биты DO и CD. Ответ на запрос с DO несёт
// подписи DNSSEC, с CD — непроверенные данные; смешивать их с обычными нельзя.
type Key string

// KeyOf строит ключ по запросу (берётся первый вопрос).
func KeyOf(req *dns.Msg) Key {
	do := false
	if opt := req.IsEdns0(); opt != nil {
		do = opt.Do()
	}
	return makeKey(req.Question[0], do, req.CheckingDisabled)
}

// QuestionKey — ключ для вопроса без DO и CD.
func QuestionKey(q dns.Question) Key { return makeKey(q, false, false) }

func makeKey(q dns.Question, do, cd bool) Key {
	flags := "-"
	if do {
		flags += "do"
	}
	if cd {
		flags += "cd"
	}
	return Key(strings.ToLower(q.Name) + "|" + strconv.Itoa(int(q.Qtype)) + "|" +
		strconv.Itoa(int(q.Qclass)) + "|" + flags)
}

type entry struct {
	key      Key
	msg      *dns.Msg
	storedAt time.Time
	ttl      time.Duration
}

type Cache struct {
	mu     sync.Mutex
	max    int
	minTTL time.Duration
	maxTTL time.Duration
	byKey  map[Key]*list.Element
	lru    *list.List // front = самый свежий
	now    func() time.Time
}

func New(maxEntries int, minTTL, maxTTL time.Duration) *Cache {
	return &Cache{
		max:    maxEntries,
		minTTL: minTTL,
		maxTTL: maxTTL,
		byKey:  make(map[Key]*list.Element),
		lru:    list.New(),
		now:    time.Now,
	}
}

// respTTL — минимальный TTL по всем RR (кроме OPT), clamped в [minTTL, maxTTL].
func (c *Cache) respTTL(m *dns.Msg) time.Duration {
	minSec := uint32(0)
	found := false
	for _, sec := range [][]dns.RR{m.Answer, m.Ns, m.Extra} {
		for _, rr := range sec {
			if rr.Header().Rrtype == dns.TypeOPT {
				continue
			}
			if !found || rr.Header().Ttl < minSec {
				minSec = rr.Header().Ttl
				found = true
			}
		}
	}
	ttl := emptyAnswerTTL
	if found {
		ttl = time.Duration(minSec) * time.Second
	}
	if ttl < c.minTTL {
		ttl = c.minTTL
	}
	if ttl > c.maxTTL {
		ttl = c.maxTTL
	}
	return ttl
}

func (c *Cache) Put(k Key, resp *dns.Msg) {
	if resp.Rcode != dns.RcodeSuccess && resp.Rcode != dns.RcodeNameError {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.byKey[k]; ok {
		c.lru.Remove(el)
		delete(c.byKey, k)
	}
	e := &entry{key: k, msg: resp.Copy(), storedAt: c.now(), ttl: c.respTTL(resp)}
	c.byKey[k] = c.lru.PushFront(e)
	for c.lru.Len() > c.max {
		last := c.lru.Back()
		c.lru.Remove(last)
		delete(c.byKey, last.Value.(*entry).key)
	}
}

// Get отдаёт свежую запись со счётчиками TTL, уменьшенными на прошедшее время.
func (c *Cache) Get(k Key) *dns.Msg {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, elapsed := c.lookup(k)
	if e == nil || elapsed >= e.ttl {
		return nil
	}
	return withTTL(e.msg, func(ttl uint32) uint32 {
		dec := uint32(elapsed / time.Second)
		if ttl > dec {
			return ttl - dec
		}
		return 1
	})
}

// GetStale отдаёт запись, даже просроченную (не старше maxStale), с TTL
// staleTTL — для случая, когда апстримы недоступны.
func (c *Cache) GetStale(k Key) *dns.Msg {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, _ := c.lookup(k)
	if e == nil {
		return nil
	}
	sec := uint32(staleTTL / time.Second)
	return withTTL(e.msg, func(uint32) uint32 { return sec })
}

// lookup находит запись и выбрасывает её, если она старше ttl+maxStale.
// Вызывается под c.mu.
func (c *Cache) lookup(k Key) (*entry, time.Duration) {
	el, ok := c.byKey[k]
	if !ok {
		return nil, 0
	}
	e := el.Value.(*entry)
	elapsed := c.now().Sub(e.storedAt)
	if elapsed >= e.ttl+maxStale {
		c.lru.Remove(el)
		delete(c.byKey, k)
		return nil, 0
	}
	c.lru.MoveToFront(el)
	return e, elapsed
}

func withTTL(m *dns.Msg, ttl func(uint32) uint32) *dns.Msg {
	out := m.Copy()
	for _, sec := range [][]dns.RR{out.Answer, out.Ns, out.Extra} {
		for _, rr := range sec {
			if rr.Header().Rrtype == dns.TypeOPT {
				continue
			}
			rr.Header().Ttl = ttl(rr.Header().Ttl)
		}
	}
	return out
}
