package mokkit

import (
	"context"
	"fmt"
	"runtime"
	"strings"
)

// An Executor runs steps against a stage.
//
// The chain API is identical whichever Executor is in use, so a channel-backed
// executor — one worker goroutine per stage, allowing steps to be posted from
// other goroutines and giving tracing a single choke point — can replace the
// inline one without touching Chain or any vocabulary.
type Executor interface {
	// Run executes one step. The Step arrives whole — name included — so an
	// executor that traces or reports has something to say about it.
	Run(ctx context.Context, s Step) error
	Close() error
}

// inlineExecutor runs each step on the calling goroutine.
type inlineExecutor struct {
	host Host
}

func (e *inlineExecutor) Run(ctx context.Context, s Step) (err error) {
	defer func() {
		// recover reports nil during runtime.Goexit, so a t.Fatal from a
		// nested helper unwinds untouched rather than being swallowed here.
		if r := recover(); r != nil {
			err = &PanicError{Value: r, Stack: panicStack()}
		}
	}()

	return s.Run(ctx, Host{ctx: ctx, r: e.host.r})
}

func (e *inlineExecutor) Close() error { return nil }

// PanicError reports a panic raised inside a step, carrying the stack from the
// point of the panic rather than from where it was recovered.
type PanicError struct {
	Value any
	Stack []byte
}

func (e *PanicError) Error() string {
	// The "panic:" prefix stays even when the value is an error, because a
	// crash and a returned failure are different events and a reader who cannot
	// tell them apart looks in the wrong place first.
	msg := fmt.Sprintf("panic: %v", e.Value)
	if len(e.Stack) == 0 {
		return msg
	}

	return msg + "\n" + string(e.Stack)
}

// Unwrap lets errors.As reach a panicked error value, so a caller can test for
// *ResolveError after Resolve panicked inside a step.
func (e *PanicError) Unwrap() error {
	err, _ := e.Value.(error)

	return err
}

// panicStack renders the frames between the panic and the executor that caught
// it: the vocabulary that panicked and what it called, without the recovery
// machinery above or mokkit's own plumbing below. A raw debug.Stack here buries
// the two frames that matter in about twenty that do not.
//
// When the executor's own frame cannot be found — a stack deeper than the
// buffer — it reports nothing rather than spilling every collected frame into
// the failure message.
// skipToPanic is how many frames sit between runtime.Callers and the panicking
// step: Callers itself, panicStack, and the deferred recover in Run.
const skipToPanic = 3

func panicStack() []byte {
	for _, depth := range [...]int{64, 512} {
		if s, ok := trimmedStack(depth); ok {
			return s
		}
	}

	return nil
}

func trimmedStack(depth int) ([]byte, bool) {
	pcs := make([]uintptr, depth)

	n := runtime.Callers(skipToPanic, pcs)
	if n == 0 {
		return nil, true
	}

	var b strings.Builder

	frames := runtime.CallersFrames(pcs[:n])
	started, emitted := false, false

	for {
		frame, more := frames.Next()

		switch {
		case !started:
			// Everything up to and including the runtime's panic entry is the
			// unwinding itself.
			started = frame.Function == "runtime.gopanic"
		case strings.HasSuffix(frame.Function, "(*inlineExecutor).Run"):
			return []byte(b.String()), true
		case !emitted && strings.HasPrefix(frame.Function, "runtime."):
			// The frames that raised the panic — panicBounds and the like. The
			// message already says what happened.
		default:
			emitted = true
			fmt.Fprintf(&b, "\t%s\n\t\t%s:%d\n", frame.Function, frame.File, frame.Line)
		}

		if !more {
			// The executor's frame was never reached, so the buffer was too
			// small and everything gathered is untrimmed noise.
			return nil, n < depth
		}
	}
}
