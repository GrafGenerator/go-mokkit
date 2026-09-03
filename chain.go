package mokkit

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// FailMode decides what a failing step does to the test.
type FailMode int

const (
	// FailFast reports through t.Fatal, which unwinds the test goroutine — so
	// the rest of the chain never runs. Used by Arrange and Act, where a broken
	// setup makes every later step meaningless.
	FailFast FailMode = iota

	// FailSoft reports through t.Error and carries on, so a run reports every
	// failing observation rather than only the first. Used by Inspect.
	FailSoft
)

// A Chain is one phase of a test — arrange, act or inspect — that executes each
// step as it is added. There is no terminal call: by the time a verb returns,
// its step has run.
//
// Vocabulary is authored by embedding a *Chain in your own type and hanging
// verbs on it:
//
//	type Arrange struct{ *mokkit.Chain }
//
//	func (a Arrange) UserExists[K mokkit.Token[User]](s Status) Arrange {
//	    a.Helper()
//	    a.Add("UserExists["+mokkit.NameOf[K]()+"]", func(ctx context.Context, h mokkit.Host) error {
//	        *a.New[K]() = newUser(s)
//	        h.Resolve[*MockUsers]().EXPECT()...
//	        return nil
//	    })
//	    return a
//	}
type Chain struct {
	// Tokens is embedded so a verb reaches its artifacts the same way a test
	// does — a.New[Buyer]() to produce, a.Of[Buyer]() to read.
	*Tokens

	// helper is embedded, and must stay embedded: the compiler elides the
	// promoted wrapper, so a.Helper() marks the calling verb's frame. A
	// hand-written forwarding method would mark its own frame instead, and
	// every failure would report the verb's body rather than the test's line.
	helper

	// tb is deliberately not embedded: promoting Errorf/Fatalf/FailNow onto
	// vocabulary types would let a verb report around the chain — and a Fatalf
	// inside an All branch Goexits the wrong goroutine. Steps report by
	// returning an error; assertion libraries are handed TB() instead.
	tb TB

	ex    Executor
	ctx   context.Context
	phase string
	mode  FailMode

	test      string
	stageID   string
	observers []Observer
}

// helper is the one part of TB that is safe to promote onto a vocabulary type:
// it cannot report, cannot fail, and every verb needs it.
type helper interface{ Helper() }

// TB reports the test this chain belongs to. Hand it, not the chain, to an
// assertion library: assert suits Inspect's soft failure, require suits
// Arrange's hard one, and going through TB keeps a verb from reporting around
// the chain by accident.
func (c *Chain) TB() TB { return c.tb }

// Add runs one named step and reports failure according to the chain's
// FailMode. The name is what identifies the step in a failure message, so it
// should be the verb the reader wrote — including the role it acted for, which
// NameOf supplies.
func (c *Chain) Add(name string, fn StepFunc) *Chain {
	c.tb.Helper()
	c.run(name, NewStep(name, fn))

	return c
}

// And runs vocabulary authored as plain functions, including from packages that
// cannot add methods to this chain's type. This is what keeps a chain unbroken
// across a foreign verb, and it is named to read as a continuation of the
// sentence rather than as an imperative aside:
//
//	f.Arrange().
//	    UserExists[Buyer](Vip).
//	    And(cachevocab.HasUser[Buyer](f)).
//	    RateIs(Vip, 0.15)
//
// A vocabulary type re-declares this to keep its own return type, and may name
// it whatever reads best there — And, Also, Then.
func (c *Chain) And(steps ...Step) *Chain {
	c.tb.Helper()
	for i, s := range steps {
		c.run(stepLabel(s, i), s)
	}

	return c
}

// All runs steps concurrently and continues once every one has finished, so the
// group is unordered within itself but ordered against its neighbors in the
// chain.
//
// Under FailFast the first failure reported ends the test, so later failures in
// the same group are not shown; under FailSoft every failure is reported.
// Branches run on their own goroutines and must report by returning an error,
// never by calling the test's own Fatal.
func (c *Chain) All(steps ...Step) *Chain {
	c.tb.Helper()
	if len(steps) <= 1 {
		return c.And(steps...)
	}

	errs := make([]error, len(steps))
	var wg sync.WaitGroup

	for i, s := range steps {
		wg.Add(1)

		go func() {
			defer wg.Done()
			errs[i] = c.observe(stepLabel(s, i), s)
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			c.fail(stepLabel(steps[i], i), err)
		}
	}

	return c
}

// Context reports the context steps in this chain are run with.
func (c *Chain) Context() context.Context { return c.ctx }

// WithContext runs subsequent steps with ctx. It mutates the chain and returns
// it, exactly as And and All do, so a vocabulary type's forwarder is written
// the same way as theirs:
//
//	func (a Arrange) WithContext(ctx context.Context) Arrange {
//	    a.Helper(); a.Chain.WithContext(ctx); return a
//	}
//
// It does not affect steps already run.
func (c *Chain) WithContext(ctx context.Context) *Chain {
	c.ctx = ctx

	return c
}

func (c *Chain) run(label string, s Step) {
	c.tb.Helper()
	if err := c.observe(label, s); err != nil {
		c.fail(label, err)
	}
}

// observe runs one step through the executor and tells the observers how it
// went. Reporting is left to the caller: run fails the chain immediately, All
// collects its branches first.
func (c *Chain) observe(label string, s Step) error {
	started := time.Now()
	err := c.ex.Run(c.ctx, s)

	if len(c.observers) > 0 {
		emitStep(c.observers, StepEvent{
			Test:     c.test,
			StageID:  c.stageID,
			Phase:    c.phase,
			Step:     label,
			Started:  started,
			Duration: time.Since(started),
			Err:      err,
		})
	}

	return err
}

// Group sequences steps into one, so a branch of All can be several steps
// rather than one. Within the group they run in order and stop at the first
// failure, which reports as "group: step"; between branches of an All nothing is
// shared, so every other branch still finishes.
func Group(name string, steps ...Step) Step {
	return NewStep(name, func(ctx context.Context, h Host) error {
		for i, s := range steps {
			if err := s.Run(ctx, h); err != nil {
				return fmt.Errorf("%s: %w", stepLabel(s, i), err)
			}
		}

		return nil
	})
}

// stepLabel falls back to the step's position when vocabulary left it unnamed.
func stepLabel(s Step, i int) string {
	if s.Name != "" {
		return s.Name
	}

	return fmt.Sprintf("step[%d]", i)
}

func (c *Chain) fail(name string, err error) {
	c.tb.Helper()
	if c.mode == FailSoft {
		c.tb.Errorf("%s: %s: %v", c.phase, name, err)

		return
	}
	c.tb.Fatalf("%s: %s: %v", c.phase, name, err)
}
