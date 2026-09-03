package mokkit

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
)

// Artifact is the phantom a token embeds to declare what it names:
//
//	type Buyer struct{ mokkit.Artifact[User] }
//
// One line declares both the role and the type of the thing that role stands
// for, and the type is recovered by inference at every call site — so a token
// is spelled once and carries its meaning everywhere it is used.
type Artifact[T any] struct{}

func (Artifact[T]) artifact() T {
	var zero T

	return zero
}

// Token constrains a role to one artifact type. A verb generic over
// Token[User] will not accept a token that names an Order, so the pairing is
// checked by the compiler rather than discovered at run time.
type Token[T any] interface{ artifact() T }

// NameOf reports a token's bare name, so a verb can build a readable step label
// or seed an identifier with the role it is acting for.
func NameOf[K any]() string { return reflect.TypeFor[K]().Name() }

// Tokens holds the artifacts a test's verbs produce, keyed by the token that
// names each one. It is the answer to "declare the artifact where you use it":
// New hands a producing verb its sink and Of reads the value back, both spelled
// only with the token, in any phase, without a variable declared above.
//
// Tokens belongs to a single test. Stage creates one per stage, so nothing
// leaks between tests.
type Tokens struct {
	t     TB
	mu    sync.Mutex
	slots map[reflect.Type]any
}

// NewTokens returns an empty registry that reports lookup failures through t.
func NewTokens(t TB) *Tokens {
	return &Tokens{t: t, slots: make(map[reflect.Type]any)}
}

// New returns the sink for the role K, creating it on first use. It is the
// write side: a producing verb fills the pointer it returns.
//
//	func (a Arrange) UserExists[K mokkit.Token[User]](s Status) Arrange {
//	    a.Add("UserExists["+mokkit.NameOf[K]()+"]", func(...) error {
//	        *a.New[K]() = newUser(s)
//	        ...
//	    })
//	    return a
//	}
func (r *Tokens) New[K Token[A], A any]() *A {
	key := reflect.TypeFor[K]()

	r.mu.Lock()
	defer r.mu.Unlock()

	if v, ok := r.slots[key]; ok {
		return sinkOf[A](v, key)
	}
	p := new(A)
	r.slots[key] = p

	return p
}

// Of returns the value filed under the role K, failing the test when no verb
// has produced it. It returns a value rather than a pointer, which keeps *T out
// of read-only positions and is safe inside a single chain expression: the Go
// spec orders method calls left to right, where a bare variable operand is
// unordered against the call that fills it.
func (r *Tokens) Of[K Token[A], A any]() A {
	r.t.Helper()

	key := reflect.TypeFor[K]()

	r.mu.Lock()
	defer r.mu.Unlock()

	if v, ok := r.slots[key]; ok {
		return *sinkOf[A](v, key)
	}
	r.t.Fatalf("mokkit: nothing arranged for %s%s", key, r.haveLocked())

	var zero A

	return zero
}

// Ref returns the artifact filed under the role K as a pointer, failing the
// test when no verb has produced it. It is Of for an artifact that has
// identity: a double whose state a verb mutates and a later phase observes.
//
// Prefer Of. A value cannot be written through by accident, which is what keeps
// a read-only phase read-only. Reach for Ref when a copy would be a different
// thing from the original — a recording double, a probe, anything whose whole
// point is that the artifact the Act mutated is the artifact the Inspect reads.
func (r *Tokens) Ref[K Token[A], A any]() *A {
	r.t.Helper()

	key := reflect.TypeFor[K]()

	r.mu.Lock()
	defer r.mu.Unlock()

	if v, ok := r.slots[key]; ok {
		return sinkOf[A](v, key)
	}
	r.t.Fatalf("mokkit: nothing arranged for %s%s", key, r.haveLocked())

	return nil
}

// Declared reports whether the role K has been produced, without producing it
// and without failing.
func (r *Tokens) Declared[K Token[A], A any]() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, ok := r.slots[reflect.TypeFor[K]()]

	return ok
}

// haveLocked lists the roles already produced, so reading one that was never
// arranged says what was. The caller holds r.mu.
func (r *Tokens) haveLocked() string {
	if len(r.slots) == 0 {
		return " (nothing has been arranged yet)"
	}

	names := make([]string, 0, len(r.slots))
	for key := range r.slots {
		names = append(names, key.String())
	}
	sort.Strings(names)

	return " (have: " + strings.Join(names, ", ") + ")"
}

// sinkOf recovers a typed sink from the registry's untyped map. The assertion
// cannot fail — the key is the token's own type and a token determines its
// artifact type — but writing it checked means that if the invariant is ever
// broken the message names the two types that disagreed, rather than reporting
// a bare interface conversion somewhere inside a verb.
func sinkOf[A any](v any, key reflect.Type) *A {
	p, ok := v.(*A)
	if !ok {
		panic(fmt.Sprintf("mokkit: %s is filed under %s, which names %s",
			reflect.TypeOf(v), key, reflect.TypeFor[A]()))
	}

	return p
}
