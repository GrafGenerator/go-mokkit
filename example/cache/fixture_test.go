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
// The three are declared as one group because gofumpt rejects consecutive
// single type declarations.
type (
	Arrange struct{ *mokkit.Chain }
	Act     struct{ *mokkit.Chain }
	Inspect struct{ *mokkit.Chain }
)

// Chain's own chain-returning methods are promoted as returning *mokkit.Chain,
// so each vocabulary type re-declares the ones it wants fluent. WithContext is
// one of them: it mutates the chain and hands it back, exactly as And and All
// do, so its forwarder is written in the same call-for-effect shape.
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

// composition is built once for the whole package: the expensive part. Each
// test enters its own stage from it.
var composition *mokkit.Setup

func TestMain(m *testing.M) {
	mocks := mokkitgomock.New()
	mokkitgomock.Add[clients.DistributedCache](mocks, clients.NewMockDistributedCache)

	app := bag.New()
	bag.Instance(app, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// The faker is a suite helper, and suite helpers are just services. Scoped
	// rather than Instance, so every test gets one seeded identically and the
	// clients it builds are the same from run to run — the Go answer to the
	// original's static faker, without the static state.
	bag.Scoped(app, func(mokkit.Resolver) *clientFaker { return newClientFaker() })

	// The probe is the cache double's state and its record of what happened.
	// Building it wires the mock, once per stage; nothing carries between
	// tests. A factory takes the composition-wide Resolver, so this is the one
	// place that still spells the free mokkit.Resolve — a verb has a Host and
	// resolves with h.Resolve instead.
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

// fixture is what a test body talks to: the three phases, and the artifacts its
// verbs produce.
//
// Embedding *mokkit.Tokens is what puts f.Of[Kept]() on the fixture itself, so
// a test reads an artifact by the role that names it rather than through a
// variable threaded down from the arrange block.
type fixture struct {
	*mokkit.Tokens

	stage *mokkit.Stage
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	stage := composition.EnterStage(t)

	return &fixture{Tokens: stage.Tokens(), stage: stage}
}

func (f *fixture) Arrange() Arrange { return Arrange{f.stage.Arrange()} }
func (f *fixture) Act() Act         { return Act{f.stage.Act()} }
func (f *fixture) Inspect() Inspect { return Inspect{f.stage.Inspect()} }
