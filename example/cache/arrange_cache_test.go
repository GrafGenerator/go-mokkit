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

// A producing verb comes in two shapes, and this suite writes both on purpose.
//
// The "A…" verbs hand the client straight back — AClient, ACachedClient. That
// is the default for a one-off artifact: no role to name, no sink to declare,
// just client := f.Arrange().ACachedClient(). The return ends the chain, which
// costs nothing when there is one thing to arrange.
//
// The token verbs file the client under a role instead — CacheHasClient[Kept].
// That is what a test with more than one client wants: the role is named once
// at the write site, read back anywhere with f.Of[Kept](), and the chain stays
// whole for the next verb.

// AClient produces a client without touching the cache — for a test that hands
// one to the subject rather than expecting it to be found.
func (a Arrange) AClient(opts ...ClientOpt) clients.Client {
	a.Helper()

	var client clients.Client

	a.Add("AClient", func(_ context.Context, h mokkit.Host) error {
		client = h.Resolve[*clientFaker]().newClient(opts...)

		return nil
	})

	return client
}

// ACachedClient produces a client and puts it in the cache, handing it back.
func (a Arrange) ACachedClient(opts ...ClientOpt) clients.Client {
	a.Helper()

	var client clients.Client

	a.Add("ACachedClient", func(_ context.Context, h mokkit.Host) error {
		var err error

		client, err = cacheClient(h, opts...)

		return err
	})

	return client
}

// CacheHasClient is ACachedClient filed under a role: the same arrangement,
// named by a token, with the chain left unbroken for the next verb.
func (a Arrange) CacheHasClient[K mokkit.Token[clients.Client]](opts ...ClientOpt) Arrange {
	a.Helper()
	a.Add("CacheHasClient["+mokkit.NameOf[K]()+"]", func(_ context.Context, h mokkit.Host) error {
		client, err := cacheClient(h, opts...)
		if err != nil {
			return err
		}
		*a.New[K]() = client

		return nil
	})

	return a
}

// cachedClient is CacheHasClient authored as a plain function — the shape a
// shared vocabulary package publishes, since it cannot hang methods on this
// suite's Arrange. It stays generic over the token so the role survives the
// trip through And.
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

// cacheClient builds a client and seeds the cache double with it, which is the
// work every shape of the verb shares.
func cacheClient(h mokkit.Host, opts ...ClientOpt) (clients.Client, error) {
	client := h.Resolve[*clientFaker]().newClient(opts...)

	data, err := json.Marshal(client)
	if err != nil {
		return clients.Client{}, fmt.Errorf("encoding the cached client: %w", err)
	}
	h.Resolve[*cacheProbe]().contents[cache.KeyFor(client.ID)] = string(data)

	return client, nil
}

// CacheHasNoClient leaves the cache empty: it answers, with nothing.
func (a Arrange) CacheHasNoClient() Arrange {
	a.Helper()
	a.Add("CacheHasNoClient", func(_ context.Context, h mokkit.Host) error {
		clear(h.Resolve[*cacheProbe]().contents)

		return nil
	})

	return a
}

// CacheReadFails makes the cache unavailable, which the subject must treat as a
// miss rather than an error.
func (a Arrange) CacheReadFails() Arrange {
	a.Helper()
	a.Add("CacheReadFails", func(_ context.Context, h mokkit.Host) error {
		h.Resolve[*cacheProbe]().getErr = errors.New("cache unavailable")

		return nil
	})

	return a
}

// CacheIsReachable states only that the cache is up. gomock treats an
// unexpected call as a failure, so an operation must be arranged even where the
// original needed no setup at all — NSubstitute would have returned a default.
func (a Arrange) CacheIsReachable() Arrange {
	a.Helper()
	a.Add("CacheIsReachable", func(_ context.Context, h mokkit.Host) error {
		// Resolving the probe is what wires the double's expectations, so
		// asking for it is the whole arrangement.
		h.Resolve[*cacheProbe]()

		return nil
	})

	return a
}
