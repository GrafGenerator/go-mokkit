package mokkitdo_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/samber/do/v2"

	"github.com/GrafGenerator/go-mokkit"
	"github.com/GrafGenerator/go-mokkit/container/bag"
	"github.com/GrafGenerator/go-mokkit/container/mokkitdo"
)

// --- a small domain: a fake the test arranges, a subject do assembles ---------

type User struct {
	ID     string
	Status string
}

type UserRepository interface {
	ByID(ctx context.Context, id string) (User, error)
}

type fakeUsers struct {
	users map[string]User
}

func newFakeUsers() *fakeUsers { return &fakeUsers{users: map[string]User{}} }

func (f *fakeUsers) add(u User) { f.users[u.ID] = u }

func (f *fakeUsers) ByID(_ context.Context, id string) (User, error) {
	u, ok := f.users[id]
	if !ok {
		return User{}, fmt.Errorf("no user %q", id)
	}

	return u, nil
}

// DiscountService is the subject. In the composition below it is built by do,
// not by hand, over a repository the test arranges through bag.
type DiscountService struct {
	Users UserRepository

	closed bool
}

func (s *DiscountService) DiscountFor(ctx context.Context, id string) (float64, error) {
	u, err := s.Users.ByID(ctx, id)
	if err != nil {
		return 0, err
	}
	if u.Status == "vip" {
		return 0.15, nil
	}

	return 0, nil
}

// Shutdown makes the subject a do.Shutdowner, so the test below can see do's
// lifecycle running when the stage closes.
func (s *DiscountService) Shutdown() { s.closed = true }

func compose(t *testing.T) *mokkit.Setup {
	t.Helper()

	// The doubles live in bag: hand-wired, arranged by the test.
	doubles := bag.New()
	bag.Scoped(doubles, func(mokkit.Resolver) *fakeUsers { return newFakeUsers() })
	bag.Alias[UserRepository, *fakeUsers](doubles)

	// The subject lives in do, assembled from whatever the composition holds.
	di := mokkitdo.New()
	mokkitdo.Provide(di, func(inj do.Injector) (*DiscountService, error) {
		return &DiscountService{Users: mokkitdo.FromStage[UserRepository](inj)}, nil
	})

	setup, err := mokkit.NewSetup(context.Background(), doubles, di)
	if err != nil {
		t.Fatalf("NewSetup: %v", err)
	}

	return setup
}

func TestTheSubjectIsAssembledByDoOverTheStagesDoubles(t *testing.T) {
	stage := compose(t).EnterStage(t)

	mokkit.Resolve[*fakeUsers](stage).add(User{ID: "u-1", Status: "vip"})

	rate, err := mokkit.Resolve[*DiscountService](stage).DiscountFor(context.Background(), "u-1")
	if err != nil {
		t.Fatalf("DiscountFor: %v", err)
	}
	if rate != 0.15 {
		t.Errorf("want the vip rate through the do-built subject, got %v", rate)
	}
}

func TestAStageResolvesOneSubjectAndStagesDoNotShareIt(t *testing.T) {
	setup := compose(t)
	stage := setup.EnterStage(t)

	first := mokkit.Resolve[*DiscountService](stage)
	second := mokkit.Resolve[*DiscountService](stage)
	if first != second {
		t.Error("one stage must hold one subject")
	}

	other := mokkit.Resolve[*DiscountService](setup.EnterStage(t))
	if first == other {
		t.Error("stages must not share a subject")
	}
}

func TestDoShutdownHooksRunWhenTheStageCloses(t *testing.T) {
	setup := compose(t)

	var subject *DiscountService

	t.Run("a stage that resolves the subject", func(t *testing.T) {
		subject = mokkit.Resolve[*DiscountService](setup.EnterStage(t))
	})

	if !subject.closed {
		t.Error("closing the stage must run do's shutdown hooks")
	}
}

func TestAnUnprovidedTypeIsAnAbsenceNotAFailure(t *testing.T) {
	stage := compose(t).EnterStage(t)

	if _, ok := mokkit.TryResolve[chan int](stage); ok {
		t.Error("a type nobody provided must resolve to nothing")
	}
}

func TestProvideRefusesDuplicatesAndLateRegistration(t *testing.T) {
	t.Run("provided twice", func(t *testing.T) {
		defer expectPanic(t, "already provided")
		di := mokkitdo.New()
		mokkitdo.Supply(di, 1)
		mokkitdo.Supply(di, 2)
	})

	t.Run("after build", func(t *testing.T) {
		defer expectPanic(t, "after the container was built")
		di := mokkitdo.New()
		if _, err := di.Build(context.Background()); err != nil {
			t.Fatalf("Build: %v", err)
		}
		mokkitdo.Supply(di, 1)
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
