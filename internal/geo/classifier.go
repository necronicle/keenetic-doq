package geo

import (
	"container/list"
	"sync"
	"time"
)

type Verdict int

const (
	Unknown Verdict = iota
	Geo
	Plain
)

const (
	// plainRecheck: «обычное» имя перепроверяется — сайт мог стать
	// геоблокированным. «Гео» живёт до рестарта.
	plainRecheck = 6 * time.Hour
	maxLearned   = 20000
)

type learned struct {
	name string
	v    Verdict
	at   time.Time
}

// Classifier решает, в какую полосу идёт имя: встроенный список и свои
// домены, затем выученные автоопределением имена.
type Classifier struct {
	static *Matcher
	max    int
	now    func() time.Time

	mu     sync.Mutex
	byName map[string]*list.Element
	lru    *list.List // front — самое свежее
}

func NewClassifier(static *Matcher) *Classifier {
	return &Classifier{static: static, max: maxLearned, now: time.Now,
		byName: map[string]*list.Element{}, lru: list.New()}
}

func (c *Classifier) Lookup(name string) Verdict {
	if c.static.Match(name) {
		return Geo
	}
	n := normalize(name)
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.byName[n]
	if !ok {
		return Unknown
	}
	e := el.Value.(*learned)
	if e.v == Plain && c.now().Sub(e.at) >= plainRecheck {
		c.lru.Remove(el)
		delete(c.byName, n)
		return Unknown
	}
	c.lru.MoveToFront(el)
	return e.v
}

func (c *Classifier) Learn(name string, v Verdict) {
	n := normalize(name)
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.byName[n]; ok {
		c.lru.Remove(el)
	}
	c.byName[n] = c.lru.PushFront(&learned{name: n, v: v, at: c.now()})
	for c.lru.Len() > c.max {
		last := c.lru.Back()
		c.lru.Remove(last)
		delete(c.byName, last.Value.(*learned).name)
	}
}

// GeoCount — сколько имён выучено как геоблокированные.
func (c *Classifier) GeoCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, el := range c.byName {
		if el.Value.(*learned).v == Geo {
			n++
		}
	}
	return n
}
