// Package mokkitgomock composes a mokkit stage from go.uber.org/mock doubles.
//
// A generated mock is registered under two types: the interface, so the subject
// receives it, and the mock's own type, so vocabulary can reach EXPECT() to
// arrange and to observe. Both resolve to one instance per stage.
//
//	mocks := mokkitgomock.New()
//	mokkitgomock.Add[UserRepository](mocks, NewMockUserRepository)
//
//	app := bag.New()
//	bag.Scoped(app, func(r mokkit.Resolver) *DiscountService {
//	    return &DiscountService{Users: mokkit.Resolve[UserRepository](r)}
//	})
//
//	setup, err := mokkit.NewSetup(ctx, mocks, app)
//
// Each stage gets its own gomock.Controller, built from the stage's test, so
// expectations are asserted when that test finishes and nothing carries over
// between tests.
package mokkitgomock

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"go.uber.org/mock/gomock"

	"github.com/GrafGenerator/go-mokkit"
)

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
	make  func(*gomock.Controller) any
}

// New returns an empty builder.
func New() *Builder {
	return &Builder{seen: make(map[reflect.Type]bool)}
}

// Add registers the mock that ctor builds as standing in for the interface I.
// ctor is mockgen's generated constructor, so the call reads:
//
//	mokkitgomock.Add[UserRepository](b, NewMockUserRepository)
//
// I must be an interface and M must implement it; both are checked here rather
// than at resolve time, so a mismatch fails while the fixture is being written.
func Add[I, M any](b *Builder, ctor func(*gomock.Controller) M) *Builder {
	iface, mock := reflect.TypeFor[I](), reflect.TypeFor[M]()

	if iface.Kind() != reflect.Interface {
		panic(fmt.Sprintf("mokkitgomock: %s is not an interface; a mock stands in for an interface", iface))
	}
	if !mock.Implements(iface) {
		panic(fmt.Sprintf("mokkitgomock: %s does not implement %s", mock, iface))
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.built {
		panic(fmt.Sprintf("mokkitgomock: %s registered after the container was built", iface))
	}
	for _, typ := range [...]reflect.Type{iface, mock} {
		if b.seen[typ] {
			panic(fmt.Sprintf("mokkitgomock: %s is already registered", typ))
		}
		b.seen[typ] = true
	}

	b.regs = append(b.regs, registration{
		iface: iface,
		mock:  mock,
		make:  func(ctrl *gomock.Controller) any { return ctor(ctrl) },
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

// Controller reports the stage's gomock.Controller, for the occasions that need
// it directly — gomock.InOrder across mocks, or a mock built on the spot.
func Controller(r mokkit.Resolver) *gomock.Controller {
	return mokkit.Resolve[*gomock.Controller](r)
}

// Satisfied is an Inspect step asserting that every expectation declared so far
// has been met by the time the chain reaches it.
//
// An expectation declared with Times(n) is otherwise only checked when the
// controller finishes at cleanup, reported against the EXPECT call's line.
// Satisfied in an Inspect chain fails at the test's own line; the controller's
// cleanup still names the missing call.
func Satisfied() mokkit.Step {
	return mokkit.NewStep("gomock.Satisfied", func(_ context.Context, h mokkit.Host) error {
		if !Controller(h).Satisfied() {
			return errors.New("mokkitgomock: expectations declared while arranging have not all been met")
		}

		return nil
	})
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
		return nil, errors.New("mokkitgomock: the stage has no test to report expectations to")
	}

	// mokkit.TB covers Errorf, Fatalf, Helper and Cleanup, so the controller
	// reports through the test and finishes when it does. Stage.Close is
	// registered after this and so runs first: scopes are released before
	// expectations are checked.
	ctrl := gomock.NewController(sc.T)

	items := make(map[reflect.Type]any, len(c.regs)*2+1)
	items[reflect.TypeFor[*gomock.Controller]()] = ctrl

	for _, reg := range c.regs {
		m := reg.make(ctrl)
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
