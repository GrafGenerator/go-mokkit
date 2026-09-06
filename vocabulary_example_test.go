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

type (
	Arrange struct{ *mokkit.Chain }
	Act     struct{ *mokkit.Chain }
	Inspect struct{ *mokkit.Chain }
)

// And, All and WithContext are promoted from *Chain returning *Chain, so a
// vocabulary type re-declares the ones it wants fluent.
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

// newUser seeds the identifier from the role.
func newUser(role string, status Status) User {
	return User{ID: strings.ToLower(role) + "-1", Status: status}
}

// UserExists files a user under the role K. The role goes into the step label.
func (a Arrange) UserExists[K mokkit.Token[User]](status Status) Arrange {
	a.Helper()

	return mokkit.DoFor[K](a, func(h mokkit.Host) {
		u := newUser(mokkit.NameOf[K](), status)
		h.Resolve[*fakeUsers]().add(u)
		*a.New[K]() = u
	})
}

// AUser hands the user straight back, for a test with a single actor.
func (a Arrange) AUser(status Status) User {
	a.Helper()

	return a.Get(func(h mokkit.Host) (User, error) {
		u := newUser("user", status)
		h.Resolve[*fakeUsers]().add(u)

		return u, nil
	})
}

func (a Arrange) RateIs(status Status, rate float64) Arrange {
	a.Helper()

	return mokkit.Do(a, func(h mokkit.Host) { h.Resolve[*fakeRates]().set(status, rate) })
}

// RateForUserIs takes the user by value: the verb that produced it has run by
// the time Of reads it.
func (a Arrange) RateForUserIs(u User, rate float64) Arrange {
	a.Helper()

	return mokkit.Do(a, func(h mokkit.Host) { h.Resolve[*fakeRates]().set(u.Status, rate) })
}

// CalculateDiscount hands its result back. An error fails the act.
func (a Act) CalculateDiscount(u User, total float64) Result {
	a.Helper()

	return a.Get(func(h mokkit.Host) (Result, error) {
		return h.Resolve[*DiscountService]().Calculate(h.Context(), u.ID, total)
	})
}

// TryCalculateDiscount hands back the outcome, error included, for a test
// about a refusal.
func (a Act) TryCalculateDiscount(u User, total float64) mokkit.Outcome[Result] {
	a.Helper()

	return a.Try(func(h mokkit.Host) (Result, error) {
		return h.Resolve[*DiscountService]().Calculate(h.Context(), u.ID, total)
	})
}

func (i Inspect) DiscountApplied(r Result, want float64) Inspect {
	i.Helper()

	return mokkit.Do(i, func(mokkit.Host) error {
		if r.Discount != want {
			return fmt.Errorf("want discount %v, got %v", want, r.Discount)
		}

		return nil
	})
}

func (i Inspect) CalculatedFor(r Result, u User) Inspect {
	i.Helper()

	return mokkit.Do(i, func(mokkit.Host) error {
		if r.UserID != u.ID {
			return fmt.Errorf("want result for %s, got %s", u.ID, r.UserID)
		}

		return nil
	})
}

func (i Inspect) Refused(o mokkit.Outcome[Result], because string) Inspect {
	i.Helper()

	return mokkit.Do(i, func(mokkit.Host) error {
		if o.Err == nil {
			return fmt.Errorf("want a refusal because %s, got %+v", because, o.Value)
		}
		if !strings.Contains(o.Err.Error(), because) {
			return fmt.Errorf("want the refusal to say %q, got: %w", because, o.Err)
		}

		return nil
	})
}

// userQueried and rateQueried are plain-function vocabulary: what a package
// that cannot add methods to Inspect publishes, and what And and All take.
func userQueried[K mokkit.Token[User]](f *fixture) mokkit.Step {
	// Of reports an unarranged role through Fatalf, so it is read here, on the
	// test's goroutine, rather than inside a branch of All.
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

// rateIsSeededElsewhere stands in for a verb owned by another package.
func rateIsSeededElsewhere(status Status, rate float64) mokkit.Step {
	return mokkit.NewStep("rates.Seeded", func(_ context.Context, h mokkit.Host) error {
		h.Resolve[*fakeRates]().set(status, rate)

		return nil
	})
}

type tenantKey struct{}

// tenantIs observes the context a step runs under.
func tenantIs(want string) mokkit.Step {
	return mokkit.NewStep("tenantIs("+want+")", func(ctx context.Context, _ mokkit.Host) error {
		if got, _ := ctx.Value(tenantKey{}).(string); got != want {
			return fmt.Errorf("want tenant %q on the step's context, got %q", want, got)
		}

		return nil
	})
}

// --- the fixture -------------------------------------------------------------

type fixture = mokkit.Fixture[Arrange, Act, Inspect]

// newFixture composes the doubles and the real subject with bag, per test.
// Each double is registered under its concrete type, for the vocabulary, and
// aliased to its interface, for the subject.
func newFixture(t *testing.T) *fixture {
	t.Helper()

	b := bag.New()
	bag.Scoped(b, func(mokkit.Resolver) *fakeUsers { return &fakeUsers{byID: map[string]User{}} })
	bag.Scoped(b, func(mokkit.Resolver) *fakeRates { return &fakeRates{byName: map[Status]float64{}} })
	bag.Alias[UserRepository, *fakeUsers](b)
	bag.Alias[RateRepository, *fakeRates](b)
	bag.Scoped(b, func(r mokkit.Resolver) *DiscountService {
		return &DiscountService{
			Users: mokkit.Resolve[UserRepository](r),
			Rates: mokkit.Resolve[RateRepository](r),
		}
	})

	return mokkit.Enter[Arrange, Act, Inspect](t, b)
}

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

func TestCalculateDiscount_ForAnUnknownUser_IsRefused(t *testing.T) {
	f := newFixture(t)

	f.Arrange().RateIs(Vip, 0.15)

	outcome := f.Act().TryCalculateDiscount(User{ID: "ghost"}, 100)

	f.Inspect().Refused(outcome, "no user ghost")
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
