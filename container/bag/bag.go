// Package bag is mokkit's hand-wired container.
//
// It is the primary way to compose a stage, not a fallback: Go wires its
// dependencies by hand, so a factory that pulls its collaborators from the
// stage resolver is all that the C# original needed a DI container and a
// mock-to-DI bridge for.
//
//	b := bag.New()
//	bag.Instance[Clock](b, fixedClock)
//	bag.Scoped(b, func(r mokkit.Resolver) *DiscountService {
//	    return &DiscountService{
//	        Users: mokkit.Resolve[UserRepository](r),   // registered by another container
//	        Rates: mokkit.Resolve[RateRepository](r),
//	    }
//	})
//
//	setup, err := mokkit.NewSetup(ctx, mocks, b)
package bag

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/GrafGenerator/go-mokkit"
)

// A Builder collects registrations. Build it into a container by handing it to
// mokkit.NewSetup.
type Builder struct {
	mu    sync.Mutex
	regs  map[reflect.Type]registration
	built bool
}

type registration struct {
	// instance is set for a value shared by every stage.
	instance any
	// factory is set for a value built once per stage.
	factory func(mokkit.Resolver) any
	// alias is set for a type that resolves to whatever another type resolves
	// to. An alias owns nothing, so it never closes what it hands back.
	alias reflect.Type
}

// New returns an empty builder.
func New() *Builder {
	return &Builder{regs: make(map[reflect.Type]registration)}
}

// Instance registers v under the type T, shared by every stage entered from
// this composition. Register the same value under several types to expose it
// both concretely and behind an interface.
func Instance[T any](b *Builder, v T) *Builder {
	return b.add(reflect.TypeFor[T](), registration{instance: v})
}

// Scoped registers a factory run at most once per stage, the first time T is
// resolved. The resolver it receives spans every container in the composition,
// so a real service can be built over doubles another container registered.
//
// If the value implements io.Closer it is closed when the stage ends.
func Scoped[T any](b *Builder, factory func(r mokkit.Resolver) T) *Builder {
	return b.add(reflect.TypeFor[T](), registration{
		factory: func(r mokkit.Resolver) any { return factory(r) },
	})
}

// Alias registers T as resolving to whatever is registered for U, so one
// per-stage instance is reachable under both. This is the shape a test double
// wants: the vocabulary arranges and observes it through its concrete type,
// while the subject receives it through the interface.
//
//	bag.Scoped(b, func(mokkit.Resolver) *fakeUsers { return newFakeUsers() })
//	bag.Alias[UserRepository, *fakeUsers](b)
//
// U must be usable as T, which is checked here. An alias hands back the
// instance U owns and never closes it a second time.
func Alias[T, U any](b *Builder) *Builder {
	target, iface := reflect.TypeFor[U](), reflect.TypeFor[T]()

	if !target.AssignableTo(iface) {
		panic(fmt.Sprintf("bag: %s cannot be used as %s", target, iface))
	}

	return b.add(iface, registration{alias: target})
}

// Registered reports whether anything is registered under T.
func Registered[T any](b *Builder) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	_, ok := b.regs[reflect.TypeFor[T]()]

	return ok
}

func (b *Builder) add(typ reflect.Type, reg registration) *Builder {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.built {
		panic(fmt.Sprintf("bag: %s registered after the container was built", typ))
	}
	if _, dup := b.regs[typ]; dup {
		panic(fmt.Sprintf("bag: %s is already registered", typ))
	}
	b.regs[typ] = reg

	return b
}

// Build satisfies mokkit.ContainerBuilder. Registering after this panics.
func (b *Builder) Build(context.Context) (mokkit.Container, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.built = true

	regs := make(map[reflect.Type]registration, len(b.regs))
	for typ, reg := range b.regs {
		regs[typ] = reg
	}

	return &container{regs: regs}, nil
}

type container struct {
	regs map[reflect.Type]registration
}

func (c *container) BeginScope(_ context.Context, sc mokkit.StageContext) (mokkit.Scope, error) {
	return &scope{
		regs:    c.regs,
		stage:   sc.Resolver,
		entries: make(map[reflect.Type]*entry, len(c.regs)),
	}, nil
}

// entry holds one scoped instance and the lock that makes building it
// single-flight, so concurrent resolves — All's branches, say — agree on one.
type entry struct {
	once  sync.Mutex
	built bool
	value any
}

type scope struct {
	regs  map[reflect.Type]registration
	stage mokkit.Resolver

	mu      sync.Mutex
	entries map[reflect.Type]*entry
	closers []io.Closer
	closed  bool
}

func (s *scope) TryResolveType(typ reflect.Type) (any, bool) {
	return s.resolve(typ, nil)
}

// TryResolveTypePath satisfies mokkit.PathResolver, so the construction path
// survives the hop through the stage and a cycle that crosses into another
// container is reported rather than deadlocking on a lock this scope holds.
func (s *scope) TryResolveTypePath(typ reflect.Type, path []reflect.Type) (any, bool) {
	return s.resolve(typ, path)
}

// resolve builds typ if needed, carrying the chain of types currently under
// construction so a cycle is reported rather than overflowing the stack.
//
// Known limitation: detection is per construction path, so two goroutines
// entering a genuinely cyclic graph from opposite ends can still meet on each
// other's entry lock. A cyclic graph is a bug either way; this reports the
// common case legibly instead of hanging.
func (s *scope) resolve(typ reflect.Type, path []reflect.Type) (any, bool) {
	reg, ok := s.regs[typ]
	if !ok {
		return nil, false
	}

	if slices.Contains(path, typ) {
		panic(&CycleError{Path: append(slices.Clone(path), typ)})
	}

	switch {
	case reg.alias != reflect.Type(nil):
		// An alias owns nothing: it resolves the target through the same path,
		// so the target's own entry decides construction and closing.
		return s.resolveThrough(reg.alias, append(slices.Clone(path), typ))
	case reg.factory == nil:
		return reg.instance, true
	}

	e := s.entryFor(typ)

	// The cycle check above runs before this lock, so a factory re-entering for
	// a type it is itself building reports a cycle instead of deadlocking here.
	e.once.Lock()
	defer e.once.Unlock()

	if e.built {
		return e.value, true
	}

	// slices.Clone, because sibling resolves must not share a backing array:
	// appending to the same path from two branches would let one overwrite the
	// other's tail and misreport the cycle.
	v := reg.factory(&pathResolver{scope: s, path: append(slices.Clone(path), typ)})
	e.value, e.built = v, true

	if closer, ok := v.(io.Closer); ok {
		s.addCloser(closer)
	}

	return v, true
}

// resolveThrough finds typ in this scope first, then the rest of the
// composition, carrying path either way.
func (s *scope) resolveThrough(typ reflect.Type, path []reflect.Type) (any, bool) {
	if v, ok := s.resolve(typ, path); ok {
		return v, true
	}
	if s.stage == nil {
		return nil, false
	}
	if pr, ok := s.stage.(mokkit.PathResolver); ok {
		return pr.TryResolveTypePath(typ, path)
	}

	return s.stage.TryResolveType(typ)
}

func (s *scope) addCloser(c io.Closer) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.closers = append(s.closers, c)
}

func (s *scope) entryFor(typ reflect.Type) *entry {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		panic(fmt.Sprintf("bag: %s resolved after the stage closed", typ))
	}

	e, ok := s.entries[typ]
	if !ok {
		e = &entry{}
		s.entries[typ] = e
	}

	return e
}

// Close closes every scoped instance that implements io.Closer, newest first.
// Instances registered with Instance are shared across stages and so are the
// caller's to close, and an alias closes nothing, because it never owned what
// it handed back.
func (s *scope) Close() error {
	s.mu.Lock()
	closers := s.closers
	s.closers = nil
	s.closed = true
	s.mu.Unlock()

	var errs []error
	for i := len(closers) - 1; i >= 0; i-- {
		if err := closers[i].Close(); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// pathResolver is what a factory receives: this scope first, so the
// construction chain is tracked and cycles are caught, then the rest of the
// composition, which the path travels through as well.
type pathResolver struct {
	scope *scope
	path  []reflect.Type
}

func (p *pathResolver) TryResolveType(typ reflect.Type) (any, bool) {
	return p.scope.resolveThrough(typ, p.path)
}

func (p *pathResolver) TryResolveTypePath(typ reflect.Type, path []reflect.Type) (any, bool) {
	return p.scope.resolveThrough(typ, append(slices.Clone(p.path), path...))
}

// CycleError reports a factory that, directly or through its collaborators,
// depends on the type it is building.
type CycleError struct {
	Path []reflect.Type
}

func (e *CycleError) Error() string {
	names := make([]string, len(e.Path))
	for i, t := range e.Path {
		names[i] = t.String()
	}

	return "bag: dependency cycle: " + strings.Join(names, " -> ")
}
