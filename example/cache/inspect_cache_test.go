package cache_test

import (
	"encoding/json"
	"fmt"

	"github.com/GrafGenerator/go-mokkit"
	"github.com/GrafGenerator/go-mokkit/example/cache"
	"github.com/GrafGenerator/go-mokkit/example/clients"
)

// RetrievedClientMatching asserts the read returned the client that was cached.
func (i Inspect) RetrievedClientMatching(got *clients.Client, want clients.Client) Inspect {
	i.Helper()

	return mokkit.Do(i, func(mokkit.Host) error {
		if got == nil {
			return fmt.Errorf("want the cached client %s, got nothing", want.ID)
		}
		if *got != want {
			return fmt.Errorf("want %+v, got %+v", want, *got)
		}

		return nil
	})
}

// RetrievedNothing asserts the read came back empty.
func (i Inspect) RetrievedNothing(got *clients.Client) Inspect {
	i.Helper()

	return mokkit.Do(i, func(mokkit.Host) error {
		if got != nil {
			return fmt.Errorf("want nothing, got %+v", *got)
		}

		return nil
	})
}

// CacheQueried asserts the cache was read under the client's key, exactly once.
func (i Inspect) CacheQueried(clientID string) Inspect {
	i.Helper()

	return mokkit.Do(i, func(h mokkit.Host) error {
		key := cache.KeyFor(clientID)

		reads := h.Resolve[*cacheProbe]().gets
		if n := count(reads, key); n != 1 {
			return fmt.Errorf("want one read of %s, got %d (reads: %v)", key, n, reads)
		}

		return nil
	})
}

// CacheStored asserts the client was written under its key, serialized, with
// the expected expiry.
func (i Inspect) CacheStored(want clients.Client) Inspect {
	i.Helper()

	return mokkit.Do(i, func(h mokkit.Host) error {
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
}

// NothingStored asserts the cache was never written to.
func (i Inspect) NothingStored() Inspect {
	i.Helper()

	return mokkit.Do(i, func(h mokkit.Host) error {
		if writes := h.Resolve[*cacheProbe]().sets; len(writes) != 0 {
			return fmt.Errorf("want no writes, got %v", writes)
		}

		return nil
	})
}

// CacheRemoved asserts the client's key was evicted, exactly once.
func (i Inspect) CacheRemoved(clientID string) Inspect {
	i.Helper()

	return mokkit.Do(i, func(h mokkit.Host) error {
		key := cache.KeyFor(clientID)

		removals := h.Resolve[*cacheProbe]().removes
		if n := count(removals, key); n != 1 {
			return fmt.Errorf("want one removal of %s, got %d (removals: %v)", key, n, removals)
		}

		return nil
	})
}

// CacheStillHas asserts the client is untouched in the cache.
func (i Inspect) CacheStillHas(want clients.Client) Inspect {
	i.Helper()

	return mokkit.Do(i, func(h mokkit.Host) error {
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
}
