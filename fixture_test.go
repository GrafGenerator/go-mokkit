package mokkit

import (
	"context"
	"errors"
	"testing"
)

type fixture = Fixture[arranging, acting, inspecting]

type brokenBuilder struct{}

func (brokenBuilder) Build(context.Context) (Container, error) {
	return nil, errors.New("the broker is down")
}

func greeterContainer() *fakeContainer {
	g := &recordingGreeter{}
	c := newFakeContainer()
	register[Greeter](c, g)
	register(c, g)

	return c
}

func TestEnterComposesAndTypesThePhases(t *testing.T) {
	f := Enter[arranging, acting, inspecting](t, greeterContainer())

	f.Arrange().UserExists[role]("vip")
	greeting := f.Act().Greet(f.Of[role]().ID)
	f.Inspect().Greeted("role")

	if greeting != "hello role" {
		t.Errorf("want the act's artifact, got %q", greeting)
	}
	if f.Stage == nil || f.Stage.TB() != TB(t) {
		t.Error("the fixture's stage belongs to the test that entered it")
	}
}

func TestEnterFailsTheTestWhenAContainerCannotBeBuilt(t *testing.T) {
	tb := newFakeTB("TestEnterFails")

	var f *fixture
	runGoexit(func() { f = Enter[arranging, acting, inspecting](tb, brokenBuilder{}) })

	if f != nil {
		t.Error("a fixture must not be handed back over a composition that failed")
	}
	assertContains(t, tb.Fatals(), "building container 0", "the broker is down")
}

func TestSetupEnterSharesTheCompositionBetweenStages(t *testing.T) {
	setup, err := NewSetup(context.Background(), greeterContainer())
	if err != nil {
		t.Fatalf("NewSetup: %v", err)
	}

	first := setup.Enter[arranging, acting, inspecting](t)
	second := setup.Enter[arranging, acting, inspecting](t)

	first.Arrange().UserExists[role]("vip")

	if second.Declared[role]() {
		t.Error("artifacts must not leak between the stages of one composition")
	}
	if first.Stage.ID() == second.Stage.ID() {
		t.Error("each Enter opens its own stage")
	}
}

func TestEnterContextRunsStepsUnderTheContext(t *testing.T) {
	type key struct{}

	ctx := context.WithValue(context.Background(), key{}, "acme")
	f := EnterContext[arranging, acting, inspecting](ctx, t, greeterContainer())

	var seen any
	Do(f.Arrange(), func(ctx context.Context, _ Host) error {
		seen = ctx.Value(key{})

		return nil
	})

	if seen != "acme" {
		t.Errorf("want the entered context on the step, got %v", seen)
	}
}
