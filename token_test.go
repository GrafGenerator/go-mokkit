package mokkit

import (
	"strings"
	"sync"
	"testing"
)

// Buyer and Seller are roles over the same artifact type, which is the whole
// reason the registry is keyed by token rather than by what the token names:
// one test needs two users and has to tell them apart.
type Buyer struct{ Artifact[User] }

type Seller struct{ Artifact[User] }

// Basket names something else entirely, so a verb generic over Token[User] will
// not accept it — that pairing is a compile error, not a run-time surprise:
//
//	f.Of[Basket]()          // fine
//	userQueried[Basket](f)  // does not compile: Basket does not name a User
type Basket struct{ Artifact[basket] }

type basket struct{ Total int }

func TestNewCreatesTheSinkOnceAndHandsBackTheSameOne(t *testing.T) {
	r := NewTokens(newFakeTB(t.Name()))

	first := r.New[Buyer]()
	*first = User{ID: "u1"}
	second := r.New[Buyer]()

	if first != second {
		t.Fatal("a second verb acting for the same role must fill the same sink")
	}
	if second.ID != "u1" {
		t.Errorf("expected the earlier write to survive, got %+v", *second)
	}
}

func TestTwoTokensNamingTheSameArtifactTypeAreDistinctSlots(t *testing.T) {
	r := NewTokens(newFakeTB(t.Name()))

	*r.New[Buyer]() = User{ID: "buyer"}
	*r.New[Seller]() = User{ID: "seller"}

	if got := r.Of[Buyer]().ID; got != "buyer" {
		t.Errorf("expected the buyer, got %q", got)
	}
	if got := r.Of[Seller]().ID; got != "seller" {
		t.Errorf("expected the seller, got %q", got)
	}
}

func TestOfReturnsTheValueThatWasWritten(t *testing.T) {
	r := NewTokens(newFakeTB(t.Name()))

	*r.New[Buyer]() = User{ID: "u1", Status: "vip"}

	got := r.Of[Buyer]()
	if got.ID != "u1" || got.Status != "vip" {
		t.Errorf("expected the arranged user, got %+v", got)
	}
}

func TestOfHandsBackACopySoAReaderCannotWriteThroughIt(t *testing.T) {
	// Of returns a value rather than a pointer, which is what keeps *T out of
	// read-only positions: a consuming verb takes what was arranged and cannot
	// quietly re-arrange it.
	r := NewTokens(newFakeTB(t.Name()))
	*r.New[Buyer]() = User{ID: "u1"}

	stolen := r.Of[Buyer]()
	//nolint:govet // the write going nowhere is exactly what this asserts.
	stolen.ID = "someone else"

	if got := r.Of[Buyer]().ID; got != "u1" {
		t.Errorf("expected the slot to be untouched, got %q", got)
	}
}

func TestOfFailsTheTestWhenNothingHasBeenArrangedAtAll(t *testing.T) {
	tb := newFakeTB(t.Name())
	r := NewTokens(tb)

	runGoexit(func() { _ = r.Of[Buyer]() })

	assertContains(t, tb.Fatals(),
		"nothing arranged for mokkit.Buyer",
		"(nothing has been arranged yet)",
	)
}

func TestOfListsTheRolesThatWereArrangedWhenTheOneAskedForWasNot(t *testing.T) {
	// The common mistake is reading the wrong role, not reading too early, so
	// the message says which roles do exist.
	tb := newFakeTB(t.Name())
	r := NewTokens(tb)
	*r.New[Buyer]() = User{ID: "u1"}
	*r.New[Basket]() = basket{Total: 10}

	runGoexit(func() { _ = r.Of[Seller]() })

	assertContains(t, tb.Fatals(),
		"nothing arranged for mokkit.Seller",
		"(have: mokkit.Basket, mokkit.Buyer)",
	)
	if strings.Contains(strings.Join(tb.Fatals(), "\n"), "nothing has been arranged yet") {
		t.Error("something had been arranged, so the empty-registry wording is wrong")
	}
}

func TestDeclaredReportsWithoutCreatingTheSlot(t *testing.T) {
	tb := newFakeTB(t.Name())
	r := NewTokens(tb)

	if r.Declared[Buyer]() {
		t.Error("nothing has produced a Buyer yet")
	}

	// Asking must not be the same as producing: Of still fails, and still says
	// the registry is empty.
	runGoexit(func() { _ = r.Of[Buyer]() })
	assertContains(t, tb.Fatals(), "(nothing has been arranged yet)")

	*r.New[Buyer]() = User{ID: "u1"}
	if !r.Declared[Buyer]() {
		t.Error("expected Declared to report the produced role")
	}
	if r.Declared[Seller]() {
		t.Error("producing one role must not declare another")
	}
}

func TestNameOfIsTheBareTokenName(t *testing.T) {
	// Verbs build their step labels out of this, so it has to read as the role
	// the test wrote rather than as a package-qualified type.
	if got := NameOf[Buyer](); got != "Buyer" {
		t.Errorf("want %q, got %q", "Buyer", got)
	}
}

func TestConcurrentNewYieldsOneSink(t *testing.T) {
	// Branches of an All produce concurrently, so two verbs acting for the same
	// role must not each get their own sink.
	r := NewTokens(newFakeTB(t.Name()))

	const n = 32
	got := make([]*User, n)

	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)

		go func() {
			defer wg.Done()
			got[i] = r.New[Buyer]()
		}()
	}
	wg.Wait()

	for i := 1; i < n; i++ {
		if got[i] != got[0] {
			t.Fatalf("concurrent New disagreed at %d", i)
		}
	}
}

// --- Ref: the read side for an artifact that has identity --------------------

// counter stands in for a recording double: a verb mutates it, a later phase
// observes what happened to it. A copy would be a different thing.
type counter struct{ n int }

type Tally struct{ Artifact[counter] }

func TestRefHandsBackTheSameArtifactSoAMutationIsVisible(t *testing.T) {
	reg := NewTokens(newFakeTB(t.Name()))

	reg.New[Tally]().n = 1
	reg.Ref[Tally]().n++

	if got := reg.Of[Tally]().n; got != 2 {
		t.Errorf("a mutation through Ref must be visible to a later read, got %d, want 2", got)
	}
}

func TestOfHandsBackACopySoAMutationThroughItIsNotVisible(t *testing.T) {
	reg := NewTokens(newFakeTB(t.Name()))

	reg.New[Tally]().n = 1

	got := reg.Of[Tally]()
	got.n++

	if reg.Of[Tally]().n != 1 {
		t.Error("Of must hand back a copy, so a read-only phase cannot write through it")
	}
}

func TestRefFailsLoudlyWhenNothingWasArranged(t *testing.T) {
	tb := newFakeTB(t.Name())
	reg := NewTokens(tb)

	reg.New[Buyer]()

	runGoexit(func() { _ = reg.Ref[Tally]() })

	assertContains(t, tb.Fatals(), "nothing arranged for", "Tally", "have:", "Buyer")
}
