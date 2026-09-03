package mokkit

import (
	"context"
)

// StageContext describes the stage a scope is being opened for.
type StageContext struct {
	// T is the test the stage belongs to. A container may use it to bind
	// per-test machinery — a gomock Controller, for instance, which asserts its
	// expectations on cleanup.
	T TB

	// StageID is unique per entered stage.
	StageID string

	// Resolver is the stage itself, spanning every container in the
	// composition. A container whose factories build real services uses it to
	// pull collaborators another container registered — which is all the
	// mock-to-DI bridge amounts to here.
	//
	// It must only be used lazily, from a factory at resolve time: while
	// BeginScope runs, the sibling scopes it would reach are still opening.
	Resolver Resolver
}

// A ContainerBuilder composes one container. Builders are run once, by
// NewSetup, before any stage is entered.
//
// The C# original ran builders through four phases so a DI builder could see a
// mock builder's registrations and bridge them. Hand-wiring removes the need:
// a factory takes the stage Resolver and pulls collaborators itself. If a
// runtime-DI adapter ever needs peer visibility, it arrives as an optional
// interface NewSetup type-asserts for, which is additive.
type ContainerBuilder interface {
	Build(ctx context.Context) (Container, error)
}

// A Container is a built, immutable composition. It hands out one Scope per
// entered stage.
type Container interface {
	BeginScope(ctx context.Context, sc StageContext) (Scope, error)
}

// A Scope holds the per-stage instances of a container's services. Scopes are
// resolved from concurrently, so implementations must be safe for concurrent
// use, and are closed when the stage is.
type Scope interface {
	Resolver
	Close() error
}
