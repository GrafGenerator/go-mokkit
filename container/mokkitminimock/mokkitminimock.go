// Package mokkitminimock composes a mokkit stage from minimock doubles.
//
// A generated mock is registered under two types: the interface, so the subject
// receives it, and the mock's own type, so vocabulary can reach the per-method
// expectation API. Both resolve to one instance per stage.
//
//	mocks := mokkitminimock.New()
//	mokkitminimock.Add[UserRepository](mocks, shop.NewUserRepositoryMock)
//
//	app := bag.New()
//	bag.Scoped(app, func(r mokkit.Resolver) *DiscountService {
//	    return &DiscountService{Users: mokkit.Resolve[UserRepository](r)}
//	})
//
//	setup, err := mokkit.NewSetup(ctx, mocks, app)
//
// Each stage gets its own minimock.Controller, built from the stage's test, so
// expectations are asserted when that test finishes and nothing carries over
// between tests.
package mokkitminimock

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"github.com/gojuno/minimock/v3"

	"github.com/GrafGenerator/go-mokkit"
)

// tester adapts the stage's TB to minimock's Tester, which also wants the
// unformatted Fatal and Error.
type tester struct {
	mokkit.TB
}

func (t tester) Fatal(args ...any) {
	t.Helper()
	t.Fatalf("%s", fmt.Sprintln(args...))
}

func (t tester) Error(args ...any) {
	t.Helper()
	t.Errorf("%s", fmt.Sprintln(args...))
}

// A Builder collects mock registrations. Build it into a container by handing
// it to mokkit.NewSetup.
type Builder struct {
	mu    sync.Mutex
	regs  []registration
	seen  map[reflect.Type]bool
	built bool
}

type registration struct {
	iface reflect.Type
	mock  reflect.Type
	make  func(minimock.Tester) any
}

// New returns an empty builder.
func New() *Builder {
	return &Builder{seen: make(map[reflect.Type]bool)}
}

// Add registers the mock that ctor builds as standing in for the interface I.
// ctor is minimock's generated constructor, so the call reads:
//
//	mokkitminimock.Add[UserRepository](b, shop.NewUserRepositoryMock)
//
// I must be an interface and M must implement it; both are checked here rather
// than at resolve time, so a mismatch fails while the fixture is being written.
func Add[I, M any](b *Builder, ctor func(minimock.Tester) M) *Builder {
	iface, mockType := reflect.TypeFor[I](), reflect.TypeFor[M]()

	if iface.Kind() != reflect.Interface {
		panic(fmt.Sprintf("mokkitminimock: %s is not an interface; a mock stands in for an interface", iface))
	}
	if !mockType.Implements(iface) {
		panic(fmt.Sprintf("mokkitminimock: %s does not implement %s", mockType, iface))
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.built {
		panic(fmt.Sprintf("mokkitminimock: %s registered after the container was built", iface))
	}
	for _, typ := range [...]reflect.Type{iface, mockType} {
		if b.seen[typ] {
			panic(fmt.Sprintf("mokkitminimock: %s is already registered", typ))
		}
		b.seen[typ] = true
	}

	b.regs = append(b.regs, registration{
		iface: iface,
		mock:  mockType,
		make:  func(t minimock.Tester) any { return ctor(t) },
	})

	return b
}

// Registered reports whether I is already spoken for — either as an interface a
// mock stands in for, or as a generated mock's own type. Both are keys, so both
// answer true.
func Registered[I any](b *Builder) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.seen[reflect.TypeFor[I]()]
}

// Controller reports the stage's minimock.Controller, for a mock built on the
// spot.
func Controller(r mokkit.Resolver) *minimock.Controller {
	return mokkit.Resolve[*minimock.Controller](r)
}

// Build satisfies mokkit.ContainerBuilder. Registering after this panics.
func (b *Builder) Build(context.Context) (mokkit.Container, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.built = true

	return &container{regs: append([]registration(nil), b.regs...)}, nil
}

type container struct {
	regs []registration
}

func (c *container) BeginScope(_ context.Context, sc mokkit.StageContext) (mokkit.Scope, error) {
	if sc.T == nil {
		return nil, errors.New("mokkitminimock: the stage has no test to report expectations to")
	}

	// NewController registers its Finish with the test's cleanup. Stage.Close
	// is registered after BeginScope and cleanups run last-first, so scopes are
	// released before expectations are checked.
	mc := minimock.NewController(tester{TB: sc.T})

	items := make(map[reflect.Type]any, len(c.regs)*2+1)
	items[reflect.TypeFor[*minimock.Controller]()] = mc

	for _, reg := range c.regs {
		m := reg.make(mc)
		items[reg.mock] = m
		items[reg.iface] = m
	}

	return &scope{items: items}, nil
}

type scope struct {
	items map[reflect.Type]any
}

func (s *scope) TryResolveType(typ reflect.Type) (any, bool) {
	v, ok := s.items[typ]

	return v, ok
}

// Close does nothing: the controller asserts its expectations through the
// cleanup it registered on the stage's test.
func (s *scope) Close() error { return nil }
