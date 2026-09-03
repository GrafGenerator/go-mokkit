// The gomock example suite, ported verb for verb onto testify mocks: the check
// that the vocabulary shape does not change when the mocking library does. The
// verbs arrange through the mock's own type, the subject receives it through
// the interface, and Satisfied() pulls an unmet expectation forward to the
// test's own line.
package mokkitmockery_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/GrafGenerator/go-mokkit"
	"github.com/GrafGenerator/go-mokkit/container/bag"
	"github.com/GrafGenerator/go-mokkit/container/mokkitmockery"
	"github.com/GrafGenerator/go-mokkit/container/mokkitmockery/internal/shop"
)

// --- the roles ---------------------------------------------------------------

// A token is a type that names one artifact, and by embedding Artifact it
// declares in the same line what that name stands for. A verb generic over
// Token[shop.User] will not accept Audited, so a role and its artifact are
// paired by the compiler rather than by the reader's memory of a string.
type (
	Buyer  struct{ mokkit.Artifact[shop.User] }
	Seller struct{ mokkit.Artifact[shop.User] }

	// Audited names what a mock recorded, not a domain object: the artifact
	// under a token is whatever the test needs to carry between phases.
	Audited struct{ mokkit.Artifact[recordedAudit] }
)

// --- the vocabulary ----------------------------------------------------------

type (
	Arrange struct{ *mokkit.Chain }
	Act     struct{ *mokkit.Chain }
	Inspect struct{ *mokkit.Chain }
)

// And and All are re-declared so a chain that runs foreign vocabulary keeps
// this type rather than decaying to *mokkit.Chain.
func (a Arrange) And(steps ...mokkit.Step) Arrange {
	a.Helper()
	a.Chain.And(steps...)

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

type userOpt func(*shop.User)

func withStatus(s string) userOpt { return func(u *shop.User) { u.Status = s } }
func withID(id string) userOpt    { return func(u *shop.User) { u.ID = id } }

// newUser builds the user a verb arranges, seeding the identifier from the role
// so two actors in one test differ without the test spelling ids at all.
func newUser(role string, opts ...userOpt) shop.User {
	u := shop.User{ID: strings.ToLower(role) + "-1", Status: "regular"}
	for _, opt := range opts {
		opt(&u)
	}

	return u
}

// UserExists stubs the lookup for the role K and states that it must happen
// exactly once. With testify the interaction assertion is declared here rather
// than asserted later in Inspect — AssertExpectations checks it when the test
// finishes, and Satisfied() brings that check forward.
//
// The role is a type parameter rather than a *shop.User out-parameter, so the
// chain stays whole and the step reports under the role it acted for.
func (a Arrange) UserExists[K mokkit.Token[shop.User]](opts ...userOpt) Arrange {
	a.Helper()
	a.Add("UserExists["+mokkit.NameOf[K]()+"]", func(_ context.Context, h mokkit.Host) error {
		u := newUser(mokkit.NameOf[K](), opts...)

		h.Resolve[*shop.MockUserRepository]().EXPECT().
			ByID(mock.Anything, u.ID).Return(u, nil).Once()

		*a.New[K]() = u

		return nil
	})

	return a
}

// AUser is the same arrangement in return form, which is what a test with a
// single actor wants: no role has to be named, and the user is simply the value
// the verb hands back. It ends the chain, which is the whole reason the token
// form above exists for tests with more than one actor.
func (a Arrange) AUser(opts ...userOpt) shop.User {
	a.Helper()

	var u shop.User

	a.Add("AUser", func(_ context.Context, h mokkit.Host) error {
		u = newUser("u", opts...)

		h.Resolve[*shop.MockUserRepository]().EXPECT().
			ByID(mock.Anything, u.ID).Return(u, nil).Once()

		return nil
	})

	return u
}

func (a Arrange) DiscountRateIs(status string, rate float64) Arrange {
	a.Helper()
	a.Add("DiscountRateIs", func(_ context.Context, h mokkit.Host) error {
		h.Resolve[*shop.MockRateRepository]().EXPECT().
			RateFor(mock.Anything, status).Return(rate, nil)

		return nil
	})

	return a
}

func (a Arrange) AuditAccepts() Arrange {
	a.Helper()
	a.Add("AuditAccepts", func(_ context.Context, h mokkit.Host) error {
		h.Resolve[*shop.MockAudit]().EXPECT().
			Record(mock.Anything, mock.Anything, mock.Anything).Return(nil)

		return nil
	})

	return a
}

// recordedAudit is what the audit mock saw, kept so Inspect can assert on the
// interaction at the test's own line instead of at the controller's cleanup.
type recordedAudit struct {
	userID string
	amount float64
}

// AuditRecords is a producing verb whose artifact is filled by the mock rather
// than by the verb: the sink is taken while arranging and written when the
// subject calls Record. Reading it with Of in Inspect is safe because the
// phases are separate statements.
func (a Arrange) AuditRecords[K mokkit.Token[recordedAudit]]() Arrange {
	a.Helper()
	a.Add("AuditRecords["+mokkit.NameOf[K]()+"]", func(_ context.Context, h mokkit.Host) error {
		into := a.New[K]()

		h.Resolve[*shop.MockAudit]().EXPECT().
			Record(mock.Anything, mock.Anything, mock.Anything).
			RunAndReturn(func(_ context.Context, userID string, amount float64) error {
				*into = recordedAudit{userID: userID, amount: amount}

				return nil
			})

		return nil
	})

	return a
}

// CalculateDiscount takes the user as a value, which is what makes it safe to
// write f.Of[Buyer]() at the call site: a consuming verb reads an artifact some
// earlier statement produced.
func (a Act) CalculateDiscount(u shop.User, total float64) shop.Result {
	a.Helper()

	var out shop.Result

	a.Add("CalculateDiscount", func(ctx context.Context, h mokkit.Host) error {
		var err error
		out, err = h.Resolve[*shop.DiscountService]().Calculate(ctx, u.ID, total)

		return err
	})

	return out
}

func (i Inspect) DiscountApplied(r shop.Result, want float64) Inspect {
	i.Helper()
	i.Add("DiscountApplied", func(context.Context, mokkit.Host) error {
		if r.Discount != want {
			return fmt.Errorf("want discount %v, got %v", want, r.Discount)
		}

		return nil
	})

	return i
}

func (i Inspect) CalculatedFor(r shop.Result, u shop.User) Inspect {
	i.Helper()
	i.Add("CalculatedFor", func(context.Context, mokkit.Host) error {
		if r.UserID != u.ID {
			return fmt.Errorf("want result for %s, got %s", u.ID, r.UserID)
		}

		return nil
	})

	return i
}

// calculatedFor is the same observation authored as a plain function — the
// shape vocabulary takes when it lives in a package that cannot add methods to
// this chain's type. It stays generic over the token, so And and All keep the
// role both in what they compare and in what they report.
//
// The artifact is read here, while the step is being built on the test's own
// goroutine, rather than inside the closure: a branch of All that reached Of
// for a role nobody produced would fatal on the wrong goroutine.
func calculatedFor[K mokkit.Token[shop.User]](f *fixture, r shop.Result) mokkit.Step {
	want := f.Of[K]()

	return mokkit.NewStep("calculatedFor["+mokkit.NameOf[K]()+"]", func(context.Context, mokkit.Host) error {
		if r.UserID != want.ID {
			return fmt.Errorf("want result for %s, got %s", want.ID, r.UserID)
		}

		return nil
	})
}

// auditRecorded is the interaction assertion written as an Inspect step rather
// than folded into Arrange. It costs an artifact, and buys a failure that points
// at the test's own line instead of arriving at cleanup.
func auditRecorded[K mokkit.Token[recordedAudit]](f *fixture, userID string, amount float64) mokkit.Step {
	got := f.Of[K]()

	return mokkit.NewStep("auditRecorded["+mokkit.NameOf[K]()+"]", func(context.Context, mokkit.Host) error {
		if got.userID != userID || got.amount != amount {
			return fmt.Errorf("want audit of %s for %v, got %s for %v",
				userID, amount, got.userID, got.amount)
		}

		return nil
	})
}

// --- the fixture -------------------------------------------------------------

// fixture embeds the stage's Tokens, which is what puts f.Of[Buyer]() on the
// fixture itself — the same New and Of a verb reaches through its chain.
type fixture struct {
	*mokkit.Tokens

	stage *mokkit.Stage
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	mocks := mokkitmockery.New()
	mokkitmockery.Add[shop.UserRepository](mocks, shop.NewMockUserRepository)
	mokkitmockery.Add[shop.RateRepository](mocks, shop.NewMockRateRepository)
	mokkitmockery.Add[shop.Audit](mocks, shop.NewMockAudit)

	app := bag.New()
	bag.Scoped(app, func(r mokkit.Resolver) *shop.DiscountService {
		return &shop.DiscountService{
			Users: mokkit.Resolve[shop.UserRepository](r),
			Rates: mokkit.Resolve[shop.RateRepository](r),
			Audit: mokkit.Resolve[shop.Audit](r),
		}
	})

	setup, err := mokkit.NewSetup(context.Background(), mocks, app)
	if err != nil {
		t.Fatalf("composing: %v", err)
	}

	stage := setup.EnterStage(t)

	return &fixture{Tokens: stage.Tokens(), stage: stage}
}

func (f *fixture) Arrange() Arrange { return Arrange{f.stage.Arrange()} }
func (f *fixture) Act() Act         { return Act{f.stage.Act()} }
func (f *fixture) Inspect() Inspect { return Inspect{f.stage.Inspect()} }

// --- the tests ---------------------------------------------------------------

func TestCalculateDiscount_ForVipUser_AppliesTieredRate(t *testing.T) {
	f := newFixture(t)

	// One actor, so the return form: the arrangement ends by handing back the
	// user it made, and no role is named anywhere.
	user := f.Arrange().
		DiscountRateIs("vip", 0.15).
		AuditAccepts().
		AUser(withStatus("vip"))

	result := f.Act().CalculateDiscount(user, 100)

	f.Inspect().
		DiscountApplied(result, 15).
		CalculatedFor(result, user).
		And(mokkitmockery.Satisfied())

	// Note what is absent: no UserRepositoryQueried verb. The Once() declared
	// by AUser is the interaction assertion, and Satisfied() pulls its failure
	// forward to this test's line.
}

func TestCalculateDiscount_ObservingAnInteractionInInspect(t *testing.T) {
	f := newFixture(t)

	// The other way round: capture the call while arranging, under a role, and
	// assert on it in Inspect — so the failure lands on the test's own line.
	user := f.Arrange().
		DiscountRateIs("vip", 0.15).
		AuditRecords[Audited]().
		AUser(withStatus("vip"))

	result := f.Act().CalculateDiscount(user, 100)

	f.Inspect().
		DiscountApplied(result, 15).
		And(auditRecorded[Audited](f, user.ID, 15))
}

func TestCalculateDiscount_SeveralActors(t *testing.T) {
	f := newFixture(t)

	// Two actors, so the token form: each arrangement names the role it is for,
	// the chain stays unbroken, and the ids fall out of the roles.
	f.Arrange().
		UserExists[Buyer](withStatus("vip")).
		UserExists[Seller](withStatus("regular")).
		DiscountRateIs("vip", 0.15).
		DiscountRateIs("regular", 0.05).
		AuditAccepts()

	buyer := f.Act().CalculateDiscount(f.Of[Buyer](), 100)
	seller := f.Act().CalculateDiscount(f.Of[Seller](), 100)

	f.Inspect().
		DiscountApplied(buyer, 15).
		DiscountApplied(seller, 5).
		All(
			calculatedFor[Buyer](f, buyer),
			calculatedFor[Seller](f, seller),
		).
		And(mokkitmockery.Satisfied())
}

func TestCalculateDiscount_ARoleCanBeGivenAnIdentityOfItsOwn(t *testing.T) {
	f := newFixture(t)

	// The seeded id is a default, not a rule: an option overrides it, and the
	// role still says which user the verb is talking about.
	f.Arrange().
		UserExists[Buyer](withID("acct-4471"), withStatus("vip")).
		DiscountRateIs("vip", 0.15).
		AuditRecords[Audited]()

	result := f.Act().CalculateDiscount(f.Of[Buyer](), 200)

	f.Inspect().
		DiscountApplied(result, 30).
		And(calculatedFor[Buyer](f, result)).
		And(auditRecorded[Audited](f, "acct-4471", 30)).
		And(mokkitmockery.Satisfied())
}
