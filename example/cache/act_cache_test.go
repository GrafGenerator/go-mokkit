package cache_test

import (
	"context"

	"github.com/GrafGenerator/go-mokkit"
	"github.com/GrafGenerator/go-mokkit/example/cache"
	"github.com/GrafGenerator/go-mokkit/example/clients"
)

// GetClient runs the read under test and hands back what it returned.
//
// The C# original reached into the stage for this (Stage.ExecuteAsync<TSvc,
// TOut>), because an Act that returns a value was the heaviest of its three
// flavors. Here an Act verb is an ordinary method that returns its artifact,
// so the operation stays in the vocabulary where the conventions want it.
func (a Act) GetClient(clientID string) *clients.Client {
	a.Helper()

	var out *clients.Client

	a.Add("GetClient", func(ctx context.Context, h mokkit.Host) error {
		out = h.Resolve[*cache.ClientCacheService]().Get(ctx, clientID)

		return nil
	})

	return out
}

// StoreClient runs the write under test. Its effects are observed in Inspect.
//
// It takes a value, not a pointer: a consuming verb is handed what a producing
// one already returned or filed, and a value is what both of those give back.
func (a Act) StoreClient(client clients.Client) Act {
	a.Helper()
	a.Add("StoreClient", func(ctx context.Context, h mokkit.Host) error {
		h.Resolve[*cache.ClientCacheService]().Set(ctx, client)

		return nil
	})

	return a
}

// RemoveClient runs the eviction under test.
func (a Act) RemoveClient(clientID string) Act {
	a.Helper()
	a.Add("RemoveClient", func(ctx context.Context, h mokkit.Host) error {
		h.Resolve[*cache.ClientCacheService]().Remove(ctx, clientID)

		return nil
	})

	return a
}
