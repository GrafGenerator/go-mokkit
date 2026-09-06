package mokkit

import (
	"context"
	"runtime"
	"strings"
)

// A Vocabulary is any type that embeds *Chain: a phase such as Arrange, or a
// scope that carries a value alongside the chain.
type Vocabulary interface{ chain() *Chain }

func (c *Chain) chain() *Chain { return c }

// A Body is the work a verb hands to Do. It receives the stage's Host and
// reports failure by returning an error; a body that cannot fail returns
// nothing.
type Body interface {
	func(Host) | func(Host) error | func(context.Context, Host) error
}

// Do runs fn as a step of v's chain and hands v back, so a verb is one return:
//
//	func (a Arrange) SequenceYields(ids ...int64) Arrange {
//	    a.Helper()
//
//	    return mokkit.Do(a, func(h mokkit.Host) {
//	        h.Resolve[*fakeSequence]().values = ids
//	    })
//	}
//
// The step is named after the verb that called Do. A verb generic over a role
// passes mokkit.NameOf[K]() as role, which is appended to the name in brackets.
func Do[V Vocabulary, F Body](v V, fn F, role ...string) V {
	c := v.chain()
	c.tb.Helper()

	label := verbLabel(role)
	c.run(label, NewStep(label, toStepFunc(fn)))

	return v
}

// Get runs fn as a step and returns what it produced. An error fails the chain
// according to its FailMode; the value is returned only when there was none.
//
//	func (a Act) Discount(userID string) Result {
//	    a.Helper()
//
//	    return a.Get(func(h mokkit.Host) (Result, error) {
//	        return h.Resolve[*Service]().Calculate(h.Context(), userID)
//	    })
//	}
//
// The step is named after the verb that called Get; role is appended as in Do.
func (c *Chain) Get[T any](fn func(Host) (T, error), role ...string) T {
	c.tb.Helper()

	var out T

	label := verbLabel(role)
	c.run(label, NewStep(label, func(_ context.Context, h Host) error {
		var err error
		out, err = fn(h)

		return err
	}))

	return out
}

// An Outcome is what Try hands back: the value fn produced and the error it
// returned. Exactly one of them is meaningful.
type Outcome[T any] struct {
	Value T
	Err   error
}

// Try runs fn as a step and returns its outcome without failing the chain on
// the error, so a test for a refusal inspects the error the way it inspects a
// value. A panic inside fn still fails the chain.
//
// The step is named after the verb that called Try; role is appended as in Do.
func (c *Chain) Try[T any](fn func(Host) (T, error), role ...string) Outcome[T] {
	c.tb.Helper()

	var out Outcome[T]

	label := verbLabel(role)
	c.run(label, NewStep(label, func(_ context.Context, h Host) error {
		out.Value, out.Err = fn(h)

		return nil
	}))

	return out
}

func toStepFunc[F Body](fn F) StepFunc {
	switch fn := any(fn).(type) {
	case func(Host):
		return func(_ context.Context, h Host) error {
			fn(h)

			return nil
		}
	case func(Host) error:
		return func(_ context.Context, h Host) error { return fn(h) }
	case func(context.Context, Host) error:
		return fn
	default:
		panic("mokkit: unreachable: Body admits no other type")
	}
}

// verbFrames is how many frames runtime.Callers skips to reach the verb:
// Callers itself, callerName, verbLabel, and Do, Get or Try.
const verbFrames = 4

// verbLabel names a step after the function that called Do, Get or Try, with
// the role appended in brackets when one is given.
func verbLabel(role []string) string {
	name := callerName(verbFrames)
	if name == "" {
		name = "step"
	}
	if len(role) > 0 {
		name += "[" + strings.Join(role, ",") + "]"
	}

	return name
}

// callerName reports the bare name of the function skip frames up, seeing
// through inlining.
func callerName(skip int) string {
	var pcs [1]uintptr
	if runtime.Callers(skip, pcs[:]) == 0 {
		return ""
	}

	frame, _ := runtime.CallersFrames(pcs[:]).Next()

	return bareName(frame.Function)
}

// bareName reduces a symbol to the function's own name: the import path, the
// receiver, type arguments and closure ordinals are all dropped.
//
//	example.com/app.Arrange.UserExists[...].func1  ->  UserExists
//	example.com/app.(*Probe).reset                 ->  reset
func bareName(symbol string) string {
	name := symbol[strings.LastIndex(symbol, "/")+1:]
	name = strings.TrimSuffix(withoutBrackets(name), "-fm")

	segments := strings.Split(name, ".")
	for len(segments) > 1 && isClosureOrdinal(segments[len(segments)-1]) {
		segments = segments[:len(segments)-1]
	}

	return segments[len(segments)-1]
}

func withoutBrackets(s string) string {
	var b strings.Builder
	depth := 0

	for _, r := range s {
		switch {
		case r == '[':
			depth++
		case r == ']' && depth > 0:
			depth--
		case depth == 0:
			b.WriteRune(r)
		}
	}

	return b.String()
}

// isClosureOrdinal reports whether a symbol segment is one the compiler gave a
// closure: func1, or a bare number for a closure nested in one.
func isClosureOrdinal(segment string) bool {
	digits := strings.TrimPrefix(segment, "func")
	if digits == "" {
		return false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
	}

	return true
}
