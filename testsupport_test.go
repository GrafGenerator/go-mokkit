package mokkit

import (
	"context"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// fakeTB records what mokkit reported, so failure paths can be asserted on.
// Fatalf calls runtime.Goexit, exactly as *testing.T does, so tests observe the
// real "rest of the chain never runs" behavior rather than an approximation.
// Anything that may fatal must therefore run under runGoexit.
type fakeTB struct {
	name string

	mu       sync.Mutex
	errors   []string
	fatals   []string
	cleanups []func()
	failed   bool
}

func newFakeTB(name string) *fakeTB { return &fakeTB{name: name} }

func (f *fakeTB) Helper()      {}
func (f *fakeTB) Name() string { return f.name }

func (f *fakeTB) Cleanup(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleanups = append(f.cleanups, fn)
}

func (f *fakeTB) Errorf(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errors = append(f.errors, fmt.Sprintf(format, args...))
	f.failed = true
}

func (f *fakeTB) Fatalf(format string, args ...any) {
	f.mu.Lock()
	f.fatals = append(f.fatals, fmt.Sprintf(format, args...))
	f.failed = true
	f.mu.Unlock()
	runtime.Goexit()
}

func (f *fakeTB) FailNow() {
	f.mu.Lock()
	f.failed = true
	f.mu.Unlock()
	runtime.Goexit()
}

func (f *fakeTB) Failed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.failed
}

func (f *fakeTB) Errors() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.errors...)
}

func (f *fakeTB) Fatals() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.fatals...)
}

// RunCleanups runs registered cleanups in reverse order, as testing does.
func (f *fakeTB) RunCleanups() {
	f.mu.Lock()
	cleanups := append([]func(){}, f.cleanups...)
	f.cleanups = nil
	f.mu.Unlock()

	for i := len(cleanups) - 1; i >= 0; i-- {
		cleanups[i]()
	}
}

// runGoexit runs fn on its own goroutine so a fakeTB.Fatalf can Goexit without
// killing the test, and returns once fn has either finished or been unwound.
func runGoexit(fn func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	<-done
}

// assertContains fails unless every want appears somewhere in got.
func assertContains(t *testing.T, got []string, want ...string) {
	t.Helper()
	joined := strings.Join(got, "\n")
	for _, w := range want {
		if !strings.Contains(joined, w) {
			t.Errorf("expected a report containing %q, got:\n%s", w, joined)
		}
	}
}

// typeOf is the key a resolver speaks in: the type a service is registered and
// resolved under.
func typeOf[T any]() reflect.Type { return reflect.TypeFor[T]() }

// --- a container to compose stages from -------------------------------------

type fakeContainer struct {
	items    map[reflect.Type]any
	scopes   int
	closes   int
	scopeErr error
	mu       sync.Mutex
}

func newFakeContainer() *fakeContainer {
	return &fakeContainer{items: make(map[reflect.Type]any)}
}

// register adds a service under the type T resolves as.
func register[T any](c *fakeContainer, v T) {
	c.items[reflect.TypeFor[T]()] = v
}

// registerAs adds v under a type it need not be usable as, which is how a
// container mis-registration is staged: the lookup finds something, and what it
// finds is the wrong thing.
func registerAs(c *fakeContainer, typ reflect.Type, v any) {
	c.items[typ] = v
}

func (c *fakeContainer) Build(context.Context) (Container, error) { return c, nil }

func (c *fakeContainer) BeginScope(_ context.Context, _ StageContext) (Scope, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.scopeErr != nil {
		return nil, c.scopeErr
	}
	c.scopes++

	return &fakeScope{owner: c}, nil
}

type fakeScope struct {
	owner  *fakeContainer
	closed bool
}

func (s *fakeScope) TryResolveType(t reflect.Type) (any, bool) {
	v, ok := s.owner.items[t]

	return v, ok
}

func (s *fakeScope) Close() error {
	s.closed = true
	s.owner.mu.Lock()
	defer s.owner.mu.Unlock()
	s.owner.closes++

	return nil
}

// --- a container that speaks the path-carrying half of the contract ----------

// pathContainer answers for exactly one type and records the construction path
// the stage handed it. Given needs, it resolves that collaborator back through
// the stage before answering — the hop into another container that the path has
// to survive if a cycle spanning the two is to be reported rather than
// deadlocked on.
type pathContainer struct {
	provides reflect.Type
	value    any
	needs    reflect.Type

	mu       sync.Mutex
	resolver Resolver
	seen     [][]reflect.Type
}

func (c *pathContainer) Build(context.Context) (Container, error) { return c, nil }

func (c *pathContainer) BeginScope(_ context.Context, sc StageContext) (Scope, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resolver = sc.Resolver

	return &pathScope{owner: c}, nil
}

// seenPaths reports the paths this container was asked for its own type under.
func (c *pathContainer) seenPaths() [][]reflect.Type {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([][]reflect.Type(nil), c.seen...)
}

type pathScope struct{ owner *pathContainer }

func (s *pathScope) TryResolveType(t reflect.Type) (any, bool) {
	return s.TryResolveTypePath(t, nil)
}

func (s *pathScope) TryResolveTypePath(t reflect.Type, path []reflect.Type) (any, bool) {
	c := s.owner
	if t != c.provides {
		return nil, false
	}

	c.mu.Lock()
	c.seen = append(c.seen, append([]reflect.Type(nil), path...))
	resolver := c.resolver
	c.mu.Unlock()

	if c.needs != nil {
		if pr, ok := resolver.(PathResolver); ok {
			pr.TryResolveTypePath(c.needs, append(path, t))
		}
	}

	return c.value, true
}

func (s *pathScope) Close() error { return nil }

// --- a small domain to write vocabulary against ------------------------------

type Greeter interface{ Greet(name string) string }

type recordingGreeter struct {
	mu    sync.Mutex
	calls []string
}

func (g *recordingGreeter) Greet(name string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, name)

	return "hello " + name
}

func (g *recordingGreeter) Calls() []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	return append([]string(nil), g.calls...)
}

type User struct {
	ID     string
	Status string
}

// stageWith composes a stage over one fakeContainer holding the given greeter.
func stageWith(t *testing.T, tb TB, g Greeter) *Stage {
	t.Helper()
	c := newFakeContainer()
	if g != nil {
		register[Greeter](c, g)
	}
	setup, err := NewSetup(context.Background(), c)
	if err != nil {
		t.Fatalf("NewSetup: %v", err)
	}

	return setup.EnterStage(tb)
}
