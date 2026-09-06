package cache_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/GrafGenerator/go-mokkit"
	"github.com/GrafGenerator/go-mokkit/container/bag"
	"github.com/GrafGenerator/go-mokkit/container/mokkitgomock"
	"github.com/GrafGenerator/go-mokkit/example/cache"
	"github.com/GrafGenerator/go-mokkit/example/clients"
)

// Arrange, Act and Inspect are this suite's vocabulary types. Verbs hang off
// them in arrange_cache_test.go, act_cache_test.go and inspect_cache_test.go.
type (
	Arrange struct{ *mokkit.Chain }
	Act     struct{ *mokkit.Chain }
	Inspect struct{ *mokkit.Chain }
)

// And, All and WithContext are promoted from *Chain returning *Chain, so each
// vocabulary type re-declares the ones it wants fluent.
func (a Arrange) And(steps ...mokkit.Step) Arrange {
	a.Helper()
	a.Chain.And(steps...)

	return a
}

func (i Inspect) And(steps ...mokkit.Step) Inspect {
	i.Helper()
	i.Chain.And(steps...)

	return i
}

func (i Inspect) All(steps ...mokkit.Step) Inspect {
	i.Helper()
	i.Chain.All(steps...)

	return i
}

func (a Act) WithContext(c context.Context) Act {
	a.Helper()
	a.Chain.WithContext(c)

	return a
}

// composition is built once for the package. Each test enters its own stage.
var composition *mokkit.Setup

func TestMain(m *testing.M) {
	mocks := mokkitgomock.New()
	mokkitgomock.Add[clients.DistributedCache](mocks, clients.NewMockDistributedCache)

	app := bag.New()
	bag.Instance(app, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// The faker is scoped, so every test gets one seeded identically.
	bag.Scoped(app, func(mokkit.Resolver) *clientFaker { return newClientFaker() })

	// The probe is the cache double's state and its record of what happened.
	// Building it wires the mock, once per stage.
	bag.Scoped(app, func(r mokkit.Resolver) *cacheProbe {
		return newCacheProbe(mokkit.Resolve[*clients.MockDistributedCache](r))
	})
	bag.Scoped(app, func(r mokkit.Resolver) *cache.ClientCacheService {
		return cache.New(
			mokkit.Resolve[clients.DistributedCache](r),
			mokkit.Resolve[*slog.Logger](r),
		)
	})

	setup, err := mokkit.NewSetup(context.Background(), mocks, app)
	if err != nil {
		panic("composing the cache suite: " + err.Error())
	}
	composition = setup

	m.Run()
}

type fixture = mokkit.Fixture[Arrange, Act, Inspect]

func newFixture(t *testing.T) *fixture {
	t.Helper()

	return composition.Enter[Arrange, Act, Inspect](t)
}
