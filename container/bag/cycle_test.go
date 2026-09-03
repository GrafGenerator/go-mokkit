package bag_test

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GrafGenerator/go-mokkit"
	"github.com/GrafGenerator/go-mokkit/container/bag"
)

type nodeA struct{ B *nodeB }

type nodeB struct{ A *nodeA }

func TestADependencyCycleIsReportedNotOverflowed(t *testing.T) {
	b := bag.New()
	bag.Scoped(b, func(r mokkit.Resolver) *nodeA { return &nodeA{B: mokkit.Resolve[*nodeB](r)} })
	bag.Scoped(b, func(r mokkit.Resolver) *nodeB { return &nodeB{A: mokkit.Resolve[*nodeA](r)} })

	stage := setupWith(t, b).EnterStage(t)

	err := cycleFrom(t, func() { mokkit.Resolve[*nodeA](stage) })

	// The path reads as the construction chain — what was being built, what it
	// asked for, and where that came back round — so the offending factory is
	// the one named just before the repeat.
	want := "*bag_test.nodeA -> *bag_test.nodeB -> *bag_test.nodeA"
	if got := pathNames(err.Path); got != want {
		t.Errorf("expected the path %q, got %q", want, got)
	}
	if got := err.Error(); !strings.Contains(got, "bag: dependency cycle: "+want) {
		t.Errorf("expected the cycle path in %q", got)
	}
}

func TestACycleThatCrossesContainersIsReportedNotDeadlocked(t *testing.T) {
	// Each container holds one half of the loop, so neither can see it alone:
	// the construction path has to survive the hop through the stage. Without
	// that, the second container blocks on an entry lock the first is still
	// holding and the test hangs instead of failing.
	first := bag.New()
	bag.Scoped(first, func(r mokkit.Resolver) *nodeA { return &nodeA{B: mokkit.Resolve[*nodeB](r)} })

	second := bag.New()
	bag.Scoped(second, func(r mokkit.Resolver) *nodeB { return &nodeB{A: mokkit.Resolve[*nodeA](r)} })

	stage := setupWith(t, first, second).EnterStage(t)

	err := cycleFrom(t, func() { mokkit.Resolve[*nodeA](stage) })

	want := "*bag_test.nodeA -> *bag_test.nodeB -> *bag_test.nodeA"
	if got := pathNames(err.Path); got != want {
		t.Errorf("expected a path naming both containers' halves, %q, got %q", want, got)
	}
}

func TestACycleInsideAStepFailsTheStepRatherThanTheProcess(t *testing.T) {
	b := bag.New()
	bag.Scoped(b, func(r mokkit.Resolver) *nodeA { return &nodeA{B: mokkit.Resolve[*nodeB](r)} })
	bag.Scoped(b, func(r mokkit.Resolver) *nodeB { return &nodeB{A: mokkit.Resolve[*nodeA](r)} })

	// A stage reports through mokkit.TB, so a fake one captures what a real
	// test would have been told.
	rec := &recordingTB{name: t.Name()}
	stage := setupWith(t, b).EnterStage(rec)

	runGoexit(func() {
		stage.Arrange().Add("serviceIsWired", func(_ context.Context, h mokkit.Host) error {
			h.Resolve[*nodeA]()

			return nil
		})
	})

	// The executor turns the panic into a failure attributed to the step,
	// rather than taking the process down.
	fatals := rec.Fatals()
	if len(fatals) != 1 {
		t.Fatalf("expected one reported failure, got %v", fatals)
	}
	for _, want := range []string{"arrange: serviceIsWired:", "panic: ", "dependency cycle", "nodeA -> *bag_test.nodeB"} {
		if !strings.Contains(fatals[0], want) {
			t.Errorf("expected %q in:\n%s", want, fatals[0])
		}
	}
}

// The graph below forks: hub builds two collaborators at once, and each of them
// reaches one more that closes the loop back onto hub. Both branches therefore
// report a cycle, and each must report its own — the paths are grown from the
// same parent slice, so sharing one backing array would let the branch that
// appends second overwrite the tail the first is about to be blamed for.
type app struct{ Core *core }

type core struct{ Hub *hub }

type hub struct {
	Left  *leftArm
	Right *rightArm
}

type leftArm struct{ Cycle *bag.CycleError }

type rightArm struct{ Cycle *bag.CycleError }

type leftHand struct{ Hub *hub }

type rightHand struct{ Hub *hub }

func TestSiblingResolvesDoNotShareAPath(t *testing.T) {
	b := bag.New()
	bag.Scoped(b, func(r mokkit.Resolver) *app { return &app{Core: mokkit.Resolve[*core](r)} })
	bag.Scoped(b, func(r mokkit.Resolver) *core { return &core{Hub: mokkit.Resolve[*hub](r)} })
	bag.Scoped(b, func(r mokkit.Resolver) *hub {
		var h hub

		var wg sync.WaitGroup
		wg.Add(2)

		go func() { defer wg.Done(); h.Left = mokkit.Resolve[*leftArm](r) }()
		go func() { defer wg.Done(); h.Right = mokkit.Resolve[*rightArm](r) }()
		wg.Wait()

		return &h
	})

	// Each arm swallows its own cycle, so both branches finish and the test can
	// compare the two paths rather than only the one that panicked first.
	bag.Scoped(b, func(r mokkit.Resolver) (a *leftArm) {
		defer func() { a = &leftArm{Cycle: recoveredCycle(recover())} }()
		mokkit.Resolve[*leftHand](r)

		return nil
	})
	bag.Scoped(b, func(r mokkit.Resolver) (a *rightArm) {
		defer func() { a = &rightArm{Cycle: recoveredCycle(recover())} }()
		mokkit.Resolve[*rightHand](r)

		return nil
	})
	bag.Scoped(b, func(r mokkit.Resolver) *leftHand { return &leftHand{Hub: mokkit.Resolve[*hub](r)} })
	bag.Scoped(b, func(r mokkit.Resolver) *rightHand { return &rightHand{Hub: mokkit.Resolve[*hub](r)} })

	stage := setupWith(t, b).EnterStage(t)

	built := mokkit.Resolve[*app](stage)

	const prefix = "*bag_test.app -> *bag_test.core -> *bag_test.hub -> "
	for _, tc := range []struct {
		branch string
		cycle  *bag.CycleError
		want   string
	}{
		{"left", built.Core.Hub.Left.Cycle, prefix + "*bag_test.leftArm -> *bag_test.leftHand -> *bag_test.hub"},
		{"right", built.Core.Hub.Right.Cycle, prefix + "*bag_test.rightArm -> *bag_test.rightHand -> *bag_test.hub"},
	} {
		if tc.cycle == nil {
			t.Errorf("expected the %s branch to report a cycle", tc.branch)

			continue
		}
		if got := pathNames(tc.cycle.Path); got != tc.want {
			t.Errorf("the %s branch reported %q, want %q", tc.branch, got, tc.want)
		}
	}
}

// cycleFrom runs fn on its own goroutine and reports the cycle it panicked
// with. The deadline is the point of doing it this way: the failure mode being
// guarded against is a hang, and a suite that hangs reports nothing about which
// test broke.
func cycleFrom(t *testing.T, fn func()) *bag.CycleError {
	t.Helper()

	raised := make(chan any, 1)
	go func() {
		defer func() { raised <- recover() }()
		fn()
	}()

	select {
	case r := <-raised:
		if r == nil {
			t.Fatal("expected a cycle to be reported")
		}
		err, ok := r.(*bag.CycleError)
		if !ok {
			t.Fatalf("expected a *bag.CycleError, got %T: %v", r, r)
		}

		return err
	case <-time.After(5 * time.Second):
		t.Fatal("resolving a cyclic graph deadlocked instead of reporting the cycle")

		return nil
	}
}

// recoveredCycle reads a recovered value as the cycle it is meant to be, and
// re-panics on anything else so a factory expecting one does not quietly
// swallow an unrelated failure.
func recoveredCycle(r any) *bag.CycleError {
	if r == nil {
		return nil
	}

	err, ok := r.(*bag.CycleError)
	if !ok {
		panic(r)
	}

	return err
}

// pathNames renders a cycle path the way the error message does, so a failing
// assertion prints the construction chain rather than a slice of reflect.Type.
func pathNames(path []reflect.Type) string {
	names := make([]string, len(path))
	for i, typ := range path {
		names[i] = typ.String()
	}

	return strings.Join(names, " -> ")
}
