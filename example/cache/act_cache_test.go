package cache_test

import (
	"github.com/GrafGenerator/go-mokkit"
	"github.com/GrafGenerator/go-mokkit/example/cache"
	"github.com/GrafGenerator/go-mokkit/example/clients"
)

// GetClient runs the read under test and hands back what it returned.
func (a Act) GetClient(clientID string) *clients.Client {
	a.Helper()

	return a.Get(func(h mokkit.Host) (*clients.Client, error) {
		return h.Resolve[*cache.ClientCacheService]().Get(h.Context(), clientID), nil
	})
}

// StoreClient runs the write under test. Its effects are observed in Inspect.
func (a Act) StoreClient(client clients.Client) Act {
	a.Helper()

	return mokkit.Do(a, func(h mokkit.Host) {
		h.Resolve[*cache.ClientCacheService]().Set(h.Context(), client)
	})
}

// RemoveClient runs the eviction under test.
func (a Act) RemoveClient(clientID string) Act {
	a.Helper()

	return mokkit.Do(a, func(h mokkit.Host) {
		h.Resolve[*cache.ClientCacheService]().Remove(h.Context(), clientID)
	})
}
