package mokkit

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
)

func TestStageResolvesFromContainersAndCachesTheInstance(t *testing.T) {
	greeter := &recordingGreeter{}
	stage := stageWith(t, newFakeTB(t.Name()), greeter)

	a := Resolve[Greeter](stage)
	b := Resolve[Greeter](stage)

	if a != b {
		t.Error("every step in a stage must see the same instance")
	}
	if a != Greeter(greeter) {
		t.Error("expected the registered greeter")
	}
}

func TestConcurrentResolveIsRaceFreeAndYieldsOneInstance(t *testing.T) {
	// All's branches resolve concurrently, so this has to hold.
	stage := stageWith(t, newFakeTB(t.Name()), &recordingGreeter{})

	const n = 32
	got := make([]Greeter, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i] = Resolve[Greeter](stage)
		}(i)
	}
	wg.Wait()

	for i := 1; i < n; i++ {
		if got[i] != got[0] {
			t.Fatalf("concurrent resolves disagreed at %d", i)
		}
	}
}

func TestTryResolveReportsAMissingService(t *testing.T) {
	stage := stageWith(t, newFakeTB(t.Name()), nil)

	if _, ok := TryResolve[Greeter](stage); ok {
		t.Error("nothing was registered, so TryResolve must report false")
	}
}

func TestStagesAreIsolatedFromEachOther(t *testing.T) {
	c := newFakeContainer()
	register[Greeter](c, &recordingGreeter{})
	setup, err := NewSetup(context.Background(), c)
	if err != nil {
		t.Fatalf("NewSetup: %v", err)
	}

	first := setup.EnterStage(newFakeTB("first"))
	second := setup.EnterStage(newFakeTB("second"))

	if first.ID() == second.ID() {
		t.Error("each entered stage needs its own identity")
	}
	if first.Tokens() == second.Tokens() {
		t.Error("artifacts must not leak between tests")
	}
	if c.scopes != 2 {
		t.Errorf("expected one scope per stage, got %d", c.scopes)
	}
}

func TestEnteringAStageRegistersItsOwnCleanup(t *testing.T) {
	c := newFakeContainer()
	setup, err := NewSetup(context.Background(), c)
	if err != nil {
		t.Fatalf("NewSetup: %v", err)
	}

	tb := newFakeTB(t.Name())
	setup.EnterStage(tb)
	if c.closes != 0 {
		t.Fatal("the scope must stay open for the duration of the test")
	}

	tb.RunCleanups()
	if c.closes != 1 {
		t.Errorf("expected the stage to close its scope on cleanup, got %d", c.closes)
	}
}

func TestContainersAreBuiltOncePerSetup(t *testing.T) {
	c := &countingBuilder{inner: newFakeContainer()}

	setup, err := NewSetup(context.Background(), c)
	if err != nil {
		t.Fatalf("NewSetup: %v", err)
	}
	setup.EnterStage(newFakeTB("a"))
	setup.EnterStage(newFakeTB("b"))

	if c.builds != 1 {
		t.Errorf("composing is the expensive part and must happen once, got %d builds", c.builds)
	}
}

func TestNewSetupReportsWhichBuilderFailed(t *testing.T) {
	_, err := NewSetup(context.Background(), &failingBuilder{})

	if err == nil {
		t.Fatal("expected an error")
	}
	assertContains(t, []string{err.Error()}, "building container 0", "no connection string")
}

func TestEnterStageFailsTheTestWhenAScopeCannotOpen(t *testing.T) {
	c := newFakeContainer()
	c.scopeErr = errors.New("container not started")
	setup, err := NewSetup(context.Background(), c)
	if err != nil {
		t.Fatalf("NewSetup: %v", err)
	}

	tb := newFakeTB(t.Name())
	runGoexit(func() { setup.EnterStage(tb) })

	assertContains(t, tb.Fatals(), "opening scope on container 0", "container not started")
}

func TestStageIsAResolverAndBuildsTheHostAStepSees(t *testing.T) {
	greeter := &recordingGreeter{}
	stage := stageWith(t, newFakeTB(t.Name()), greeter)

	// Host is a struct now, so the stage is not one; it builds one, for the
	// occasions that want what a step receives without entering a chain.
	var _ Resolver = stage
	var _ PathResolver = stage

	if stage.Context() == nil {
		t.Error("a stage must carry a context for its steps")
	}
	if _, ok := stage.TryResolveType(typeOf[Greeter]()); !ok {
		t.Error("expected the greeter to resolve by type")
	}

	h := stage.Host()
	if h.Resolver() != Resolver(stage) {
		t.Error("a host built by the stage must resolve through it")
	}
	if h.Resolve[Greeter]() != Greeter(greeter) {
		t.Error("expected the registered greeter off the host")
	}
	if _, ok := h.TryResolve[*fakeContainer](); ok {
		t.Error("nothing was registered as *fakeContainer")
	}
}

type countingBuilder struct {
	inner  *fakeContainer
	builds int
}

func (b *countingBuilder) Build(ctx context.Context) (Container, error) {
	b.builds++

	return b.inner.Build(ctx)
}

type failingBuilder struct{}

func (failingBuilder) Build(context.Context) (Container, error) {
	return nil, errors.New("no connection string")
}

func TestEnterStageContextThreadsItsContextIntoSteps(t *testing.T) {
	// A test that needs a deadline, a trace or a cancellation gives it to the
	// stage once, and every step it runs is under it.
	c := newFakeContainer()
	setup, err := NewSetup(context.Background(), c)
	if err != nil {
		t.Fatalf("NewSetup: %v", err)
	}

	ctx := context.WithValue(context.Background(), ctxKey{}, "stage")
	stage := setup.EnterStageContext(ctx, newFakeTB(t.Name()))

	if got := stage.Context().Value(ctxKey{}); got != "stage" {
		t.Errorf("expected the stage to report its own context, got %v", got)
	}

	var fromStep, fromHost any
	stage.Arrange().Add("reads", func(ctx context.Context, h Host) error {
		fromStep, fromHost = ctx.Value(ctxKey{}), h.Context().Value(ctxKey{})

		return nil
	})

	if fromStep != "stage" || fromHost != "stage" {
		t.Errorf("expected the stage's context in the step and its host, got %v and %v", fromStep, fromHost)
	}
	if got := stage.Host().Context().Value(ctxKey{}); got != "stage" {
		t.Errorf("expected the stage's context on the host it builds, got %v", got)
	}
}

func TestEachStageGetsItsOwnTokens(t *testing.T) {
	// Tokens belong to a test, so the same role in two tests is two artifacts.
	c := newFakeContainer()
	setup, err := NewSetup(context.Background(), c)
	if err != nil {
		t.Fatalf("NewSetup: %v", err)
	}

	first := setup.EnterStage(newFakeTB("first"))
	second := setup.EnterStage(newFakeTB("second"))

	*first.Tokens().New[Buyer]() = User{ID: "first"}

	if second.Tokens().Declared[Buyer]() {
		t.Error("a role produced in one stage must not be visible in another")
	}
	if got := first.Tokens().Of[Buyer]().ID; got != "first" {
		t.Errorf("expected the first stage to keep its own artifact, got %q", got)
	}
}

// --- closing -----------------------------------------------------------------

func TestClosingAStageTwiceIsNotAnError(t *testing.T) {
	// Cleanup closes the stage, and a test that closed it itself must not turn
	// that into a second close of every scope, nor into a reported failure.
	c := newFakeContainer()
	setup, err := NewSetup(context.Background(), c)
	if err != nil {
		t.Fatalf("NewSetup: %v", err)
	}

	tb := newFakeTB(t.Name())
	stage := setup.EnterStage(tb)

	if err := stage.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := stage.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}

	tb.RunCleanups()
	if c.closes != 1 {
		t.Errorf("expected the scope to be closed exactly once, got %d", c.closes)
	}
	if got := tb.Errors(); len(got) != 0 {
		t.Errorf("closing twice must not report a failure, got %v", got)
	}
}

func TestClosingAStageEmptiesTheResolutionCache(t *testing.T) {
	// A stage has a lifetime rather than outliving its test as a bag of dead
	// instances: what it cached must not survive its scopes.
	stage := stageWith(t, newFakeTB(t.Name()), &recordingGreeter{})

	if _, ok := TryResolve[Greeter](stage); !ok {
		t.Fatal("expected the greeter to resolve while the stage is open")
	}
	if err := stage.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, ok := TryResolve[Greeter](stage); ok {
		t.Error("a closed stage must not keep handing out what it cached")
	}
}

func TestClosingIsRaceFreeAgainstConcurrentResolution(t *testing.T) {
	// All's branches resolve on their own goroutines, and a test that fails
	// while they are in flight closes the stage from under them.
	stage := stageWith(t, newFakeTB(t.Name()), &recordingGreeter{})

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)

		go func() {
			defer wg.Done()
			stage.TryResolveType(typeOf[Greeter]())
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = stage.Close()
	}()
	wg.Wait()

	if err := stage.Close(); err != nil {
		t.Errorf("Close after the race: %v", err)
	}
}

// --- resolving through the host a step receives ------------------------------

func TestResolvingAnUnregisteredServiceInAStepIsReportedAgainstTheStep(t *testing.T) {
	// h.Resolve panics on purpose: the executor turns it into a failure the
	// verb is named in, so vocabulary resolves without error handling.
	tb := newFakeTB(t.Name())
	stage := stageWith(t, tb, nil)

	runGoexit(func() {
		stage.Arrange().Add("greeterIsReady", func(_ context.Context, h Host) error {
			h.Resolve[Greeter]().Greet("nobody")

			return nil
		})
	})

	assertContains(t, tb.Fatals(),
		"arrange: greeterIsReady:",
		// A crash and a returned failure are different events, so the report
		// says which one this was even though the value is an error.
		"panic: mokkit: no service registered as mokkit.Greeter",
	)
}

func TestSomethingRegisteredUnderATypeItCannotBeUsedAsIsReportedAsSuch(t *testing.T) {
	// Registering the wrong thing under a type is a different bug from
	// registering nothing; the two must read differently.
	c := newFakeContainer()
	registerAs(c, typeOf[Greeter](), "not a greeter")
	setup, err := NewSetup(context.Background(), c)
	if err != nil {
		t.Fatalf("NewSetup: %v", err)
	}
	stage := setup.EnterStage(newFakeTB(t.Name()))

	if _, ok := TryResolve[Greeter](stage); ok {
		t.Error("what is registered cannot be used as a Greeter, so TryResolve must report false")
	}

	runErr := stage.ex.Run(context.Background(), NewStep("probe", func(_ context.Context, h Host) error {
		h.Resolve[Greeter]().Greet("nobody")

		return nil
	}))

	var resolveErr *ResolveError
	if !errors.As(runErr, &resolveErr) {
		t.Fatalf("expected a *ResolveError, got %v", runErr)
	}
	if !resolveErr.Present {
		t.Error("something was registered under the type, so Present must say so")
	}
	assertContains(t, []string{resolveErr.Error()},
		"what is registered as mokkit.Greeter cannot be used as it")
}

// --- the path that crosses containers ----------------------------------------

func TestStageThreadsTheConstructionPathAcrossContainers(t *testing.T) {
	// A container detects a cycle by looking at what is already under
	// construction. When the cycle runs out through the stage into a second
	// container, the path has to arrive there too — otherwise it is invisible to
	// both and they deadlock on a lock one of them holds.
	inner := &pathContainer{provides: typeOf[*recordingGreeter](), value: &recordingGreeter{}}
	outer := &pathContainer{
		provides: typeOf[Greeter](),
		value:    Greeter(&recordingGreeter{}),
		needs:    typeOf[*recordingGreeter](),
	}

	setup, err := NewSetup(context.Background(), outer, inner)
	if err != nil {
		t.Fatalf("NewSetup: %v", err)
	}
	stage := setup.EnterStage(newFakeTB(t.Name()))

	if _, ok := TryResolve[Greeter](stage); !ok {
		t.Fatal("expected the greeter to resolve")
	}

	// The outermost resolution starts with nothing under construction.
	if got := outer.seenPaths(); len(got) != 1 || len(got[0]) != 0 {
		t.Fatalf("expected one resolution under an empty path, got %v", got)
	}

	// The collaborator's container sees what the hop was made from.
	got := inner.seenPaths()
	if len(got) != 1 {
		t.Fatalf("expected the second container to be asked once, got %v", got)
	}
	if len(got[0]) != 1 || got[0][0] != typeOf[Greeter]() {
		t.Errorf("expected the path to arrive as [mokkit.Greeter], got %v", got[0])
	}
}

func TestAScopeThatDoesNotCarryPathsStillResolves(t *testing.T) {
	// PathResolver is the optional half of the contract: a container that never
	// resolves its own collaborators has no use for it.
	greeter := &recordingGreeter{}
	stage := stageWith(t, newFakeTB(t.Name()), greeter)

	v, ok := stage.TryResolveTypePath(typeOf[Greeter](), []reflect.Type{typeOf[*recordingGreeter]()})
	if !ok {
		t.Fatal("expected the greeter to resolve through the path-carrying call")
	}
	if v != any(greeter) {
		t.Errorf("expected the registered greeter, got %v", v)
	}
}
