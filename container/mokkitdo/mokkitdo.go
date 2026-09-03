// Package mokkitdo composes a mokkit stage over a samber/do injector, for
// suites whose subject is wired by runtime DI rather than by hand.
//
// The builder collects providers; each stage gets its own injector, built lazily
// and shut down — do's own shutdown hooks included — when the stage closes:
//
//	di := mokkitdo.New()
//	mokkitdo.Provide(di, func(inj do.Injector) (*DiscountService, error) {
//	    return &DiscountService{
//	        Users: mokkitdo.FromStage[UserRepository](inj),
//	    }, nil
//	})
//
//	setup, err := mokkit.NewSetup(ctx, mocks, di)
//
// FromStage is the bridge: a provider can pull a collaborator that another
// container in the composition registered — a mock, a fake, a bag service — so
// the subject is assembled by do over the doubles the test arranges.
package mokkitdo

import (
	"context"
	"fmt"
	"reflect"
	"sync"

	"github.com/samber/do/v2"

	"github.com/GrafGenerator/go-mokkit"
)

// A Builder collects providers. Build it into a container by handing it to
// mokkit.NewSetup.
type Builder struct {
	mu        sync.Mutex
	installs  []func(do.Injector)
	resolvers map[reflect.Type]func(do.Injector) (any, error)
	built     bool
}

// New returns an empty builder.
func New() *Builder {
	return &Builder{resolvers: make(map[reflect.Type]func(do.Injector) (any, error))}
}

// Provide registers a lazy provider for T, built at most once per stage, and
// exposes T to the rest of the composition.
func Provide[T any](b *Builder, provider func(do.Injector) (T, error)) *Builder {
	return install[T](b, func(inj do.Injector) { do.Provide(inj, provider) })
}

// Supply registers a ready value for T and exposes it to the composition.
func Supply[T any](b *Builder, value T) *Builder {
	return install[T](b, func(inj do.Injector) { do.ProvideValue(inj, value) })
}

// Install runs arbitrary do registrations — a package, in do's vocabulary —
// against every stage's injector. Services registered this way stay internal to
// do unless a Provide or Supply also exposes their type.
func (b *Builder) Install(pkg func(do.Injector)) *Builder {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.ensureOpen()
	b.installs = append(b.installs, pkg)

	return b
}

func install[T any](b *Builder, register func(do.Injector)) *Builder {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.ensureOpen()

	typ := reflect.TypeFor[T]()
	if _, dup := b.resolvers[typ]; dup {
		panic(fmt.Sprintf("mokkitdo: %s is already provided", typ))
	}

	b.installs = append(b.installs, register)
	b.resolvers[typ] = func(inj do.Injector) (any, error) { return do.Invoke[T](inj) }

	return b
}

func (b *Builder) ensureOpen() {
	if b.built {
		panic("mokkitdo: provided after the container was built")
	}
}

// FromStage resolves a service another container in the composition registered,
// from inside a do provider. It is the mock-to-DI bridge: do builds the subject,
// and the doubles come from wherever the test arranges them.
func FromStage[T any](inj do.Injector) T {
	return mokkit.Resolve[T](do.MustInvoke[mokkit.Resolver](inj))
}

// Build satisfies mokkit.ContainerBuilder. Providing after this panics.
func (b *Builder) Build(context.Context) (mokkit.Container, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.built = true

	resolvers := make(map[reflect.Type]func(do.Injector) (any, error), len(b.resolvers))
	for typ, r := range b.resolvers {
		resolvers[typ] = r
	}

	return &container{
		installs:  append([]func(do.Injector){}, b.installs...),
		resolvers: resolvers,
	}, nil
}

type container struct {
	installs  []func(do.Injector)
	resolvers map[reflect.Type]func(do.Injector) (any, error)
}

func (c *container) BeginScope(_ context.Context, sc mokkit.StageContext) (mokkit.Scope, error) {
	inj := do.New()

	// The stage itself is a service, which is what lets FromStage reach the
	// rest of the composition. It must only be used lazily, at provide time:
	// sibling scopes are still opening while this runs.
	do.ProvideValue[mokkit.Resolver](inj, sc.Resolver)

	for _, register := range c.installs {
		register(inj)
	}

	return &scope{injector: inj, resolvers: c.resolvers}, nil
}

type scope struct {
	injector  *do.RootScope
	resolvers map[reflect.Type]func(do.Injector) (any, error)
}

func (s *scope) TryResolveType(typ reflect.Type) (any, bool) {
	resolve, ok := s.resolvers[typ]
	if !ok {
		return nil, false
	}

	v, err := resolve(s.injector)
	if err != nil {
		// A provider that fails is a broken fixture, not an absence. Steps run
		// under the executor, which reports a panic against the step that
		// resolved.
		panic(fmt.Sprintf("mokkitdo: building %s: %v", typ, err))
	}

	return v, true
}

// Close shuts the injector down, running do's shutdown hooks newest-first.
func (s *scope) Close() error {
	report := s.injector.Shutdown()
	if report != nil && !report.Succeed {
		return fmt.Errorf("mokkitdo: shutting down: %s", report.Error())
	}

	return nil
}
