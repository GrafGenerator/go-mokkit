// Package cache holds the system under test: a client cache that degrades
// gracefully when the cache itself is unavailable.
package cache

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/GrafGenerator/go-mokkit/example/clients"
)

// Expiration is how long a cached client lives.
const Expiration = 30 * time.Minute

// KeyFor is the cache key a client is stored under.
func KeyFor(clientID string) string { return "client:" + clientID }

// ClientCacheService reads and writes clients through a distributed cache.
//
// Every method swallows cache failures, as the original does: a cache is an
// optimisation, and losing it must not fail the request. That is the behavior
// the "degrades" test pins down.
type ClientCacheService struct {
	cache  clients.DistributedCache
	logger *slog.Logger
}

func New(cache clients.DistributedCache, logger *slog.Logger) *ClientCacheService {
	return &ClientCacheService{cache: cache, logger: logger}
}

// Get returns the cached client, or nil for a miss — and also for a cache
// failure, which is reported to the log and otherwise treated as a miss.
func (s *ClientCacheService) Get(ctx context.Context, clientID string) *clients.Client {
	data, err := s.cache.Get(ctx, KeyFor(clientID))
	if err != nil {
		s.logger.ErrorContext(ctx, "reading client from cache", "clientId", clientID, "err", err)

		return nil
	}
	if data == "" {
		s.logger.DebugContext(ctx, "client not in cache", "clientId", clientID)

		return nil
	}

	var client clients.Client
	if err := json.Unmarshal([]byte(data), &client); err != nil {
		s.logger.ErrorContext(ctx, "decoding cached client", "clientId", clientID, "err", err)

		return nil
	}

	return &client
}

// Set stores the client. A cache failure is logged and swallowed.
func (s *ClientCacheService) Set(ctx context.Context, client clients.Client) {
	data, err := json.Marshal(client)
	if err != nil {
		s.logger.ErrorContext(ctx, "encoding client for cache", "clientId", client.ID, "err", err)

		return
	}
	if err := s.cache.Set(ctx, KeyFor(client.ID), string(data), Expiration); err != nil {
		s.logger.ErrorContext(ctx, "caching client", "clientId", client.ID, "err", err)
	}
}

// Remove drops the client from the cache. A cache failure is logged and
// swallowed.
func (s *ClientCacheService) Remove(ctx context.Context, clientID string) {
	if err := s.cache.Remove(ctx, KeyFor(clientID)); err != nil {
		s.logger.ErrorContext(ctx, "removing client from cache", "clientId", clientID, "err", err)
	}
}
