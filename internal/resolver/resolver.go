// Package resolver соединяет кеш и пул апстримов.
package resolver

import (
	"context"

	"github.com/miekg/dns"
	"github.com/necronicle/keenetic-doq/internal/cache"
	"github.com/necronicle/keenetic-doq/internal/upstream"
)

type Resolver struct {
	cache *cache.Cache
	up    upstream.Exchanger
}

func New(c *cache.Cache, up upstream.Exchanger) *Resolver {
	return &Resolver{cache: c, up: up}
}

func (r *Resolver) Resolve(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
	if len(req.Question) != 1 {
		return r.up.Exchange(ctx, req) // экзотику не кешируем
	}
	q := req.Question[0]
	if resp := r.cache.Get(q); resp != nil {
		resp.Id = req.Id
		return resp, nil
	}
	resp, err := r.up.Exchange(ctx, req)
	if err != nil {
		return nil, err
	}
	r.cache.Put(q, resp)
	return resp, nil
}
