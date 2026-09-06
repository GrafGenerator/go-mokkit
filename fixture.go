package mokkit

import "context"

// A Phase is a vocabulary type declared as a struct whose only field is an
// embedded *Chain:
//
//	type (
//	    Arrange struct{ *mokkit.Chain }
//	    Act     struct{ *mokkit.Chain }
//	    Inspect struct{ *mokkit.Chain }
//	)
type Phase interface{ ~struct{ *Chain } }

// A Fixture is what a test body talks to: the three phases, typed as the
// suite's own vocabulary, and the artifacts its verbs produce. Embedding
// *Tokens puts f.New, f.Of and f.Ref on the fixture itself.
//
//	type fixture = mokkit.Fixture[Arrange, Act, Inspect]
//
//	func newFixture(t *testing.T) *fixture {
//	    t.Helper()
//
//	    b := bag.New()
//	    bag.Fresh[fakeUsers](b)
//	    bag.Scoped(b, func(r mokkit.Resolver) *Service { ... })
//
//	    return mokkit.Enter[Arrange, Act, Inspect](t, b)
//	}
type Fixture[A, C, I Phase] struct {
	*Tokens

	// Stage is the runtime the fixture's chains run against.
	Stage *Stage
}

// Enter builds the containers, opens a stage for t and returns the fixture over
// it. It is for a composition built per test; a composition shared by a package
// is built once with NewSetup and entered with Setup.Enter. A container that
// cannot be built fails t.
func Enter[A, C, I Phase](t TB, builders ...ContainerBuilder) *Fixture[A, C, I] {
	t.Helper()

	return EnterContext[A, C, I](context.Background(), t, builders...)
}

// EnterContext is Enter with an explicit context, which the containers are
// built with and the stage's steps run with.
func EnterContext[A, C, I Phase](ctx context.Context, t TB, builders ...ContainerBuilder) *Fixture[A, C, I] {
	t.Helper()

	setup, err := NewSetup(ctx, builders...)
	if err != nil {
		t.Fatalf("%v", err)

		return nil
	}

	return setup.EnterContext[A, C, I](ctx, t)
}

// Enter opens a stage for t and returns the fixture over it.
func (s *Setup) Enter[A, C, I Phase](t TB) *Fixture[A, C, I] {
	t.Helper()

	return s.EnterContext[A, C, I](context.Background(), t)
}

// EnterContext is Enter with an explicit context, which the stage's steps run
// with.
func (s *Setup) EnterContext[A, C, I Phase](ctx context.Context, t TB) *Fixture[A, C, I] {
	t.Helper()

	stage := s.EnterStageContext(ctx, t)
	if stage == nil {
		return nil
	}

	return &Fixture[A, C, I]{Tokens: stage.Tokens(), Stage: stage}
}

// Arrange starts a setup chain, typed as the suite's Arrange.
func (f *Fixture[A, C, I]) Arrange() A { return A(struct{ *Chain }{f.Stage.Arrange()}) }

// Act starts a chain for the operation under test, typed as the suite's Act.
func (f *Fixture[A, C, I]) Act() C { return C(struct{ *Chain }{f.Stage.Act()}) }

// Inspect starts an observation chain, typed as the suite's Inspect.
func (f *Fixture[A, C, I]) Inspect() I { return I(struct{ *Chain }{f.Stage.Inspect()}) }
