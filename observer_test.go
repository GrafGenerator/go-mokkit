package mokkit

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// recordingObserver captures the event stream, concurrency-safely, as the
// contract demands of real observers.
type recordingObserver struct {
	mu      sync.Mutex
	entered []string
	steps   []StepEvent
	closed  []string
	verdict map[string]bool
}

func newRecordingObserver() *recordingObserver {
	return &recordingObserver{verdict: map[string]bool{}}
}

func (r *recordingObserver) StageEntered(test, stageID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entered = append(r.entered, stageID)
}

func (r *recordingObserver) StepRan(event StepEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps = append(r.steps, event)
}

func (r *recordingObserver) StageClosed(test, stageID string, failed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = append(r.closed, stageID)
	r.verdict[stageID] = failed
}

func (r *recordingObserver) snapshot() (entered []string, steps []StepEvent, closed []string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]string(nil), r.entered...),
		append([]StepEvent(nil), r.steps...),
		append([]string(nil), r.closed...)
}

func observedStage(t *testing.T, tb TB, obs Observer) *Stage {
	t.Helper()

	c := newFakeContainer()
	register[Greeter](c, &recordingGreeter{})

	setup, err := NewSetup(context.Background(), c)
	if err != nil {
		t.Fatalf("NewSetup: %v", err)
	}
	setup.Observe(obs)

	return setup.EnterStage(tb)
}

func TestAnObserverHearsTheWholeStageStory(t *testing.T) {
	obs := newRecordingObserver()
	tb := newFakeTB(t.Name())
	stage := observedStage(t, tb, obs)

	stage.Arrange().Add("worldExists", func(context.Context, Host) error { return nil })
	stage.Inspect().Add("worldStillThere", func(context.Context, Host) error {
		return errors.New("it is gone")
	})

	tb.RunCleanups()

	entered, steps, closed := obs.snapshot()

	if len(entered) != 1 || len(closed) != 1 || entered[0] != closed[0] {
		t.Fatalf("want one stage entered and closed under one id, got entered=%v closed=%v", entered, closed)
	}

	if len(steps) != 2 {
		t.Fatalf("want 2 step events, got %d: %+v", len(steps), steps)
	}

	first, second := steps[0], steps[1]
	if first.Phase != "arrange" || first.Step != "worldExists" || first.Err != nil {
		t.Errorf("first event wrong: %+v", first)
	}
	if second.Phase != "inspect" || second.Step != "worldStillThere" || second.Err == nil {
		t.Errorf("second event wrong: %+v", second)
	}
	if first.Test != tb.Name() || first.StageID != entered[0] {
		t.Errorf("attribution wrong: %+v", first)
	}

	// The soft failure reached the test, and the verdict at close says so.
	if !obs.verdict[closed[0]] {
		t.Error("the stage closed with a failed test, and the observer must hear that")
	}
}

// A FailFast step Goexits the test goroutine — the observer must already have
// its event by then, or the most interesting step of a broken run is the one
// missing from the report.
func TestAFatalStepIsObservedBeforeTheChainDies(t *testing.T) {
	obs := newRecordingObserver()
	tb := newFakeTB(t.Name())
	stage := observedStage(t, tb, obs)

	runGoexit(func() {
		stage.Arrange().
			Add("breaks", func(context.Context, Host) error { return errors.New("boom") }).
			Add("neverRuns", func(context.Context, Host) error { return nil })
	})

	_, steps, _ := obs.snapshot()
	if len(steps) != 1 || steps[0].Step != "breaks" || steps[0].Err == nil {
		t.Fatalf("want exactly the fatal step observed, got %+v", steps)
	}
}

func TestAllBranchesAreEachObserved(t *testing.T) {
	obs := newRecordingObserver()
	tb := newFakeTB(t.Name())
	stage := observedStage(t, tb, obs)

	stage.Inspect().All(
		NewStep("left", func(context.Context, Host) error { return nil }),
		NewStep("right", func(context.Context, Host) error { return errors.New("askew") }),
	)

	_, steps, _ := obs.snapshot()
	if len(steps) != 2 {
		t.Fatalf("want one event per branch, got %+v", steps)
	}

	byName := map[string]error{}
	for _, e := range steps {
		byName[e.Step] = e.Err
	}
	if byName["left"] != nil || byName["right"] == nil {
		t.Errorf("branch outcomes wrong: %+v", byName)
	}
}

// A panic arrives as the event's error, wrapped the way the chain reports it,
// so a reporter can tell broken from failed.
func TestAPanickedStepIsObservedAsAPanicError(t *testing.T) {
	obs := newRecordingObserver()
	tb := newFakeTB(t.Name())
	stage := observedStage(t, tb, obs)

	runGoexit(func() {
		stage.Arrange().Add("crashes", func(context.Context, Host) error {
			panic("structural failure")
		})
	})

	_, steps, _ := obs.snapshot()
	if len(steps) != 1 {
		t.Fatalf("want the crashed step observed, got %+v", steps)
	}

	var panicked *PanicError
	if !errors.As(steps[0].Err, &panicked) || !strings.Contains(panicked.Error(), "structural failure") {
		t.Errorf("want a *PanicError carrying the panic, got %v", steps[0].Err)
	}
}

func TestAStageWithNoObserversEmitsNothingAndCostsNothing(t *testing.T) {
	tb := newFakeTB(t.Name())

	c := newFakeContainer()
	setup, err := NewSetup(context.Background(), c)
	if err != nil {
		t.Fatalf("NewSetup: %v", err)
	}

	stage := setup.EnterStage(tb)
	stage.Arrange().Add("quiet", func(context.Context, Host) error { return nil })
	tb.RunCleanups()
}
