package cache_test

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/GrafGenerator/go-mokkit"
	"github.com/GrafGenerator/go-mokkit/example/cache"
	"github.com/GrafGenerator/go-mokkit/example/clients"
)

// RetrievedClientMatching asserts the read returned the client that was cached.
//
// got is a pointer because nothing is what the subject returns on a miss; want
// is a value, because that is what a producing verb handed back.
func (i Inspect) RetrievedClientMatching(got *clients.Client, want clients.Client) Inspect {
	i.Helper()
	i.Add("RetrievedClientMatching", func(context.Context, mokkit.Host) error {
		if got == nil {
			return fmt.Errorf("want the cached client %s, got nothing", want.ID)
		}
		if *got != want {
			return fmt.Errorf("want %+v, got %+v", want, *got)
		}

		return nil
	})

	return i
}

// RetrievedNothing asserts the read came back empty — the shape a miss, a cache
// failure and a canceled request must all produce.
func (i Inspect) RetrievedNothing(got *clients.Client) Inspect {
	i.Helper()
	i.Add("RetrievedNothing", func(context.Context, mokkit.Host) error {
		if got != nil {
			return fmt.Errorf("want nothing, got %+v", *got)
		}

		return nil
	})

	return i
}

// CacheQueried asserts the cache was read under the client's key, exactly once.
func (i Inspect) CacheQueried(clientID string) Inspect {
	i.Helper()
	i.Add("CacheQueried", func(_ context.Context, h mokkit.Host) error {
		key := cache.KeyFor(clientID)

		reads := h.Resolve[*cacheProbe]().gets
		if n := count(reads, key); n != 1 {
			return fmt.Errorf("want one read of %s, got %d (reads: %v)", key, n, reads)
		}

		return nil
	})

	return i
}

// CacheStored asserts the client was written under its key, serialized, with
// the expected expiry.
func (i Inspect) CacheStored(want clients.Client) Inspect {
	i.Helper()
	i.Add("CacheStored", func(_ context.Context, h mokkit.Host) error {
		data, err := json.Marshal(want)
		if err != nil {
			return fmt.Errorf("encoding the expected client: %w", err)
		}

		key := cache.KeyFor(want.ID)

		writes := h.Resolve[*cacheProbe]().sets
		for _, w := range writes {
			if w.key == key && w.value == string(data) && w.ttl == cache.Expiration {
				return nil
			}
		}

		return fmt.Errorf("want %s stored for %v, got writes %v", key, cache.Expiration, writes)
	})

	return i
}

// NothingStored asserts the cache was never written to.
func (i Inspect) NothingStored() Inspect {
	i.Helper()
	i.Add("NothingStored", func(_ context.Context, h mokkit.Host) error {
		if writes := h.Resolve[*cacheProbe]().sets; len(writes) != 0 {
			return fmt.Errorf("want no writes, got %v", writes)
		}

		return nil
	})

	return i
}

// CacheRemoved asserts the client's key was evicted, exactly once.
func (i Inspect) CacheRemoved(clientID string) Inspect {
	i.Helper()
	i.Add("CacheRemoved", func(_ context.Context, h mokkit.Host) error {
		key := cache.KeyFor(clientID)

		removals := h.Resolve[*cacheProbe]().removes
		if n := count(removals, key); n != 1 {
			return fmt.Errorf("want one removal of %s, got %d (removals: %v)", key, n, removals)
		}

		return nil
	})

	return i
}

// CacheStillHas asserts the client is untouched in the cache. It is the other
// half of an eviction: what went, and what stayed.
func (i Inspect) CacheStillHas(want clients.Client) Inspect {
	i.Helper()
	i.Add("CacheStillHas", func(_ context.Context, h mokkit.Host) error {
		data, err := json.Marshal(want)
		if err != nil {
			return fmt.Errorf("encoding the expected client: %w", err)
		}

		key := cache.KeyFor(want.ID)

		got, ok := h.Resolve[*cacheProbe]().contents[key]
		if !ok {
			return fmt.Errorf("want %s still cached, got nothing", key)
		}
		if got != string(data) {
			return fmt.Errorf("want %s cached as %s, got %s", key, data, got)
		}

		return nil
	})

	return i
}
