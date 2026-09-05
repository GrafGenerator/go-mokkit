package mokkit

import (
	"context"
	"fmt"
	"reflect"
)

// Resolver hands back a service registered in a container, keyed by its type.
// Stage, Scope and a container's own factory resolver all satisfy it, so
// Resolve and TryResolve work against any of them.
type Resolver interface {
	// TryResolveType looks up a service by type. It reports false when nothing
	// is registered under t. Implementations must be safe for concurrent use.
	TryResolveType(t reflect.Type) (any, bool)
}

// PathResolver is the optional half of the contract: a resolver that can carry
// the chain of types currently under construction. A container detects a
// dependency cycle by checking that chain, and the chain has to survive the hop
// through the stage — otherwise a cycle spanning two containers is invisible to
// both and deadlocks on a lock one of them already holds.
//
// Implement it on a Scope whose factories can resolve their own collaborators.
// Stage implements it, and threads the path through every scope that does.
type PathResolver interface {
	Resolver

	// TryResolveTypePath resolves t with path naming the types already under
	// construction on this goroutine, outermost first.
	TryResolveTypePath(t reflect.Type, path []reflect.Type) (any, bool)
}

// Host is what a Step receives: the stage's resolver plus its context. It is a
// struct rather than an interface so that Resolve can be a generic method on it.
type Host struct {
	ctx context.Context
	r   Resolver
}

// NewHost pairs a resolver with the context steps run under.
func NewHost(ctx context.Context, r Resolver) Host { return Host{ctx: ctx, r: r} }

// Context reports the context this step is running under.
func (h Host) Context() context.Context { return h.ctx }

// Resolver reports the underlying resolver, for the places that still want the
// interface — passing it to a container, or to the free Resolve.
func (h Host) Resolver() Resolver { return h.r }

// TryResolveType satisfies Resolver, so a Host can be handed anywhere one is
// wanted.
func (h Host) TryResolveType(t reflect.Type) (any, bool) { return h.r.TryResolveType(t) }

// Resolve returns the service registered as T, panicking when nothing is.
// Inside a Step that is intentional: the executor recovers the panic and
// reports it against the step, so vocabulary resolves without error handling.
func (h Host) Resolve[T any]() T { return Resolve[T](h.r) }

// TryResolve returns the service registered as T, reporting false when nothing
// is registered under that type.
func (h Host) TryResolve[T any]() (T, bool) { return TryResolve[T](h.r) }

// Resolve returns the service registered as T.
//
// It panics when nothing is registered; inside a Step, the executor recovers
// the panic and reports it as a failure attributed to the step, so vocabulary
// calls Resolve without error handling. Inside a verb, prefer Host.Resolve.
func Resolve[T any](r Resolver) T {
	v, present, ok := lookup[T](r)
	if !ok {
		panic(&ResolveError{Type: reflect.TypeFor[T](), Present: present})
	}

	return v
}

// TryResolve returns the service registered as T, reporting false when nothing
// is registered under that type or the registration cannot be used as T.
func TryResolve[T any](r Resolver) (T, bool) {
	v, _, ok := lookup[T](r)

	return v, ok
}

// lookup reports both whether the resolution succeeded and whether anything was
// registered under the type at all, so a mis-registration is reported as one
// rather than as an absence.
func lookup[T any](r Resolver) (value T, present, ok bool) {
	var zero T

	v, present := r.TryResolveType(reflect.TypeFor[T]())
	if !present {
		return zero, false, false
	}

	typed, ok := v.(T)
	if !ok {
		return zero, true, false
	}

	return typed, true, true
}

// ResolveError reports a service that no container had registered, or one
// registered under a type it cannot be used as.
type ResolveError struct {
	Type reflect.Type

	// Present reports that something was registered under Type but could not be
	// used as it — a container handed back the wrong thing, which is a
	// different bug from having registered nothing.
	Present bool
}

func (e *ResolveError) Error() string {
	if e.Present {
		return fmt.Sprintf("mokkit: what is registered as %s cannot be used as it", e.Type)
	}

	return fmt.Sprintf("mokkit: no service registered as %s", e.Type)
}
