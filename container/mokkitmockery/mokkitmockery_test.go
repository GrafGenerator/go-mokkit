package mokkitmockery

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/GrafGenerator/go-mokkit"
	"github.com/GrafGenerator/go-mokkit/container/mokkitmockery/internal/shop"
)

// fakeTB reports like *testing.T does — Fatalf unwinds via Goexit — so failure
// paths can be asserted on without failing this suite.
type fakeTB struct {
	name string

	mu       sync.Mutex
	errors   []string
	cleanups []func()
	failed   bool
}

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
	f.Errorf(format, args...)
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

func (f *fakeTB) runCleanups() {
	f.mu.Lock()
	cleanups := append([]func(){}, f.cleanups...)
	f.cleanups = nil
	f.mu.Unlock()

	for i := len(cleanups) - 1; i >= 0; i-- {
		cleanups[i]()
	}
}

func (f *fakeTB) reported() string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return strings.Join(f.errors, "\n")
}

func newSetup(t *testing.T) *mokkit.Setup {
	t.Helper()

	b := New()
	Add[shop.UserRepository](b, shop.NewMockUserRepository)

	setup, err := mokkit.NewSetup(context.Background(), b)
	if err != nil {
		t.Fatalf("NewSetup: %v", err)
	}

	return setup
}

func TestTheMockAnswersUnderBothItsTypes(t *testing.T) {
	stage := newSetup(t).EnterStage(t)

	asInterface := mokkit.Resolve[shop.UserRepository](stage)
	asMock := mokkit.Resolve[*shop.MockUserRepository](stage)

	if any(asInterface) != any(asMock) {
		t.Error("the interface and the mock type must resolve to one instance")
	}
}

func TestEachStageGetsItsOwnMock(t *testing.T) {
	setup := newSetup(t)

	first := mokkit.Resolve[*shop.MockUserRepository](setup.EnterStage(t))
	second := mokkit.Resolve[*shop.MockUserRepository](setup.EnterStage(t))

	if first == second {
		t.Error("stages must not share a mock")
	}
}

// The reason the adapter hands the constructor the stage's test: an expectation
// nobody met fails that test when it finishes, with no extra wiring.
func TestAnUnmetExpectationFailsTheTestAtCleanup(t *testing.T) {
	tb := &fakeTB{name: t.Name()}
	setup := newSetup(t)

	done := make(chan struct{})
	go func() {
		defer close(done)

		stage := setup.EnterStageContext(context.Background(), tb)
		mokkit.Resolve[*shop.MockUserRepository](stage).EXPECT().
			ByID(context.Background(), "u-1").Return(shop.User{}, nil).Once()
	}()
	<-done

	tb.runCleanups()

	if !tb.Failed() {
		t.Fatal("an unmet expectation must fail the test")
	}
	if got := tb.reported(); !strings.Contains(got, "ByID") {
		t.Errorf("the failure must name the missing call, got:\n%s", got)
	}
}

func TestSatisfiedReportsTheMissingCallAsAStepError(t *testing.T) {
	tb := &fakeTB{name: t.Name()}
	setup := newSetup(t)

	done := make(chan struct{})
	go func() {
		defer close(done)

		stage := setup.EnterStageContext(context.Background(), tb)
		repo := mokkit.Resolve[*shop.MockUserRepository](stage)
		repo.EXPECT().ByID(context.Background(), "u-1").Return(shop.User{ID: "u-1"}, nil).Once()

		stage.Inspect().And(Satisfied())
	}()
	<-done

	if got := tb.reported(); !strings.Contains(got, "inspect: mockery.Satisfied") {
		t.Errorf("Satisfied must report through the inspect chain, got:\n%s", got)
	}

	// Meeting the expectation afterwards keeps cleanup quiet about it.
	tb.runCleanups()
}

func TestSatisfiedPassesOnceEveryExpectationIsMet(t *testing.T) {
	stage := newSetup(t).EnterStage(t)

	repo := mokkit.Resolve[*shop.MockUserRepository](stage)
	repo.EXPECT().ByID(context.Background(), "u-1").Return(shop.User{ID: "u-1"}, nil).Once()

	if _, err := repo.ByID(context.Background(), "u-1"); err != nil {
		t.Fatalf("ByID: %v", err)
	}

	stage.Inspect().And(Satisfied())
}

func TestAddRefusesWhatCannotStandIn(t *testing.T) {
	t.Run("not an interface", func(t *testing.T) {
		defer expectPanic(t, "is not an interface")
		Add[shop.User](New(), func(TestingT) shop.User { return shop.User{} })
	})

	t.Run("does not implement", func(t *testing.T) {
		defer expectPanic(t, "does not implement")
		Add[shop.RateRepository](New(), shop.NewMockUserRepository)
	})

	t.Run("registered twice", func(t *testing.T) {
		defer expectPanic(t, "already registered")
		b := New()
		Add[shop.UserRepository](b, shop.NewMockUserRepository)
		Add[shop.UserRepository](b, shop.NewMockUserRepository)
	})

	t.Run("after build", func(t *testing.T) {
		defer expectPanic(t, "after the container was built")
		b := New()
		if _, err := b.Build(context.Background()); err != nil {
			t.Fatalf("Build: %v", err)
		}
		Add[shop.UserRepository](b, shop.NewMockUserRepository)
	})
}

// expectPanic is always used as `defer expectPanic(t, ...)`, which makes it the
// deferred function itself — so recover here is direct and does work.
//
//nolint:revive // the rule cannot see the deferred call site.
func expectPanic(t *testing.T, want string) {
	t.Helper()

	r := recover()
	if r == nil {
		t.Fatalf("expected a panic containing %q", want)
	}
	if got, ok := r.(string); !ok || !strings.Contains(got, want) {
		t.Fatalf("expected a panic containing %q, got %v", want, r)
	}
}
