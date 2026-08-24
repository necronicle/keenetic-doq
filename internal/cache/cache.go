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

type entry struct {
	key      string
	msg      *dns.Msg
	storedAt time.Time
	ttl      time.Duration
}

type Cache struct {
	mu     sync.Mutex
	max    int
	minTTL time.Duration
	maxTTL time.Duration
	byKey  map[string]*list.Element
	lru    *list.List // front = самый свежий
	now    func() time.Time
}

func New(maxEntries int, minTTL, maxTTL time.Duration) *Cache {
	return &Cache{
		max:    maxEntries,
		minTTL: minTTL,
		maxTTL: maxTTL,
		byKey:  make(map[string]*list.Element),
		lru:    list.New(),
		now:    time.Now,
	}
}

func key(q dns.Question) string {
	return strings.ToLower(q.Name) + "|" + strconv.Itoa(int(q.Qtype)) + "|" + strconv.Itoa(int(q.Qclass))
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

func (c *Cache) Put(q dns.Question, resp *dns.Msg) {
	if resp.Rcode != dns.RcodeSuccess && resp.Rcode != dns.RcodeNameError {
		return
	}
	k := key(q)
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

func (c *Cache) Get(q dns.Question) *dns.Msg {
	k := key(q)
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.byKey[k]
	if !ok {
		return nil
	}
	e := el.Value.(*entry)
	elapsed := c.now().Sub(e.storedAt)
	if elapsed >= e.ttl {
		c.lru.Remove(el)
		delete(c.byKey, k)
		return nil
	}
	c.lru.MoveToFront(el)
	out := e.msg.Copy()
	dec := uint32(elapsed / time.Second)
	for _, sec := range [][]dns.RR{out.Answer, out.Ns, out.Extra} {
		for _, rr := range sec {
			if rr.Header().Rrtype == dns.TypeOPT {
				continue
			}
			if rr.Header().Ttl > dec {
				rr.Header().Ttl -= dec
			} else {
				rr.Header().Ttl = 1
			}
		}
	}
	return out
}
