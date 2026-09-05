package mokkit

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
)

// A Setup is a composition of containers, built once. Building is the expensive
// part; entering a stage from it is cheap and happens per test.
type Setup struct {
	containers []Container
	observers  []Observer
}

// NewSetup builds every container. Do this once per system-under-test, in
// whatever "run once" hook the test runner offers — TestMain, not init.
func NewSetup(ctx context.Context, builders ...ContainerBuilder) (*Setup, error) {
	containers := make([]Container, 0, len(builders))
	for i, b := range builders {
		c, err := b.Build(ctx)
		if err != nil {
			return nil, fmt.Errorf("mokkit: building container %d (%T): %w", i, b, err)
		}
		containers = append(containers, c)
	}

	return &Setup{containers: containers}, nil
}

var stageSeq atomic.Uint64

// EnterStage opens a fresh, isolated stage for one test and registers its
// cleanup with t. Scoped services are created per stage and released when the
// test ends, so nothing leaks between tests.
func (s *Setup) EnterStage(t TB) *Stage {
	t.Helper()

	return s.EnterStageContext(context.Background(), t)
}

// EnterStageContext is EnterStage with an explicit context, which steps in this
// stage's chains are run with.
func (s *Setup) EnterStageContext(ctx context.Context, t TB) *Stage {
	t.Helper()

	stage := &Stage{
		t:         t,
		ctx:       ctx,
		id:        fmt.Sprintf("%s#%d", t.Name(), stageSeq.Add(1)),
		tokens:    NewTokens(t),
		cache:     make(map[reflect.Type]any),
		observers: slices.Clone(s.observers),
	}
	stage.ex = &inlineExecutor{host: NewHost(ctx, stage)}

	sc := StageContext{T: t, StageID: stage.id, Resolver: stage}
	for i, c := range s.containers {
		scope, err := c.BeginScope(ctx, sc)
		if err != nil {
			// Close what did open before giving up.
			_ = stage.Close()
			t.Fatalf("mokkit: opening scope on container %d (%T): %v", i, c, err)

			// Only a TB whose Fatalf returns gets here; it receives nothing
			// rather than a half-built stage.
			return nil
		}
		stage.scopes = append(stage.scopes, scope)
	}

	t.Cleanup(func() {
		if err := stage.Close(); err != nil {
			t.Errorf("mokkit: closing stage: %v", err)
		}
	})

	for _, o := range stage.observers {
		o.StageEntered(t.Name(), stage.id)
	}

	return stage
}

// A Stage is the runtime one test runs against: the services it resolves, the
// tokens its artifacts live under, and the executor its steps run on.
type Stage struct {
	t         TB
	ctx       context.Context
	id        string
	ex        Executor
	tokens    *Tokens
	observers []Observer

	mu     sync.Mutex
	scopes []Scope
	cache  map[reflect.Type]any
	closed bool
}

// ID reports the stage's unique identifier.
func (s *Stage) ID() string { return s.id }

// Context reports the context this stage's steps run with.
func (s *Stage) Context() context.Context { return s.ctx }

// TB reports the test this stage belongs to, so a fixture accessor can mark
// itself a helper and keep failures pointing at the test's own line.
func (s *Stage) TB() TB { return s.t }

// Tokens reports the per-test artifact registry backing New and Of. Embed it
// in a fixture to have f.New[Buyer]() and f.Of[Buyer]() promoted onto it.
func (s *Stage) Tokens() *Tokens { return s.tokens }

// Host reports what a step receives, for the occasions that want one outside a
// chain.
func (s *Stage) Host() Host { return NewHost(s.ctx, s) }

// TryResolveType finds the first container that has typ registered, caching the
// result so every step and every branch of an All group sees one instance.
func (s *Stage) TryResolveType(typ reflect.Type) (any, bool) {
	return s.TryResolveTypePath(typ, nil)
}

// TryResolveTypePath is TryResolveType carrying the types already under
// construction, so a container can report a cycle that crosses into another
// container instead of deadlocking inside it.
//
// The cache lock is not held while a scope resolves, because a container's
// factory may resolve its own collaborators back through this method; holding
// it would deadlock. A scope that builds lazily is therefore responsible for
// handing back the same instance under concurrent resolution — see
// container/bag.
func (s *Stage) TryResolveTypePath(typ reflect.Type, path []reflect.Type) (any, bool) {
	if v, ok := s.cached(typ); ok {
		return v, true
	}

	for _, scope := range s.snapshotScopes() {
		v, ok := resolveThrough(scope, typ, path)
		if !ok {
			continue
		}

		return s.remember(typ, v), true
	}

	return nil, false
}

// resolveThrough prefers the path-carrying half of the contract, so cycle
// detection survives the hop between containers.
func resolveThrough(scope Scope, typ reflect.Type, path []reflect.Type) (any, bool) {
	if pr, ok := scope.(PathResolver); ok {
		return pr.TryResolveTypePath(typ, path)
	}

	return scope.TryResolveType(typ)
}

func (s *Stage) snapshotScopes() []Scope {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.scopes)
}

func (s *Stage) cached(typ reflect.Type) (any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	v, ok := s.cache[typ]

	return v, ok
}

// remember stores v unless another goroutine got there first, in which case the
// stored instance wins so every caller agrees on one.
func (s *Stage) remember(typ reflect.Type, v any) any {
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.cache[typ]; ok {
		return existing
	}
	s.cache[typ] = v

	return v
}

// Chain starts a phase that fails according to mode. Arrange, Act and Inspect
// are the named phases; this is for anything else.
func (s *Stage) Chain(phase string, mode FailMode) *Chain {
	return &Chain{
		Tokens: s.tokens,
		helper: s.t,
		tb:     s.t,
		ex:     s.ex,
		ctx:    s.ctx,
		phase:  phase,
		mode:   mode,

		test:      s.t.Name(),
		stageID:   s.id,
		observers: s.observers,
	}
}

// Arrange starts a setup chain. It fails fast: a broken setup makes every later
// step meaningless.
func (s *Stage) Arrange() *Chain { return s.Chain("arrange", FailFast) }

// Act starts a chain for the operation under test. It fails fast.
func (s *Stage) Act() *Chain { return s.Chain("act", FailFast) }

// Inspect starts an observation chain. It fails soft, so one run reports every
// failing observation rather than only the first.
func (s *Stage) Inspect() *Chain { return s.Chain("inspect", FailSoft) }

// Close releases every scope, newest first, and empties the resolution cache.
// EnterStage registers it with t.Cleanup, so tests do not normally call it.
// Closing twice is a no-op.
func (s *Stage) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()

		return nil
	}
	s.closed = true
	scopes := s.scopes
	s.scopes = nil
	clear(s.cache)
	s.mu.Unlock()

	// Close runs from t.Cleanup, after the test body, so the verdict includes
	// soft failures.
	for _, o := range s.observers {
		o.StageClosed(s.t.Name(), s.id, s.t.Failed())
	}

	var errs []error
	for i := len(scopes) - 1; i >= 0; i-- {
		if err := scopes[i].Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if s.ex != nil {
		if err := s.ex.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}
