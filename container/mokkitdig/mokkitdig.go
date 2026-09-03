// Package mokkitdig composes a mokkit stage over a uber-go/dig container, for
// suites whose subject is wired by runtime DI rather than by hand.
//
// The builder collects constructors; each stage gets its own dig container:
//
//	di := mokkitdig.New()
//	di.Provide(func(users UserRepository) *DiscountService {
//	    return &DiscountService{Users: users}
//	})
//	mokkitdig.Expose[*DiscountService](di)
//	mokkitdig.Bridge[UserRepository](di)
//
//	setup, err := mokkit.NewSetup(ctx, mocks, di)
//
// Bridge is the mock-to-DI seam: it teaches dig to take a dependency from the
// rest of the composition — a mock, a fake, a bag service — so the subject is
// assembled by dig over the doubles the test arranges. Expose is the opposite
// direction: it makes a dig-built service resolvable by verbs and fixtures.
//
// dig has no scoped lifecycle, so a stage closing releases the container and
// nothing else; a service that must be torn down belongs in a container that
// owns teardown, such as bag.
package mokkitdig

import (
	"context"
	"fmt"
	"reflect"
	"sync"

	"go.uber.org/dig"

	"github.com/GrafGenerator/go-mokkit"
)

// A Builder collects constructors. Build it into a container by handing it to
// mokkit.NewSetup.
type Builder struct {
	mu        sync.Mutex
	provides  []provide
	resolvers map[reflect.Type]func(*dig.Container) (any, error)
	built     bool
}

type provide struct {
	constructor any
	opts        []dig.ProvideOption
}

// New returns an empty builder.
func New() *Builder {
	return &Builder{resolvers: make(map[reflect.Type]func(*dig.Container) (any, error))}
}

// Provide registers a constructor with every stage's dig container, exactly as
// dig.Container.Provide would. What it produces stays internal to dig unless
// Expose makes it reachable.
func (b *Builder) Provide(constructor any, opts ...dig.ProvideOption) *Builder {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.ensureOpen()
	b.provides = append(b.provides, provide{constructor: constructor, opts: opts})

	return b
}

// Expose makes T resolvable by the rest of the composition — verbs, fixtures,
// and other containers. T must be buildable by the constructors provided;
// exposing it does not register anything with dig.
func Expose[T any](b *Builder) *Builder {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.ensureOpen()

	typ := reflect.TypeFor[T]()
	if _, dup := b.resolvers[typ]; dup {
		panic(fmt.Sprintf("mokkitdig: %s is already exposed", typ))
	}

	b.resolvers[typ] = func(c *dig.Container) (any, error) {
		var out T
		if err := c.Invoke(func(v T) { out = v }); err != nil {
			return nil, err
		}

		return out, nil
	}

	return b
}

// Bridge teaches dig to take T from the rest of the composition. It is the
// mock-to-DI seam: the constructor dig sees asks the stage, and the stage asks
// every container the setup holds.
func Bridge[T any](b *Builder) *Builder {
	return b.Provide(func(r mokkit.Resolver) T { return mokkit.Resolve[T](r) })
}

func (b *Builder) ensureOpen() {
	if b.built {
		panic("mokkitdig: provided after the container was built")
	}
}

// Build satisfies mokkit.ContainerBuilder. Providing after this panics.
func (b *Builder) Build(context.Context) (mokkit.Container, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.built = true

	resolvers := make(map[reflect.Type]func(*dig.Container) (any, error), len(b.resolvers))
	for typ, r := range b.resolvers {
		resolvers[typ] = r
	}

	return &container{
		provides:  append([]provide{}, b.provides...),
		resolvers: resolvers,
	}, nil
}

type container struct {
	provides  []provide
	resolvers map[reflect.Type]func(*dig.Container) (any, error)
}

func (c *container) BeginScope(_ context.Context, sc mokkit.StageContext) (mokkit.Scope, error) {
	dc := dig.New()

	// The stage itself is a dependency, which is what Bridge builds on. It must
	// only be used lazily, from a constructor at invoke time: sibling scopes
	// are still opening while this runs.
	if err := dc.Provide(func() mokkit.Resolver { return sc.Resolver }); err != nil {
		return nil, fmt.Errorf("mokkitdig: providing the stage resolver: %w", err)
	}

	for _, p := range c.provides {
		if err := dc.Provide(p.constructor, p.opts...); err != nil {
			return nil, fmt.Errorf("mokkitdig: %w", err)
		}
	}

	return &scope{container: dc, resolvers: c.resolvers}, nil
}

type scope struct {
	container *dig.Container
	resolvers map[reflect.Type]func(*dig.Container) (any, error)
}

func (s *scope) TryResolveType(typ reflect.Type) (any, bool) {
	resolve, ok := s.resolvers[typ]
	if !ok {
		return nil, false
	}

	v, err := resolve(s.container)
	if err != nil {
		// The type was exposed on purpose, so failing to build it is a broken
		// fixture, not an absence. Steps run under the executor, which reports
		// a panic against the step that resolved.
		panic(fmt.Sprintf("mokkitdig: building %s: %v", typ, err))
	}

	return v, true
}

// Close releases the container. dig owns no lifecycle, so there is nothing to
// run.
func (s *scope) Close() error { return nil }
