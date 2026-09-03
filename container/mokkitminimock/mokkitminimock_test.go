package mokkitminimock

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/gojuno/minimock/v3"

	"github.com/GrafGenerator/go-mokkit"
	"github.com/GrafGenerator/go-mokkit/container/mokkitminimock/internal/shop"
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
	Add[shop.UserRepository](b, shop.NewUserRepositoryMock)

	setup, err := mokkit.NewSetup(context.Background(), b)
	if err != nil {
		t.Fatalf("NewSetup: %v", err)
	}

	return setup
}

func TestTheMockAnswersUnderBothItsTypes(t *testing.T) {
	stage := newSetup(t).EnterStage(t)

	asInterface := mokkit.Resolve[shop.UserRepository](stage)
	asMock := mokkit.Resolve[*shop.UserRepositoryMock](stage)

	if any(asInterface) != any(asMock) {
		t.Error("the interface and the mock type must resolve to one instance")
	}
}

func TestEachStageGetsItsOwnMock(t *testing.T) {
	setup := newSetup(t)

	first := mokkit.Resolve[*shop.UserRepositoryMock](setup.EnterStage(t))
	second := mokkit.Resolve[*shop.UserRepositoryMock](setup.EnterStage(t))

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
		mokkit.Resolve[*shop.UserRepositoryMock](stage).ByIDMock.
			Expect(minimock.AnyContext, "u-1").Return(shop.User{}, nil)
	}()
	<-done

	// Cleanup runs on its own goroutine: a reporter may Fatalf, which Goexits.
	cleaned := make(chan struct{})
	go func() {
		defer close(cleaned)
		tb.runCleanups()
	}()
	<-cleaned

	if !tb.Failed() {
		t.Fatal("an unmet expectation must fail the test")
	}
	if got := tb.reported(); !strings.Contains(got, "ByID") {
		t.Errorf("the failure must name the missing call, got:\n%s", got)
	}
}

func TestAddRefusesWhatCannotStandIn(t *testing.T) {
	t.Run("not an interface", func(t *testing.T) {
		defer expectPanic(t, "is not an interface")
		Add[shop.User](New(), func(minimock.Tester) shop.User { return shop.User{} })
	})

	t.Run("does not implement", func(t *testing.T) {
		defer expectPanic(t, "does not implement")
		Add[shop.RateRepository](New(), shop.NewUserRepositoryMock)
	})

	t.Run("registered twice", func(t *testing.T) {
		defer expectPanic(t, "already registered")
		b := New()
		Add[shop.UserRepository](b, shop.NewUserRepositoryMock)
		Add[shop.UserRepository](b, shop.NewUserRepositoryMock)
	})

	t.Run("after build", func(t *testing.T) {
		defer expectPanic(t, "after the container was built")
		b := New()
		if _, err := b.Build(context.Background()); err != nil {
			t.Fatalf("Build: %v", err)
		}
		Add[shop.UserRepository](b, shop.NewUserRepositoryMock)
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
