package mokkitdig_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/GrafGenerator/go-mokkit"
	"github.com/GrafGenerator/go-mokkit/container/bag"
	"github.com/GrafGenerator/go-mokkit/container/mokkitdig"
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

func compose(t *testing.T) *mokkit.Setup {
	t.Helper()

	// The doubles live in bag: hand-wired, arranged by the test.
	doubles := bag.New()
	bag.Scoped(doubles, func(mokkit.Resolver) *fakeUsers { return newFakeUsers() })
	bag.Alias[UserRepository, *fakeUsers](doubles)

	// The subject lives in dig, assembled from whatever the composition holds:
	// Bridge hands dig the repository the test arranges through bag.
	di := mokkitdig.New()
	di.Provide(func(users UserRepository) *DiscountService {
		return &DiscountService{Users: users}
	})
	mokkitdig.Expose[*DiscountService](di)
	mokkitdig.Bridge[UserRepository](di)

	setup, err := mokkit.NewSetup(context.Background(), doubles, di)
	if err != nil {
		t.Fatalf("NewSetup: %v", err)
	}

	return setup
}

func TestTheSubjectIsAssembledByDigOverTheStagesDoubles(t *testing.T) {
	stage := compose(t).EnterStage(t)

	mokkit.Resolve[*fakeUsers](stage).add(User{ID: "u-1", Status: "vip"})

	rate, err := mokkit.Resolve[*DiscountService](stage).DiscountFor(context.Background(), "u-1")
	if err != nil {
		t.Fatalf("DiscountFor: %v", err)
	}
	if rate != 0.15 {
		t.Errorf("want the vip rate through the dig-built subject, got %v", rate)
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

func TestAnUnprovidedTypeIsAnAbsenceNotAFailure(t *testing.T) {
	stage := compose(t).EnterStage(t)

	if _, ok := mokkit.TryResolve[chan int](stage); ok {
		t.Error("a type nobody provided must resolve to nothing")
	}
}

func TestExposeRefusesDuplicatesAndLateRegistration(t *testing.T) {
	t.Run("exposed twice", func(t *testing.T) {
		defer expectPanic(t, "already exposed")
		di := mokkitdig.New()
		mokkitdig.Expose[int](di)
		mokkitdig.Expose[int](di)
	})

	t.Run("after build", func(t *testing.T) {
		defer expectPanic(t, "after the container was built")
		di := mokkitdig.New()
		if _, err := di.Build(context.Background()); err != nil {
			t.Fatalf("Build: %v", err)
		}
		mokkitdig.Expose[int](di)
	})
}

// The failure mode dig cannot have as an absence: an exposed type whose
// dependencies are missing is a broken fixture, and says so.
func TestAnExposedButUnbuildableTypeSaysWhatIsMissing(t *testing.T) {
	di := mokkitdig.New()
	di.Provide(func(s string) int { return len(s) })
	mokkitdig.Expose[int](di)

	setup, err := mokkit.NewSetup(context.Background(), di)
	if err != nil {
		t.Fatalf("NewSetup: %v", err)
	}
	stage := setup.EnterStage(t)

	defer expectPanic(t, "building int")
	mokkit.Resolve[int](stage)
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
