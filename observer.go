package mokkit

import "time"

// A StepEvent describes one step that ran: which test asked for it, the phase
// and verb the reader wrote, how long it took, and how it ended. Err is nil for
// a step that passed, the step's own error for one that failed, and a
// *PanicError for one that crashed — a reporter that wants to distinguish
// "failed" from "broken" branches on that.
type StepEvent struct {
	Test     string
	StageID  string
	Phase    string
	Step     string
	Started  time.Time
	Duration time.Duration
	Err      error
}

// An Observer hears what a composition's stages do: a stage entered per test,
// every step that ran, and the stage closing with the test's verdict. It is the
// seam a reporter plugs into — the steps arrive already carrying the vocabulary
// names the suite was written in, so a report reads as the scenario did.
//
// Implementations must be safe for concurrent use: All runs its branches on
// their own goroutines, and suites may run tests in parallel. Events for one
// stage may therefore interleave with another's; StageID is what groups them.
//
// An Observer must not fail the test.
type Observer interface {
	// StageEntered reports a test beginning to run against the composition.
	StageEntered(test, stageID string)

	// StepRan reports one step, after it finished.
	StepRan(event StepEvent)

	// StageClosed reports the stage releasing its scopes. failed is the test's
	// verdict at that moment — soft failures included, since cleanup runs after
	// the test body.
	StageClosed(test, stageID string, failed bool)
}

// Observe registers observers for every stage later entered from this setup.
// Call it once, where the composition is built:
//
//	setup, err := mokkit.NewSetup(ctx, mocks, app)
//	...
//	setup.Observe(allure.New(resultsDir))
//
// It returns the setup, so it chains. Observing after stages have been entered
// affects only stages entered afterwards.
func (s *Setup) Observe(observers ...Observer) *Setup {
	s.observers = append(s.observers, observers...)

	return s
}

// emitStep hands one finished step to every observer.
func emitStep(observers []Observer, event StepEvent) {
	for _, o := range observers {
		o.StepRan(event)
	}
}
