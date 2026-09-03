package mokkit

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestAPanicStackNamesTheVocabularyAndNothingElse(t *testing.T) {
	stage := stageWith(t, newFakeTB(t.Name()), nil)

	err := stage.ex.Run(context.Background(), NewStep("probe", func(context.Context, Host) error {
		var rows []string
		//nolint:govet // the out-of-range index is the panic this test exists to catch.
		_ = rows[3]

		return nil
	}))

	var panicErr *PanicError
	if !errors.As(err, &panicErr) {
		t.Fatalf("expected a *PanicError, got %v", err)
	}

	stack := string(panicErr.Stack)
	if !strings.Contains(stack, "TestAPanicStackNamesTheVocabularyAndNothingElse") {
		t.Errorf("expected the panicking function in:\n%s", stack)
	}
	// None of mokkit's own plumbing, and none of the runtime's panic machinery.
	for _, unwanted := range []string{"inlineExecutor", "runtime.gopanic", "runtime.panicBounds", "debug.Stack"} {
		if strings.Contains(stack, unwanted) {
			t.Errorf("expected %q to be trimmed from:\n%s", unwanted, stack)
		}
	}
	if lines := strings.Count(strings.TrimSpace(stack), "\n") + 1; lines > 6 {
		t.Errorf("expected a short stack, got %d lines:\n%s", lines, stack)
	}
}

func TestAPanicErrorSaysItWasAPanicEvenWhenTheValueIsAnError(t *testing.T) {
	// A crash and a returned failure are different events, and a reader who
	// cannot tell them apart looks in the wrong place first.
	e := &PanicError{Value: errors.New("boom")}
	if got, want := e.Error(), "panic: boom"; got != want {
		t.Errorf("want %q, got %q", want, got)
	}
}

func TestADeepPanicReportsNoStackRatherThanAWallOfFrames(t *testing.T) {
	// Trimming works by finding the executor's own frame. When the stack
	// between the panic and it is deeper than the buffer, that frame is never
	// reached and everything gathered is untrimmed noise — so the report says
	// what happened and nothing else, which is the case the trimming exists to
	// prevent.
	stage := stageWith(t, newFakeTB(t.Name()), nil)

	err := stage.ex.Run(context.Background(), NewStep("probe", func(context.Context, Host) error {
		recurseThenPanic(600)

		return nil
	}))

	var panicErr *PanicError
	if !errors.As(err, &panicErr) {
		t.Fatalf("expected a *PanicError, got %v", err)
	}
	if len(panicErr.Stack) != 0 {
		t.Errorf("expected no stack at all, got:\n%s", panicErr.Stack)
	}
	if got, want := panicErr.Error(), "panic: too deep"; got != want {
		t.Errorf("want %q, got %q", want, got)
	}
}

// recurseThenPanic panics with more frames beneath it than panicStack collects.
// It is kept out of line so the recursion is real frames rather than a loop the
// compiler flattened.
//
//go:noinline
func recurseThenPanic(n int) {
	if n == 0 {
		panic("too deep")
	}

	recurseThenPanic(n - 1)
}
