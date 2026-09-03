// This file is the design's proof: a small suite written the way the docs say
// to write one, using only the exported API, from a package that cannot add
// methods to mokkit's types.
package mokkit_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/GrafGenerator/go-mokkit"
	"github.com/GrafGenerator/go-mokkit/container/bag"
)

// --- the system under test ---------------------------------------------------

// Status is what a user's discount rate is looked up by.
type Status string

const (
	Regular Status = "regular"
	Vip     Status = "vip"
)

type User struct {
	ID     string
	Status Status
}

type Result struct {
	UserID   string
	Discount float64
}

type UserRepository interface {
	ByID(ctx context.Context, id string) (User, error)
}

type RateRepository interface {
	RateFor(ctx context.Context, status Status) (float64, error)
}

type DiscountService struct {
	Users UserRepository
	Rates RateRepository
}

func (s *DiscountService) Calculate(ctx context.Context, userID string, total float64) (Result, error) {
	user, err := s.Users.ByID(ctx, userID)
	if err != nil {
		return Result{}, err
	}

	rate, err := s.Rates.RateFor(ctx, user.Status)
	if err != nil {
		return Result{}, err
	}

	return Result{UserID: user.ID, Discount: total * rate}, nil
}

// --- test doubles (hand-rolled; adopting mokkit is not adopting a mock library)

type fakeUsers struct {
	mu      sync.Mutex
	byID    map[string]User
	queried []string
}

func (f *fakeUsers) add(u User) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.byID[u.ID] = u
}

func (f *fakeUsers) ByID(_ context.Context, id string) (User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.queried = append(f.queried, id)

	u, ok := f.byID[id]
	if !ok {
		return User{}, fmt.Errorf("no user %s", id)
	}

	return u, nil
}

func (f *fakeUsers) Queried() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.queried)
}

type fakeRates struct {
	mu      sync.Mutex
	byName  map[Status]float64
	queried []Status
}

func (f *fakeRates) set(status Status, rate float64) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.byName[status] = rate
}

func (f *fakeRates) RateFor(_ context.Context, status Status) (float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.queried = append(f.queried, status)

	r, ok := f.byName[status]
	if !ok {
		return 0, errors.New("no rate for " + string(status))
	}

	return r, nil
}

func (f *fakeRates) Queried() []Status {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.queried)
}

// --- the roles ---------------------------------------------------------------

// A token is a type that declares what it names: one line gives both the role
// and the type of the artifact filed under it, and every call site afterwards
// spells only the role — f.Of[Buyer]() is a User because Buyer says so.
type (
	Buyer  struct{ mokkit.Artifact[User] }
	Seller struct{ mokkit.Artifact[User] }
)

// --- the vocabulary ----------------------------------------------------------

// The three phase types are declared as one group, because gofumpt rejects
// three consecutive single-line type declarations and a suite people copy
// should lint clean where they copy it to.
type (
	Arrange struct{ *mokkit.Chain }
	Act     struct{ *mokkit.Chain }
	Inspect struct{ *mokkit.Chain }
)

// Chain's own chain-returning methods are promoted as returning *mokkit.Chain,
// which would end the fluent chain at the first call. Each vocabulary type
// re-declares the ones it wants to stay fluent — one line each, written once
// per suite. WithContext mutates the chain and hands it back exactly as And and
// All do, so its forwarder has the same shape as theirs.
func (a Arrange) And(steps ...mokkit.Step) Arrange {
	a.Helper()
	a.Chain.And(steps...)

	return a
}

func (a Arrange) All(steps ...mokkit.Step) Arrange {
	a.Helper()
	a.Chain.All(steps...)

	return a
}

func (i Inspect) And(steps ...mokkit.Step) Inspect {
	i.Helper()
	i.Chain.And(steps...)

	return i
}

func (i Inspect) All(steps ...mokkit.Step) Inspect {
	i.Helper()
	i.Chain.All(steps...)

	return i
}

func (a Arrange) WithContext(ctx context.Context) Arrange {
	a.Helper()
	a.Chain.WithContext(ctx)

	return a
}

// newUser seeds the identifier from the role, so a scene with several actors
// needs no explicit ids to tell them apart.
func newUser(role string, status Status) User {
	return User{ID: strings.ToLower(role) + "-1", Status: status}
}

// UserExists is the producing form: the role names the artifact, so the verb
// takes no sink and the chain is never broken to get one out. NameOf puts the
// role in the step label, which is what a failure reports under.
func (a Arrange) UserExists[K mokkit.Token[User]](status Status) Arrange {
	a.Helper()
	a.Add("UserExists["+mokkit.NameOf[K]()+"]", func(_ context.Context, h mokkit.Host) error {
		u := newUser(mokkit.NameOf[K](), status)
		h.Resolve[*fakeUsers]().add(u)
		*a.New[K]() = u

		return nil
	})

	return a
}

// AUser is the return form, and the default for a test with a single artifact:
// naming a role earns nothing when there is only one of something, and the verb
// is naturally terminal because the step has already run by the time it
// returns.
func (a Arrange) AUser(status Status) User {
	a.Helper()

	var u User

	a.Add("AUser", func(_ context.Context, h mokkit.Host) error {
		u = newUser("user", status)
		h.Resolve[*fakeUsers]().add(u)

		return nil
	})

	return u
}

func (a Arrange) RateIs(status Status, rate float64) Arrange {
	a.Helper()
	a.Add("RateIs", func(_ context.Context, h mokkit.Host) error {
		h.Resolve[*fakeRates]().set(status, rate)

		return nil
	})

	return a
}

// RateForUserIs is the consuming form: it takes the artifact by value, which is
// safe mid-chain because the spec orders method calls left to right — the verb
// that produced the user has run by the time Of reads it, and a value keeps
// *User out of a read-only position.
func (a Arrange) RateForUserIs(u User, rate float64) Arrange {
	a.Helper()
	a.Add("RateForUserIs", func(_ context.Context, h mokkit.Host) error {
		h.Resolve[*fakeRates]().set(u.Status, rate)

		return nil
	})

	return a
}

// CalculateDiscount is naturally terminal, so it hands its artifact back
// directly rather than filing it under a role.
func (a Act) CalculateDiscount(u User, total float64) Result {
	a.Helper()

	var out Result

	a.Add("CalculateDiscount", func(ctx context.Context, h mokkit.Host) error {
		var err error
		out, err = h.Resolve[*DiscountService]().Calculate(ctx, u.ID, total)

		return err
	})

	return out
}

func (i Inspect) DiscountApplied(r Result, want float64) Inspect {
	i.Helper()
	i.Add("DiscountApplied", func(context.Context, mokkit.Host) error {
		if r.Discount != want {
			return fmt.Errorf("want discount %v, got %v", want, r.Discount)
		}

		return nil
	})

	return i
}

func (i Inspect) CalculatedFor(r Result, u User) Inspect {
	i.Helper()
	i.Add("CalculatedFor", func(context.Context, mokkit.Host) error {
		if r.UserID != u.ID {
			return fmt.Errorf("want result for %s, got %s", u.ID, r.UserID)
		}

		return nil
	})

	return i
}

// userQueried and rateQueried are the plain-function form: this is how a
// package that cannot add methods to Inspect contributes vocabulary, and what
// And and All consume. Staying generic over the token keeps the role legible
// where the step is written.
func userQueried[K mokkit.Token[User]](f *fixture) mokkit.Step {
	// The artifact is read here, on the test's own goroutine, rather than
	// inside the step: Of reports a role nothing produced through Fatalf, and a
	// branch of All runs on a goroutine whose Goexit would abandon the test
	// rather than fail it.
	want := f.Of[K]().ID

	return mokkit.NewStep("userQueried["+mokkit.NameOf[K]()+"]", func(_ context.Context, h mokkit.Host) error {
		if got := h.Resolve[*fakeUsers]().Queried(); !slices.Contains(got, want) {
			return fmt.Errorf("want a lookup of %s, got %v", want, got)
		}

		return nil
	})
}

func rateQueried(status Status) mokkit.Step {
	return mokkit.NewStep("rateQueried("+string(status)+")", func(_ context.Context, h mokkit.Host) error {
		if got := h.Resolve[*fakeRates]().Queried(); !slices.Contains(got, status) {
			return fmt.Errorf("want a lookup of %s, got %v", status, got)
		}

		return nil
	})
}

// rateIsSeededElsewhere stands in for a verb owned by another package, which
// And runs without breaking the chain it appears in.
func rateIsSeededElsewhere(status Status, rate float64) mokkit.Step {
	return mokkit.NewStep("rates.Seeded", func(_ context.Context, h mokkit.Host) error {
		h.Resolve[*fakeRates]().set(status, rate)

		return nil
	})
}

type tenantKey struct{}

// tenantIs observes the context a step runs under, which is what makes
// WithContext testable from outside.
func tenantIs(want string) mokkit.Step {
	return mokkit.NewStep("tenantIs("+want+")", func(ctx context.Context, _ mokkit.Host) error {
		if got, _ := ctx.Value(tenantKey{}).(string); got != want {
			return fmt.Errorf("want tenant %q on the step's context, got %q", want, got)
		}

		return nil
	})
}

// --- the fixture -------------------------------------------------------------

// The fixture embeds the stage's token registry, which is what puts
// f.Of[Buyer]() on the fixture itself — the read side of the same registry the
// verbs write through.
type fixture struct {
	*mokkit.Tokens

	stage *mokkit.Stage
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	stage := discountStage(t)

	return &fixture{Tokens: stage.Tokens(), stage: stage}
}

func (f *fixture) Arrange() Arrange { return Arrange{f.stage.Arrange()} }
func (f *fixture) Act() Act         { return Act{f.stage.Act()} }
func (f *fixture) Inspect() Inspect { return Inspect{f.stage.Inspect()} }

// --- the tests ---------------------------------------------------------------

func TestCalculateDiscount_ForVipUser_AppliesTieredRate(t *testing.T) {
	f := newFixture(t)

	f.Arrange().
		UserExists[Buyer](Vip).
		RateForUserIs(f.Of[Buyer](), 0.15)

	result := f.Act().CalculateDiscount(f.Of[Buyer](), 100)

	f.Inspect().
		DiscountApplied(result, 15).
		CalculatedFor(result, f.Of[Buyer]()).
		And(userQueried[Buyer](f), rateQueried(Vip))
}

func TestCalculateDiscount_WithASingleArtifactTheVerbJustReturnsIt(t *testing.T) {
	f := newFixture(t)

	// One actor, no role: the artifact comes back from the verb that made it.
	user := f.Arrange().
		RateIs(Vip, 0.15).
		AUser(Vip)

	result := f.Act().CalculateDiscount(user, 200)

	f.Inspect().
		DiscountApplied(result, 30).
		CalculatedFor(result, user)
}

func TestCalculateDiscount_RolesCarryASceneWithSeveralActors(t *testing.T) {
	f := newFixture(t)

	f.Arrange().
		UserExists[Buyer](Vip).
		UserExists[Seller](Regular).
		All(
			// Arrange has an All too: steps that do not depend on each other
			// are unordered within the group and ordered against the verbs
			// around it.
			rateIsSeededElsewhere(Vip, 0.15),
			rateIsSeededElsewhere(Regular, 0.05),
		)

	buyerResult := f.Act().CalculateDiscount(f.Of[Buyer](), 100)
	sellerResult := f.Act().CalculateDiscount(f.Of[Seller](), 100)

	f.Inspect().
		DiscountApplied(buyerResult, 15).
		DiscountApplied(sellerResult, 5).
		All(
			// Group sequences steps into one branch, so a branch of All can be
			// a small story rather than a single observation: within a group
			// the order holds and a failure reports as "group: step", between
			// branches nothing is shared.
			mokkit.Group("the buyer",
				userQueried[Buyer](f),
				rateQueried(Vip),
			),
			mokkit.Group("the seller",
				userQueried[Seller](f),
				rateQueried(Regular),
			),
		).
		CalculatedFor(buyerResult, f.Of[Buyer]())
}

func TestForeignVocabularyKeepsTheChainUnbroken(t *testing.T) {
	f := newFixture(t)

	f.Arrange().
		UserExists[Buyer](Vip).
		And(rateIsSeededElsewhere(Vip, 0.15)).
		RateIs(Regular, 0.05)

	result := f.Act().CalculateDiscount(f.Of[Buyer](), 100)

	f.Inspect().DiscountApplied(result, 15)
}

func TestWithContextAppliesToTheStepsThatFollowIt(t *testing.T) {
	f := newFixture(t)

	ctx := context.WithValue(context.Background(), tenantKey{}, "acme")

	f.Arrange().
		UserExists[Buyer](Vip).
		WithContext(ctx).
		And(tenantIs("acme"))

	// The override belongs to the chain that asked for it: a chain started
	// afterwards runs on the stage's own context again.
	f.Inspect().And(tenantIs(""))
}

// --- the container -----------------------------------------------------------

// The doubles and the real subject are composed with bag: hand-wired, with each
// factory pulling its collaborators from the stage. That is all the C# original
// needed a DI container and a mock-to-DI bridge for.
func discountStage(t *testing.T) *mokkit.Stage {
	t.Helper()

	b := bag.New()

	// Each double is reachable under its concrete type, so vocabulary can
	// arrange and observe it, and under its interface, so the subject receives
	// that very same instance.
	bag.Scoped(b, func(mokkit.Resolver) *fakeUsers { return &fakeUsers{byID: map[string]User{}} })
	bag.Scoped(b, func(mokkit.Resolver) *fakeRates { return &fakeRates{byName: map[Status]float64{}} })
	bag.Alias[UserRepository, *fakeUsers](b)
	bag.Alias[RateRepository, *fakeRates](b)

	// A factory takes the resolver, so the free Resolve is what it uses; inside
	// a verb the Host's method form reads better and means the same thing.
	bag.Scoped(b, func(r mokkit.Resolver) *DiscountService {
		return &DiscountService{
			Users: mokkit.Resolve[UserRepository](r),
			Rates: mokkit.Resolve[RateRepository](r),
		}
	})

	setup, err := mokkit.NewSetup(context.Background(), b)
	if err != nil {
		t.Fatalf("composing the stage: %v", err)
	}

	return setup.EnterStage(t)
}
