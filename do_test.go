package mokkit

import (
	"context"
	"errors"
	"testing"
)

// A vocabulary written with Do, Get and Try, from inside the package so the
// reporter can be faked.
type (
	arranging  struct{ *Chain }
	acting     struct{ *Chain }
	inspecting struct{ *Chain }
)

type role struct{ Artifact[User] }

func (a arranging) UserExists[K Token[User]](status string) arranging {
	a.Helper()

	return DoFor[K](a, func(h Host) {
		*a.New[K]() = User{ID: NameOf[K](), Status: status}
	})
}

func (a arranging) GreeterIsNamed(name string) arranging {
	a.Helper()

	return DoAs(a, "greeter.Named("+name+")", func(Host) {})
}

func (a arranging) GreeterIsSilent() arranging {
	a.Helper()

	return Do(a, func(Host) error { return errors.New("the greeter cannot be silenced") })
}

func (a arranging) GreeterSeesContext() arranging {
	a.Helper()

	return Do(a, func(ctx context.Context, _ Host) error {
		if ctx == nil {
			return errors.New("no context")
		}

		return nil
	})
}

// greeterSpeaks stands in for a step published by another package.
func greeterSpeaks(name string) Step {
	return NewStep("greeter.Speaks("+name+")", func(_ context.Context, h Host) error {
		h.Resolve[Greeter]().Greet(name)

		return nil
	})
}

func (a arranging) GreeterSpoke(name string) arranging {
	a.Helper()

	return Do(a, greeterSpeaks(name))
}

func (a acting) Greet(name string) string {
	a.Helper()

	return a.Get(func(h Host) (string, error) {
		return h.Resolve[Greeter]().Greet(name), nil
	})
}

func (a acting) GreetNobody() string {
	a.Helper()

	return a.Get(func(Host) (string, error) { return "", errors.New("nobody to greet") })
}

func (a acting) TryGreet(name string) Outcome[string] {
	a.Helper()

	return a.Try(func(h Host) (string, error) {
		if name == "" {
			return "", errors.New("nobody to greet")
		}

		return h.Resolve[Greeter]().Greet(name), nil
	})
}

func (a acting) GreetFor[K Token[User]]() string {
	a.Helper()

	return a.GetFor[K](func(h Host) (string, error) {
		return h.Resolve[Greeter]().Greet(a.Of[K]().ID), nil
	})
}

func (a acting) TryGreetAs(name string) Outcome[string] {
	a.Helper()

	return a.TryAs("greeter.Try("+name+")", func(h Host) (string, error) {
		return h.Resolve[Greeter]().Greet(name), nil
	})
}

func (a acting) TryPanicking() Outcome[string] {
	a.Helper()

	return a.Try(func(Host) (string, error) { panic("boom") })
}

func (i inspecting) Greeted(want string) inspecting {
	i.Helper()

	return Do(i, func(h Host) error {
		got := h.Resolve[*recordingGreeter]().Calls()
		if len(got) != 1 || got[0] != want {
			return errors.New("want a greeting of " + want)
		}

		return nil
	})
}

func doStage(t *testing.T, tb TB) (*Stage, *recordingGreeter) {
	t.Helper()

	g := &recordingGreeter{}
	c := newFakeContainer()
	register[Greeter](c, g)
	register(c, g)

	setup, err := NewSetup(context.Background(), c)
	if err != nil {
		t.Fatalf("NewSetup: %v", err)
	}

	return setup.EnterStageContext(context.Background(), tb), g
}

func TestDoKeepsTheVocabularyTypeAndRunsTheStep(t *testing.T) {
	stage, g := doStage(t, t)

	a := arranging{stage.Arrange()}.UserExists[role]("vip")
	if a.Chain == nil {
		t.Fatal("Do must hand the phase back")
	}
	if got := stage.Tokens().Of[role](); got.Status != "vip" {
		t.Errorf("the step did not run: %+v", got)
	}

	acting{stage.Act()}.Greet("ann")
	if calls := g.Calls(); len(calls) != 1 || calls[0] != "ann" {
		t.Errorf("Get did not run the step: %v", calls)
	}
}

func TestDoNamesTheStepAfterTheVerb(t *testing.T) {
	tb := newFakeTB("TestDoNames")

	runGoexit(func() {
		stage, _ := doStage(t, tb)
		arranging{stage.Arrange()}.GreeterIsSilent()
	})

	assertContains(t, tb.Fatals(), "arrange: GreeterIsSilent: the greeter cannot be silenced")
}

func TestDoAppendsTheRoleToTheName(t *testing.T) {
	tb := newFakeTB("TestDoRole")

	obs := newRecordingObserver()
	runGoexit(func() {
		stage, _ := doStage(t, tb)
		stage.observers = []Observer{obs}
		arranging{stage.Arrange()}.UserExists[role]("vip")
	})

	_, steps, _ := obs.snapshot()
	if len(steps) != 1 || steps[0].Step != "UserExists[role]" {
		t.Errorf("want one step labeled UserExists[role], got %+v", steps)
	}
}

func TestDoAcceptsEveryBodyShape(t *testing.T) {
	stage, g := doStage(t, t)

	arranging{stage.Arrange()}.
		GreeterSeesContext().
		GreeterSpoke("eve")

	if calls := g.Calls(); len(calls) != 1 || calls[0] != "eve" {
		t.Errorf("a Step body must run like any other: %v", calls)
	}
}

func TestTheAsAndForFormsNameTheStep(t *testing.T) {
	obs := newRecordingObserver()
	stage, _ := doStage(t, t)
	stage.observers = []Observer{obs}

	arranging{stage.Arrange()}.
		UserExists[role]("vip").
		GreeterIsNamed("ann")
	acting{stage.Act()}.GreetFor[role]()
	acting{stage.Act()}.TryGreetAs("bob")
	acting{stage.Act()}.GetAs("greeter.Get", func(Host) (string, error) { return "", nil })

	_, steps, _ := obs.snapshot()

	want := []string{"UserExists[role]", "greeter.Named(ann)", "GreetFor[role]", "greeter.Try(bob)", "greeter.Get"}
	for n, w := range want {
		if n >= len(steps) || steps[n].Step != w {
			t.Errorf("step %d: want %q, got %+v", n, w, steps)
		}
	}
}

func TestDoRunsAStepUnderItsOwnName(t *testing.T) {
	obs := newRecordingObserver()
	stage, _ := doStage(t, t)
	stage.observers = []Observer{obs}

	arranging{stage.Arrange()}.GreeterSpoke("eve")

	_, steps, _ := obs.snapshot()
	if len(steps) != 1 || steps[0].Step != "greeter.Speaks(eve)" {
		t.Errorf("want the step's own name, got %+v", steps)
	}
}

func TestGetFailsTheChainOnError(t *testing.T) {
	tb := newFakeTB("TestGetFails")

	reached := false
	runGoexit(func() {
		stage, _ := doStage(t, tb)
		acting{stage.Act()}.GreetNobody()
		reached = true
	})

	if reached {
		t.Error("Get on a fail-fast chain must not return after an error")
	}
	assertContains(t, tb.Fatals(), "act: GreetNobody: nobody to greet")
}

func TestTryHandsBackTheOutcomeInsteadOfFailing(t *testing.T) {
	stage, _ := doStage(t, t)

	refused := acting{stage.Act()}.TryGreet("")
	if refused.Err == nil || refused.Err.Error() != "nobody to greet" {
		t.Errorf("want the refusal as the outcome, got %+v", refused)
	}

	greeted := acting{stage.Act()}.TryGreet("bob")
	if greeted.Err != nil || greeted.Value != "hello bob" {
		t.Errorf("want the greeting as the outcome, got %+v", greeted)
	}

	inspecting{stage.Inspect()}.Greeted("bob")
}

func TestTryStillFailsOnAPanic(t *testing.T) {
	tb := newFakeTB("TestTryPanics")

	runGoexit(func() {
		stage, _ := doStage(t, tb)
		acting{stage.Act()}.TryPanicking()
	})

	assertContains(t, tb.Fatals(), "act: TryPanicking: panic: boom")
}

func TestBareNameDropsEverythingButTheFunction(t *testing.T) {
	cases := map[string]string{
		"example.com/app.Arrange.UserExists.func1":                       "UserExists",
		"example.com/app.Arrange.UserExists[go.shape.struct {}].func1.2": "UserExists",
		"example.com/app.(*Probe).reset":                                 "reset",
		"example.com/app.(*Probe).reset-fm":                              "reset",
		"example.com/app.TestSomething.func3":                            "TestSomething",
		"app.HasClient":                                                  "HasClient",
		"main.main":                                                      "main",
		"":                                                               "",
	}
	for symbol, want := range cases {
		if got := bareName(symbol); got != want {
			t.Errorf("bareName(%q) = %q, want %q", symbol, got, want)
		}
	}
}
