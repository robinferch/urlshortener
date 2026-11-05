package app

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
	"github.com/jackc/pgx/v5/pgxpool"
)

const cacheChannel = "urlshortener_cache"

type cachedDomain struct {
	id       int64
	fallback string
	found    bool
}

type cachedLink struct {
	found        bool
	id           int64
	target       string
	ios, android string
	passwordHash string
	validSince   *time.Time
	validUntil   *time.Time
	maxVisits    *int32
	exhausted    atomic.Bool // max visits reached; lets this replica refuse without a DB round trip
	forwardQuery bool
	enabled      bool
}

// cache holds hot redirect data, including negative entries so unknown slugs don't hit the DB.
// Writes invalidate locally and broadcast via Postgres NOTIFY so other replicas drop the key too;
// the TTL bounds staleness if a notification is ever missed.
type cache struct {
	domains *expirable.LRU[string, *cachedDomain]
	links   *expirable.LRU[string, *cachedLink]
	// gen bumps on every invalidation. A lookup only stores its DB result if gen is unchanged,
	// so a slow read that started before a write can't re-cache the old row after the write's invalidation.
	gen atomic.Uint64
	mu  sync.Mutex // makes "gen unchanged? then add" atomic with respect to drop
}

// store caches a lookup result unless an invalidation happened since gen was read.
func store[V any](c *cache, lru *expirable.LRU[string, V], gen uint64, key string, v V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen.Load() == gen {
		lru.Add(key, v)
	}
}

func newCache() *cache {
	return &cache{
		domains: expirable.NewLRU[string, *cachedDomain](10_000, nil, time.Minute),
		links:   expirable.NewLRU[string, *cachedLink](200_000, nil, time.Minute),
	}
}

func linkKey(domainID int64, slug string) string {
	return "l:" + itoa(domainID) + "/" + slug
}

func (c *cache) drop(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen.Add(1)
	switch {
	case key == "*":
		c.domains.Purge()
		c.links.Purge()
	case strings.HasPrefix(key, "d:"):
		c.domains.Remove(key[2:])
	case strings.HasPrefix(key, "l:"):
		c.links.Remove(key)
	}
}

// invalidate drops keys locally and tells other replicas to do the same.
func (c *cache) invalidate(ctx context.Context, db *pgxpool.Pool, keys ...string) {
	ctx = context.WithoutCancel(ctx) // the write is committed; a client hanging up must not skip the broadcast
	for _, k := range keys {
		c.drop(k)
		if _, err := db.Exec(ctx, "SELECT pg_notify($1, $2)", cacheChannel, k); err != nil {
			slog.Warn("cache notify failed", "err", err)
		}
	}
}

func (c *cache) listen(ctx context.Context, db *pgxpool.Pool) {
	for ctx.Err() == nil {
		err := c.listenOnce(ctx, db)
		if ctx.Err() != nil {
			return
		}
		slog.Warn("cache listener disconnected, retrying", "err", err)
		c.drop("*") // we may have missed notifications
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
	}
}

func (c *cache) listenOnce(ctx context.Context, db *pgxpool.Pool) error {
	conn, err := db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN "+cacheChannel); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), "UNLISTEN "+cacheChannel)
	for {
		n, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return err
		}
		c.drop(n.Payload)
	}
}
