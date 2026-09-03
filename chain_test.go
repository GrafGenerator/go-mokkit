package mokkit

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestChainRunsStepsEagerlyAndInOrder(t *testing.T) {
	stage := stageWith(t, newFakeTB(t.Name()), nil)

	var order []string
	stage.Arrange().
		Add("first", func(context.Context, Host) error {
			order = append(order, "first")

			return nil
		}).
		Add("second", func(context.Context, Host) error {
			order = append(order, "second")

			return nil
		})

	// No terminal call: both steps have already run by the time the chain
	// expression finishes.
	if got := len(order); got != 2 {
		t.Fatalf("expected 2 steps to have run, got %d (%v)", got, order)
	}
	if order[0] != "first" || order[1] != "second" {
		t.Errorf("expected in-order execution, got %v", order)
	}
}

func TestFailFastStopsTheRestOfTheChain(t *testing.T) {
	tb := newFakeTB(t.Name())
	stage := stageWith(t, tb, nil)

	ran := 0
	runGoexit(func() {
		stage.Arrange().
			Add("ok", func(context.Context, Host) error {
				ran++

				return nil
			}).
			Add("boom", func(context.Context, Host) error { return errors.New("no rate configured") }).
			Add("never", func(context.Context, Host) error {
				ran++

				return nil
			})
	})

	if ran != 1 {
		t.Errorf("expected the chain to stop at the failure, but %d steps ran", ran)
	}
	assertContains(t, tb.Fatals(), "arrange: boom: no rate configured")
	if got := len(tb.Errors()); got != 0 {
		t.Errorf("FailFast should not report through Errorf, got %d", got)
	}
}

func TestFailSoftReportsEveryFailure(t *testing.T) {
	tb := newFakeTB(t.Name())
	stage := stageWith(t, tb, nil)

	ran := 0
	stage.Inspect().
		Add("statusWrong", func(context.Context, Host) error { return errors.New("want 200, got 500") }).
		Add("bodyWrong", func(context.Context, Host) error { return errors.New("want Acme, got \"\"") }).
		Add("stillRuns", func(context.Context, Host) error {
			ran++

			return nil
		})

	if ran != 1 {
		t.Errorf("expected the chain to continue past failures, ran=%d", ran)
	}
	// The point of soft-failing Inspect: one run tells you everything that is
	// wrong, not just the first thing.
	assertContains(t, tb.Errors(),
		"inspect: statusWrong: want 200, got 500",
		"inspect: bodyWrong: want Acme",
	)
	if got := len(tb.Fatals()); got != 0 {
		t.Errorf("FailSoft should not fatal, got %d", got)
	}
}

func TestDoRunsForeignVocabularyAndNamesItFromTheRuntime(t *testing.T) {
	tb := newFakeTB(t.Name())
	stage := stageWith(t, tb, nil)

	runGoexit(func() {
		stage.Arrange().And(cacheIsUnreachable())
	})

	// A step authored in another package reports under the name that package
	// gave it, not as an anonymous closure and not as its caller.
	assertContains(t, tb.Fatals(), "arrange: cache.IsUnreachable: connection refused")
}

// cacheIsUnreachable stands in for vocabulary from a package that cannot add
// methods to this chain's type.
func cacheIsUnreachable() Step {
	return NewStep("cache.IsUnreachable", func(context.Context, Host) error {
		return errors.New("connection refused")
	})
}

func TestAllRunsConcurrentlyButOrdersAgainstNeighbours(t *testing.T) {
	stage := stageWith(t, newFakeTB(t.Name()), nil)

	var mu sync.Mutex
	var seq []int
	note := func(n int) StepFunc {
		return func(context.Context, Host) error {
			mu.Lock()
			defer mu.Unlock()
			seq = append(seq, n)

			return nil
		}
	}

	// Both members of the group must have started before either may finish,
	// which deadlocks unless All is genuinely concurrent.
	var wg sync.WaitGroup
	wg.Add(2)
	rendezvous := func(n int) Step {
		return NewStep(fmt.Sprintf("branch%d", n), func(ctx context.Context, h Host) error {
			wg.Done()
			wg.Wait()

			return note(n)(ctx, h)
		})
	}

	stage.Inspect().
		Add("before", note(1)).
		All(rendezvous(2), rendezvous(3)).
		Add("after", note(4))

	if len(seq) != 4 {
		t.Fatalf("expected 4 steps, got %v", seq)
	}
	if seq[0] != 1 {
		t.Errorf("expected the pre-group step first, got %v", seq)
	}
	if seq[3] != 4 {
		t.Errorf("expected the post-group step last, got %v", seq)
	}
	if seq[1]+seq[2] != 5 {
		t.Errorf("expected the group members in the middle, got %v", seq)
	}
}

func TestAllReportsEveryFailureUnderFailSoft(t *testing.T) {
	tb := newFakeTB(t.Name())
	stage := stageWith(t, tb, nil)

	stage.Inspect().All(
		NewStep("dbRowExists", func(context.Context, Host) error { return errors.New("db row missing") }),
		NewStep("cacheWarmed", func(context.Context, Host) error { return nil }),
		NewStep("eventPublished", func(context.Context, Host) error { return errors.New("event not published") }),
	)

	assertContains(t, tb.Errors(), "inspect: dbRowExists:", "inspect: eventPublished:")

	assertContains(t, tb.Errors(), "db row missing", "event not published")
	if got := len(tb.Errors()); got != 2 {
		t.Errorf("expected exactly 2 reports, got %d: %v", got, tb.Errors())
	}
}

func TestPanicInAStepIsReportedAgainstThatStep(t *testing.T) {
	tb := newFakeTB(t.Name())
	stage := stageWith(t, tb, nil)

	runGoexit(func() {
		stage.Arrange().Add("greeterIsReady", func(_ context.Context, h Host) error {
			// Nothing registered Greeter, so Resolve panics. The executor turns
			// that into a failure attributed to this verb.
			_ = Resolve[Greeter](h)

			return nil
		})
	})

	assertContains(t, tb.Fatals(),
		"arrange: greeterIsReady:",
		"no service registered as mokkit.Greeter",
	)
}

func TestPanicErrorUnwrapsToTheOriginalError(t *testing.T) {
	stage := stageWith(t, newFakeTB(t.Name()), nil)

	err := stage.ex.Run(context.Background(), NewStep("probe", func(_ context.Context, h Host) error {
		_ = Resolve[Greeter](h)

		return nil
	}))

	var resolveErr *ResolveError
	if !errors.As(err, &resolveErr) {
		t.Fatalf("expected a *ResolveError inside the panic, got %v", err)
	}
	if resolveErr.Type != typeOf[Greeter]() {
		t.Errorf("expected the missing type to be Greeter, got %s", resolveErr.Type)
	}
}

func TestFatalFromANestedHelperIsNotSwallowed(t *testing.T) {
	tb := newFakeTB(t.Name())
	stage := stageWith(t, tb, nil)

	after := false
	runGoexit(func() {
		stage.Arrange().
			Add("assertsInline", func(context.Context, Host) error {
				// Vocabulary is free to assert with t directly. Fatalf unwinds
				// via Goexit, which recover() must not turn into a step error.
				tb.Fatalf("expected a VIP user")

				return nil
			}).
			Add("never", func(context.Context, Host) error {
				after = true

				return nil
			})
	})

	if after {
		t.Error("a t.Fatal inside a step must unwind the chain, not be recovered")
	}
	assertContains(t, tb.Fatals(), "expected a VIP user")
	if got := len(tb.Fatals()); got != 1 {
		t.Errorf("expected only the helper's own report, got %v", tb.Fatals())
	}
}

func TestAllBranchesEachFinishRegardlessOfTheOthers(t *testing.T) {
	// The reason C# grew branch builders: independent checks must all run and
	// all report, so one run tells you everything that is wrong. This pins that
	// the ...Step form delivers it.
	tb := newFakeTB(t.Name())
	stage := stageWith(t, tb, nil)

	var mu sync.Mutex
	finished := map[string]bool{}
	branch := func(name string, fail bool) Step {
		return NewStep(name, func(context.Context, Host) error {
			mu.Lock()
			finished[name] = true
			mu.Unlock()
			if fail {
				return errors.New(name + " is wrong")
			}

			return nil
		})
	}

	stage.Inspect().All(
		branch("apiClient", true),
		branch("dbClient", true),
		branch("eventPublished", false),
	)

	for _, name := range []string{"apiClient", "dbClient", "eventPublished"} {
		if !finished[name] {
			t.Errorf("branch %s did not run", name)
		}
	}
	assertContains(t, tb.Errors(),
		"inspect: apiClient: apiClient is wrong",
		"inspect: dbClient: dbClient is wrong",
	)
	if got := len(tb.Errors()); got != 2 {
		t.Errorf("expected both failures reported, got %d: %v", got, tb.Errors())
	}
}

func TestGroupMakesABranchOutOfSeveralSteps(t *testing.T) {
	tb := newFakeTB(t.Name())
	stage := stageWith(t, tb, nil)

	var mu sync.Mutex
	var ran []string
	step := func(name string, fail bool) Step {
		return NewStep(name, func(context.Context, Host) error {
			mu.Lock()
			ran = append(ran, name)
			mu.Unlock()
			if fail {
				return errors.New("no row")
			}

			return nil
		})
	}

	stage.Inspect().All(
		Group("db", step("dbRow", true), step("dbIndex", false)),
		Group("api", step("apiGet", false), step("apiList", false)),
	)

	// Within a branch, steps run in order and stop at the first failure — the
	// same as a C# branch, where the exception ends that branch only.
	if got := len(ran); got != 3 {
		t.Errorf("expected dbIndex to be skipped after dbRow failed, ran %v", ran)
	}
	// The other branch still completed.
	assertContains(t, tb.Errors(), "inspect: db: dbRow: no row")
}

// --- vocabulary authored the way the docs say --------------------------------

// arrange is a vocabulary type: a *Chain embedded in your own type, with verbs
// hung on it. Its verbs are generic over the token, so the role is spelled once
// at the call site and carried into the step label.
type arrange struct{ *Chain }

// UserExists is the producing form. It takes no sink parameter — the chain
// stays whole because the artifact is filed under the token instead of being
// assigned out of the middle of the expression.
func (a arrange) UserExists[K Token[User]](status string) arrange {
	a.Helper()
	a.Add("UserExists["+NameOf[K]()+"]", func(context.Context, Host) error {
		*a.New[K]() = User{ID: NameOf[K](), Status: status}

		return nil
	})

	return a
}

// Greets is the consuming form. It reads the artifact back through the token
// and resolves its collaborator off the host, which is what a verb normally
// wants instead of the free Resolve.
func (a arrange) Greets[K Token[User]]() arrange {
	a.Helper()
	a.Add("Greets["+NameOf[K]()+"]", func(_ context.Context, h Host) error {
		h.Resolve[Greeter]().Greet(a.Of[K]().ID)

		return nil
	})

	return a
}

// WithContext is the forwarder every vocabulary type writes. It reads exactly
// like the And and All ones because the chain mutates in place.
func (a arrange) WithContext(ctx context.Context) arrange {
	a.Helper()
	a.Chain.WithContext(ctx)

	return a
}

func TestAVerbProducesAndReadsItsArtifactThroughTheToken(t *testing.T) {
	greeter := &recordingGreeter{}
	stage := stageWith(t, newFakeTB(t.Name()), greeter)

	arrange{stage.Arrange()}.
		UserExists[Buyer]("vip").
		UserExists[Seller]("plain").
		Greets[Seller]().
		Greets[Buyer]()

	// Both roles survive the chain, and each verb acted for the one it named.
	if got := greeter.Calls(); len(got) != 2 || got[0] != "Seller" || got[1] != "Buyer" {
		t.Errorf("expected each verb to act for the role it named, got %v", got)
	}
	if got := stage.Tokens().Of[Buyer]().Status; got != "vip" {
		t.Errorf("expected the buyer's status to survive, got %q", got)
	}
}

// --- the reporter is a field, not an embedding -------------------------------

func TestTheTestReporterIsNotPromotedOntoVocabulary(t *testing.T) {
	// Promoting TB let a verb report around the chain: inside an All branch,
	// t.Fatalf's Goexit kills only the branch goroutine, the failure loses its
	// phase and verb prefix, and a FailFast chain carries on. These are compile
	// errors now — a.Errorf("...") does not build — and this pins that the
	// method set really is missing them rather than the call merely being
	// unfashionable.
	for _, typ := range []reflect.Type{typeOf[*Chain](), typeOf[arrange]()} {
		for _, name := range []string{"Errorf", "Fatalf", "FailNow", "Failed", "Name", "Cleanup"} {
			if _, ok := typ.MethodByName(name); ok {
				t.Errorf("%s must not expose %s", typ, name)
			}
		}
		for _, name := range []string{"Helper", "TB"} {
			if _, ok := typ.MethodByName(name); !ok {
				t.Errorf("%s must expose %s", typ, name)
			}
		}
	}
}

func TestChainTBIsTheStagesReporter(t *testing.T) {
	// The one legitimate reason to reach the reporter is handing it to an
	// assertion library, and what it hands over must be the test's own.
	tb := newFakeTB(t.Name())
	stage := stageWith(t, tb, nil)

	if got := stage.Arrange().TB(); got != TB(tb) {
		t.Errorf("expected the stage's reporter, got %#v", got)
	}
}

// --- WithContext mutates -----------------------------------------------------

type ctxKey struct{}

func TestWithContextAffectsLaterStepsOnTheSameChain(t *testing.T) {
	stage := stageWith(t, newFakeTB(t.Name()), nil)

	c := stage.Arrange()
	if got := c.WithContext(context.WithValue(context.Background(), ctxKey{}, "late")); got != c {
		t.Error("WithContext must return the receiver, as And and All do")
	}

	var fromStep, fromHost any
	c.Add("reads", func(ctx context.Context, h Host) error {
		fromStep, fromHost = ctx.Value(ctxKey{}), h.Context().Value(ctxKey{})

		return nil
	})

	if fromStep != "late" || fromHost != "late" {
		t.Errorf("expected the new context in the step and its host, got %v and %v", fromStep, fromHost)
	}
	if got := c.Context().Value(ctxKey{}); got != "late" {
		t.Errorf("expected the chain to report the new context, got %v", got)
	}
}

func TestWithContextThroughAVocabularyForwarderAffectsTheChainItWasCalledOn(t *testing.T) {
	// The copy-constructor form made this the trap it is named for: the
	// forwarder mutated its copy, the caller kept the old chain, and the
	// context silently did not apply.
	stage := stageWith(t, newFakeTB(t.Name()), &recordingGreeter{})

	a := arrange{stage.Arrange()}
	a.WithContext(context.WithValue(context.Background(), ctxKey{}, "forwarded"))

	var seen any
	a.Add("reads", func(ctx context.Context, _ Host) error {
		seen = ctx.Value(ctxKey{})

		return nil
	})

	if seen != "forwarded" {
		t.Errorf("expected the forwarder to affect the chain it was called on, got %v", seen)
	}
}

func TestWithContextDoesNotReachStepsAlreadyRun(t *testing.T) {
	stage := stageWith(t, newFakeTB(t.Name()), nil)

	var before any
	stage.Arrange().
		Add("early", func(ctx context.Context, _ Host) error {
			before = ctx.Value(ctxKey{})

			return nil
		}).
		WithContext(context.WithValue(context.Background(), ctxKey{}, "late"))

	if before != nil {
		t.Errorf("a step that already ran cannot have seen a later context, got %v", before)
	}
}

// --- All reports its branches by name ----------------------------------------

func TestAllReportsAFailingBranchByItsStepNameUnderFailFast(t *testing.T) {
	tb := newFakeTB(t.Name())
	stage := stageWith(t, tb, nil)

	runGoexit(func() {
		stage.Arrange().
			All(
				NewStep("cacheWarmed", func(context.Context, Host) error { return nil }),
				NewStep("queueBound", func(context.Context, Host) error { return errors.New("no such queue") }),
			).
			Add("never", func(context.Context, Host) error {
				t.Error("a failed group must end a FailFast chain")

				return nil
			})
	})

	// The branch failed by returning, not by reporting, so the failure still
	// carries its phase and verb.
	assertContains(t, tb.Fatals(), "arrange: queueBound: no such queue")
}

func TestAnUnnamedBranchReportsItsPosition(t *testing.T) {
	tb := newFakeTB(t.Name())
	stage := stageWith(t, tb, nil)

	stage.Inspect().All(
		Step{Run: func(context.Context, Host) error { return nil }},
		Step{Run: func(context.Context, Host) error { return errors.New("nothing was published") }},
	)

	assertContains(t, tb.Errors(), "inspect: step[1]: nothing was published")
}

// --- attribution -------------------------------------------------------------

// helperCallerTB records which function called Helper — the mechanism behind a
// failure reporting the test's line instead of the verb's body. A fake whose
// Helper is a no-op cannot see this, so it is asserted on directly.
type helperCallerTB struct {
	*fakeTB

	mu      sync.Mutex
	callers []string
}

func newHelperCallerTB(name string) *helperCallerTB {
	return &helperCallerTB{fakeTB: newFakeTB(name)}
}

func (h *helperCallerTB) Helper() {
	pc, _, _, ok := runtime.Caller(1)
	if !ok {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	h.callers = append(h.callers, runtime.FuncForPC(pc).Name())
}

func (h *helperCallerTB) Callers() []string {
	h.mu.Lock()
	defer h.mu.Unlock()

	return append([]string(nil), h.callers...)
}

// markSelf is a verb written the documented way: its first line is a.Helper().
func (a arrange) markSelf() arrange {
	a.Helper()

	return a
}

// TestAVerbMarksItsOwnFrameNotTheChains is the regression test for the one
// thing that makes this library worth using. testing.T.Helper records the frame
// of its immediate caller, so a.Helper() must resolve to a call whose caller is
// the VERB. An embedded interface gives that, because the compiler-generated
// promoted wrapper is elided; a hand-written forwarding method does not, and
// silently moves every failure in every suite to chain.go.
func TestAVerbMarksItsOwnFrameNotTheChains(t *testing.T) {
	tb := newHelperCallerTB(t.Name())
	stage := stageWith(t, tb, &recordingGreeter{})

	arrange{stage.Arrange()}.markSelf()

	var marked bool
	for _, caller := range tb.Callers() {
		if strings.HasSuffix(caller, ".arrange.markSelf") {
			marked = true
		}
		if strings.HasSuffix(caller, "(*Chain).Helper") {
			t.Errorf("Helper recorded the chain's own frame (%s); a verb's Helper must record the verb", caller)
		}
	}

	if !marked {
		t.Errorf("the verb's own frame was never marked as a helper, got callers %v", tb.Callers())
	}
}
