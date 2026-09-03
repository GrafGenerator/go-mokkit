package mokkitgomock_test

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"

	"go.uber.org/mock/gomock"

	"github.com/GrafGenerator/go-mokkit"
	"github.com/GrafGenerator/go-mokkit/container/bag"
	"github.com/GrafGenerator/go-mokkit/container/mokkitgomock"
	"github.com/GrafGenerator/go-mokkit/container/mokkitgomock/internal/shop"
)

// The stage hands gomock a mokkit.TB, so TB has to be wide enough for the
// three shapes gomock looks for. TestReporter is the obvious one; TestHelper
// matters because NewController wraps a reporter that is not one in a
// nopTestHelper, and the Cleanup probe is then run against that wrapper — which
// has no Cleanup. Fail either and the controller never finishes, so every
// Times(n) quietly stops being enforced. The behavior these three make
// possible is pinned by TestAnUnmetTimesIsEnforcedWhenTheTestFinishes.
var (
	_ gomock.TestReporter = mokkit.TB(nil)
	_ gomock.TestHelper   = mokkit.TB(nil)

	// The cleanuper probe gomock uses is unexported, so this is its shape.
	_ interface{ Cleanup(func()) } = mokkit.TB(nil)
)

// compose wires the mocks and the real subject, the way a fixture would.
func compose(t *testing.T, tb mokkit.TB) *mokkit.Stage {
	t.Helper()

	mocks := mokkitgomock.New()
	mokkitgomock.Add[shop.UserRepository](mocks, shop.NewMockUserRepository)
	mokkitgomock.Add[shop.RateRepository](mocks, shop.NewMockRateRepository)
	mokkitgomock.Add[shop.Audit](mocks, shop.NewMockAudit)

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

	return setup.EnterStage(tb)
}

func TestTheSubjectReceivesTheMockTheTestArranges(t *testing.T) {
	stage := compose(t, t)
	ctx := context.Background()

	// Arrange through the mock's own type ...
	mokkit.Resolve[*shop.MockUserRepository](stage).EXPECT().
		ByID(gomock.Any(), "u-1").Return(shop.User{ID: "u-1", Status: "vip"}, nil)
	mokkit.Resolve[*shop.MockRateRepository](stage).EXPECT().
		RateFor(gomock.Any(), "vip").Return(0.15, nil)
	mokkit.Resolve[*shop.MockAudit](stage).EXPECT().
		Record(gomock.Any(), "u-1", 15.0).Return(nil)

	// ... and the subject, built in a different container, gets that instance.
	got, err := mokkit.Resolve[*shop.DiscountService](stage).Calculate(ctx, "u-1", 100)
	if err != nil {
		t.Fatalf("Calculate: %v", err)
	}
	if want := (shop.Result{UserID: "u-1", Discount: 15}); got != want {
		t.Errorf("want %+v, got %+v", want, got)
	}
}

func TestAMockResolvesIdenticallyByInterfaceAndByItsOwnType(t *testing.T) {
	stage := compose(t, t)

	concrete := mokkit.Resolve[*shop.MockUserRepository](stage)
	if mokkit.Resolve[shop.UserRepository](stage) != shop.UserRepository(concrete) {
		t.Error("both registrations must reach one instance, or arranging and injecting diverge")
	}
}

func TestEachStageGetsFreshMocks(t *testing.T) {
	mocks := mokkitgomock.New()
	mokkitgomock.Add[shop.UserRepository](mocks, shop.NewMockUserRepository)

	setup, err := mokkit.NewSetup(context.Background(), mocks)
	if err != nil {
		t.Fatalf("composing: %v", err)
	}

	first := mokkit.Resolve[*shop.MockUserRepository](setup.EnterStage(t))
	second := mokkit.Resolve[*shop.MockUserRepository](setup.EnterStage(t))

	if first == second {
		t.Error("expectations would carry between tests if stages shared a mock")
	}
}

func TestUnmetExpectationsFailTheTestTheStageBelongsTo(t *testing.T) {
	tb := &recordingTB{name: "inner"}
	stage := compose(t, tb)

	mokkit.Resolve[*shop.MockUserRepository](stage).EXPECT().
		ByID(gomock.Any(), "u-1").Return(shop.User{}, nil)

	// Nothing calls it. The controller finishes through the cleanup it
	// registered on this stage's test.
	tb.RunCleanups()

	if !tb.Failed() {
		t.Fatal("an unmet expectation must fail the test")
	}
	assertReported(t, tb.all(), "missing call(s)", "ByID")
}

func TestAnUnmetTimesIsEnforcedWhenTheTestFinishes(t *testing.T) {
	// Times(n) is not decided when the call is made but when the controller
	// finishes, and the controller only finishes because mokkit.TB carries
	// Cleanup past gomock's probe. Drive it with a reporter of our own, so the
	// failure can be observed rather than failing this test.
	tb := &recordingTB{name: "inner"}
	stage := compose(t, tb)

	mokkit.Resolve[*shop.MockAudit](stage).EXPECT().
		Record(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(2)

	if err := mokkit.Resolve[shop.Audit](stage).Record(context.Background(), "u-1", 15); err != nil {
		t.Fatalf("Record: %v", err)
	}

	if tb.Failed() {
		t.Fatalf("one of two calls is not yet a failure, got:\n%s", strings.Join(tb.all(), "\n"))
	}

	tb.RunCleanups()

	if !tb.Failed() {
		t.Fatal("an unmet Times(2) must fail the test the stage belongs to")
	}
	assertReported(t, tb.all(), "missing call(s)", "Record")
}

func TestScopesAreReleasedBeforeExpectationsAreChecked(t *testing.T) {
	// Stage.Close is registered after the controller's cleanup, and cleanups
	// run last-registered-first, so a scoped service is torn down before
	// gomock decides whether its calls happened.
	var order []string

	mocks := mokkitgomock.New()
	mokkitgomock.Add[shop.Audit](mocks, shop.NewMockAudit)

	app := bag.New()
	bag.Scoped(app, func(mokkit.Resolver) *closingService {
		return &closingService{onClose: func() { order = append(order, "scope closed") }}
	})

	setup, err := mokkit.NewSetup(context.Background(), mocks, app)
	if err != nil {
		t.Fatalf("composing: %v", err)
	}

	tb := &recordingTB{name: "inner", onFail: func() { order = append(order, "expectations checked") }}
	stage := setup.EnterStage(tb)

	mokkit.Resolve[*closingService](stage)
	mokkit.Resolve[*shop.MockAudit](stage).EXPECT().Record(gomock.Any(), gomock.Any(), gomock.Any())

	tb.RunCleanups()

	want := []string{"scope closed", "expectations checked"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("want %v, got %v", want, order)
	}
}

type closingService struct{ onClose func() }

func (s *closingService) Close() error {
	s.onClose()

	return nil
}

func TestTheStageControllerIsReachable(t *testing.T) {
	stage := compose(t, t)

	ctrl := mokkitgomock.Controller(stage)
	if ctrl == nil {
		t.Fatal("expected the stage's controller")
	}

	// It is the same controller the mocks were built with, so InOrder across
	// them behaves as expected.
	users := mokkit.Resolve[*shop.MockUserRepository](stage)
	rates := mokkit.Resolve[*shop.MockRateRepository](stage)
	gomock.InOrder(
		users.EXPECT().ByID(gomock.Any(), "u-1").Return(shop.User{ID: "u-1", Status: "vip"}, nil),
		rates.EXPECT().RateFor(gomock.Any(), "vip").Return(0.15, nil),
	)
	mokkit.Resolve[*shop.MockAudit](stage).EXPECT().Record(gomock.Any(), gomock.Any(), gomock.Any())

	if _, err := mokkit.Resolve[*shop.DiscountService](stage).Calculate(context.Background(), "u-1", 100); err != nil {
		t.Fatalf("Calculate: %v", err)
	}
}

func TestConcurrentArrangingAndCallingIsSafe(t *testing.T) {
	// All's branches arrange and observe at once. gomock takes its controller
	// lock on both paths, so this is safe; the test pins that.
	stage := compose(t, t)
	audit := mokkit.Resolve[*shop.MockAudit](stage)

	const n = 16

	var wg sync.WaitGroup

	for i := range n {
		wg.Add(1)

		go func() {
			defer wg.Done()
			audit.EXPECT().Record(gomock.Any(), fmt.Sprintf("u-%d", i), gomock.Any()).Return(nil)
		}()
	}
	wg.Wait()

	for i := range n {
		wg.Add(1)

		go func() {
			defer wg.Done()

			if err := audit.Record(context.Background(), fmt.Sprintf("u-%d", i), 1); err != nil {
				t.Errorf("Record: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestAnErrorFromAMockSurfacesThroughTheChain(t *testing.T) {
	tb := &recordingTB{name: "inner"}
	stage := compose(t, tb)

	mokkit.Resolve[*shop.MockUserRepository](stage).EXPECT().
		ByID(gomock.Any(), "u-1").Return(shop.User{}, errors.New("user store unavailable"))

	runGoexit(func() {
		stage.Act().Add("CalculateDiscount", func(ctx context.Context, h mokkit.Host) error {
			_, err := h.Resolve[*shop.DiscountService]().Calculate(ctx, "u-1", 100)

			return err
		})
	})

	assertReported(t, tb.all(), "act: CalculateDiscount: user store unavailable")
}

func TestSatisfiedPassesOnceEveryExpectationIsMet(t *testing.T) {
	stage := compose(t, t)

	mokkit.Resolve[*shop.MockAudit](stage).EXPECT().
		Record(gomock.Any(), "u-1", 15.0).Return(nil).Times(1)

	if err := mokkit.Resolve[shop.Audit](stage).Record(context.Background(), "u-1", 15); err != nil {
		t.Fatalf("Record: %v", err)
	}

	stage.Inspect().And(mokkitgomock.Satisfied())

	if t.Failed() {
		t.Error("expected Satisfied to pass")
	}
}

func TestSatisfiedFailsAtTheInspectRatherThanAtCleanup(t *testing.T) {
	tb := &recordingTB{name: "inner"}
	stage := compose(t, tb)

	mokkit.Resolve[*shop.MockAudit](stage).EXPECT().
		Record(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(1)

	// Nothing calls it, so the chain reports here — before cleanup, and against
	// the caller rather than the EXPECT line inside a verb.
	stage.Inspect().And(mokkitgomock.Satisfied())

	assertReported(t, tb.all(), "inspect: gomock.Satisfied:", "have not all been met")
}

func TestRegisteringANonInterfacePanics(t *testing.T) {
	defer expectPanic(t, "is not an interface")

	b := mokkitgomock.New()
	mokkitgomock.Add[shop.User](b, shop.NewMockUserRepository)
}

func TestRegisteringAMockThatDoesNotImplementThePanics(t *testing.T) {
	defer expectPanic(t, "does not implement")

	b := mokkitgomock.New()
	mokkitgomock.Add[shop.RateRepository](b, shop.NewMockUserRepository)
}

func TestRegisteringTheSameInterfaceTwicePanics(t *testing.T) {
	defer expectPanic(t, "already registered")

	b := mokkitgomock.New()
	mokkitgomock.Add[shop.UserRepository](b, shop.NewMockUserRepository)
	mokkitgomock.Add[shop.UserRepository](b, shop.NewMockUserRepository)
}

func TestRegisteringAfterBuildPanics(t *testing.T) {
	b := mokkitgomock.New()
	if _, err := b.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}

	defer expectPanic(t, "after the container was built")

	mokkitgomock.Add[shop.UserRepository](b, shop.NewMockUserRepository)
}

func TestRegisteredReportsWhatIsThere(t *testing.T) {
	b := mokkitgomock.New()
	if mokkitgomock.Registered[shop.UserRepository](b) {
		t.Error("nothing registered yet")
	}

	mokkitgomock.Add[shop.UserRepository](b, shop.NewMockUserRepository)

	if !mokkitgomock.Registered[shop.UserRepository](b) {
		t.Error("expected UserRepository to be registered")
	}
}

// --- helpers -----------------------------------------------------------------

// recordingTB stands in for the test a stage belongs to, so failures the stage
// reports can be asserted on instead of failing the run. Fatalf and FailNow
// call runtime.Goexit exactly as *testing.T does, so anything that may fatal
// runs under runGoexit.
type recordingTB struct {
	name     string
	onFail   func()
	mu       sync.Mutex
	reports  []string
	cleanups []func()
	failed   bool
}

func (r *recordingTB) Helper()      {}
func (r *recordingTB) Name() string { return r.name }

func (r *recordingTB) Cleanup(fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.cleanups = append(r.cleanups, fn)
}

func (r *recordingTB) Failed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.failed
}

func (r *recordingTB) FailNow() {
	r.mark("")
	runtime.Goexit()
}

func (r *recordingTB) Errorf(format string, args ...any) { r.mark(fmt.Sprintf(format, args...)) }

func (r *recordingTB) Fatalf(format string, args ...any) {
	r.mark(fmt.Sprintf(format, args...))
	runtime.Goexit()
}

func (r *recordingTB) mark(msg string) {
	r.mu.Lock()
	if msg != "" {
		r.reports = append(r.reports, msg)
	}
	first := !r.failed
	r.failed = true
	r.mu.Unlock()

	if first && r.onFail != nil {
		r.onFail()
	}
}

func (r *recordingTB) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]string(nil), r.reports...)
}

// RunCleanups runs registered cleanups last-registered-first, as testing does.
func (r *recordingTB) RunCleanups() {
	r.mu.Lock()
	cleanups := append([]func(){}, r.cleanups...)
	r.cleanups = nil
	r.mu.Unlock()

	for i := len(cleanups) - 1; i >= 0; i-- {
		cleanups[i]()
	}
}

// runGoexit runs fn on its own goroutine, so a fatal reported through
// recordingTB unwinds that goroutine rather than killing the test.
func runGoexit(fn func()) {
	done := make(chan struct{})

	go func() {
		defer close(done)
		fn()
	}()
	<-done
}

func assertReported(t *testing.T, got []string, want ...string) {
	t.Helper()

	joined := strings.Join(got, "\n")
	for _, w := range want {
		if !strings.Contains(joined, w) {
			t.Errorf("expected a report containing %q, got:\n%s", w, joined)
		}
	}
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
