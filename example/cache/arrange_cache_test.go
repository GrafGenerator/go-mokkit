package cache_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GrafGenerator/go-mokkit"
	"github.com/GrafGenerator/go-mokkit/example/cache"
	"github.com/GrafGenerator/go-mokkit/example/clients"
)

// AClient produces a client without touching the cache.
func (a Arrange) AClient(opts ...ClientOpt) clients.Client {
	a.Helper()

	return a.Get(func(h mokkit.Host) (clients.Client, error) {
		return h.Resolve[*clientFaker]().newClient(opts...), nil
	})
}

// ACachedClient produces a client, puts it in the cache and hands it back.
func (a Arrange) ACachedClient(opts ...ClientOpt) clients.Client {
	a.Helper()

	return a.Get(func(h mokkit.Host) (clients.Client, error) { return cacheClient(h, opts...) })
}

// CacheHasClient is ACachedClient filed under the role K, with the chain left
// unbroken for the next verb.
func (a Arrange) CacheHasClient[K mokkit.Token[clients.Client]](opts ...ClientOpt) Arrange {
	a.Helper()

	return mokkit.Do(a, func(h mokkit.Host) error {
		client, err := cacheClient(h, opts...)
		if err != nil {
			return err
		}
		*a.New[K]() = client

		return nil
	}, mokkit.NameOf[K]())
}

// cachedClient is CacheHasClient authored as a plain function, the shape a
// shared vocabulary package publishes.
func cachedClient[K mokkit.Token[clients.Client]](f *fixture, opts ...ClientOpt) mokkit.Step {
	name := "cache.cachedClient[" + mokkit.NameOf[K]() + "]"

	return mokkit.NewStep(name, func(_ context.Context, h mokkit.Host) error {
		client, err := cacheClient(h, opts...)
		if err != nil {
			return err
		}
		*f.New[K]() = client

		return nil
	})
}

// cacheClient builds a client and seeds the cache double with it.
func cacheClient(h mokkit.Host, opts ...ClientOpt) (clients.Client, error) {
	client := h.Resolve[*clientFaker]().newClient(opts...)

	data, err := json.Marshal(client)
	if err != nil {
		return clients.Client{}, fmt.Errorf("encoding the cached client: %w", err)
	}
	h.Resolve[*cacheProbe]().contents[cache.KeyFor(client.ID)] = string(data)

	return client, nil
}

// CacheHasNoClient leaves the cache empty.
func (a Arrange) CacheHasNoClient() Arrange {
	a.Helper()

	return mokkit.Do(a, func(h mokkit.Host) { clear(h.Resolve[*cacheProbe]().contents) })
}

// CacheReadFails makes the cache unavailable.
func (a Arrange) CacheReadFails() Arrange {
	a.Helper()

	return mokkit.Do(a, func(h mokkit.Host) {
		h.Resolve[*cacheProbe]().getErr = errors.New("cache unavailable")
	})
}

// CacheIsReachable states only that the cache is up. Resolving the probe wires
// the double's expectations, which is the whole arrangement.
func (a Arrange) CacheIsReachable() Arrange {
	a.Helper()

	return mokkit.Do(a, func(h mokkit.Host) { h.Resolve[*cacheProbe]() })
}
