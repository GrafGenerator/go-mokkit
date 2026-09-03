// Package mokkitmockery composes a mokkit stage from testify mocks, the kind
// mockery generates.
//
// A generated mock is registered under two types: the interface, so the subject
// receives it, and the mock's own type, so vocabulary can reach EXPECT() to
// arrange and to observe. Both resolve to one instance per stage.
//
//	mocks := mokkitmockery.New()
//	mokkitmockery.Add[UserRepository](mocks, shop.NewMockUserRepository)
//
//	app := bag.New()
//	bag.Scoped(app, func(r mokkit.Resolver) *DiscountService {
//	    return &DiscountService{Users: mokkit.Resolve[UserRepository](r)}
//	})
//
//	setup, err := mokkit.NewSetup(ctx, mocks, app)
//
// Each stage hands its own test to every constructor, so a mock's expectations
// are asserted when that test finishes and nothing carries over between tests.
package mokkitmockery

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/stretchr/testify/mock"

	"github.com/GrafGenerator/go-mokkit"
)

// TestingT is what a mockery-generated constructor asks for: testify's
// reporter plus Cleanup. Add spells it anonymously in its signature because a
// defined type is not identical to the anonymous one in generated code, and the
// two must unify for M to be inferred; this named form exists for
// documentation and for hand-written constructors.
type TestingT = interface {
	mock.TestingT
	Cleanup(func())
}

// reporter adapts the stage's TB to testify's expectations. testify splits its
// report: Errorf carries only a count ("0 out of 1 expectation(s) were met")
// while the lines naming the missing calls go through Logf, which mokkit's TB
// does not have. The reporter keeps the FAIL detail lines and attaches them to
// the next Errorf, so the test's failure names the calls; the PASS chatter is
// dropped.
type reporter struct {
	mokkit.TB

	mu    sync.Mutex
	fails []string
}

func (r *reporter) Logf(format string, args ...any) {
	line := strings.TrimSpace(fmt.Sprintf(format, args...))
	if !strings.HasPrefix(line, "FAIL") {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.fails = append(r.fails, line)
}

func (r *reporter) Errorf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)

	r.mu.Lock()
	if len(r.fails) > 0 {
		msg += "\n" + strings.Join(r.fails, "\n")
		r.fails = nil
	}
	r.mu.Unlock()

	r.TB.Errorf("%s", msg)
}

// A Builder collects mock registrations. Build it into a container by handing
// it to mokkit.NewSetup.
type Builder struct {
	mu    sync.Mutex
	regs  []registration
	seen  map[reflect.Type]bool
	built bool
}

type registration struct {
	iface reflect.Type
	mock  reflect.Type
	make  func(TestingT) any
}

// New returns an empty builder.
func New() *Builder {
	return &Builder{seen: make(map[reflect.Type]bool)}
}

// Add registers the mock that ctor builds as standing in for the interface I.
// ctor is mockery's generated constructor, so the call reads:
//
//	mokkitmockery.Add[UserRepository](b, shop.NewMockUserRepository)
//
// I must be an interface and M must implement it; both are checked here rather
// than at resolve time, so a mismatch fails while the fixture is being written.
func Add[I, M any](b *Builder, ctor func(TestingT) M) *Builder {
	iface, mockType := reflect.TypeFor[I](), reflect.TypeFor[M]()

	if iface.Kind() != reflect.Interface {
		panic(fmt.Sprintf("mokkitmockery: %s is not an interface; a mock stands in for an interface", iface))
	}
	if !mockType.Implements(iface) {
		panic(fmt.Sprintf("mokkitmockery: %s does not implement %s", mockType, iface))
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.built {
		panic(fmt.Sprintf("mokkitmockery: %s registered after the container was built", iface))
	}
	for _, typ := range [...]reflect.Type{iface, mockType} {
		if b.seen[typ] {
			panic(fmt.Sprintf("mokkitmockery: %s is already registered", typ))
		}
		b.seen[typ] = true
	}

	b.regs = append(b.regs, registration{
		iface: iface,
		mock:  mockType,
		make:  func(t TestingT) any { return ctor(t) },
	})

	return b
}

// Registered reports whether I is already spoken for — either as an interface a
// mock stands in for, or as a generated mock's own type. Both are keys, so both
// answer true.
func Registered[I any](b *Builder) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.seen[reflect.TypeFor[I]()]
}

// asserter is the slice of a testify mock that Satisfied needs.
type asserter interface {
	AssertExpectations(t mock.TestingT) bool
}

// expectations is the stage's set of mocks, resolvable so an Inspect step can
// ask about all of them at once.
type expectations struct {
	mocks []asserter
}

// Satisfied is an Inspect step asserting that every expectation declared so far
// has been met by the time the chain reaches it.
//
// It exists for attribution. A mock's own assertion only runs when the test
// finishes, and that failure is reported without a phase or a verb. Placing
// this in Inspect fails at the test's line instead.
func Satisfied() mokkit.Step {
	return mokkit.NewStep("mockery.Satisfied", func(_ context.Context, h mokkit.Host) error {
		exp := mokkit.Resolve[*expectations](h)

		quiet := &collectingT{}
		for _, m := range exp.mocks {
			m.AssertExpectations(quiet)
		}

		if len(quiet.failures) == 0 {
			return nil
		}

		return fmt.Errorf("mokkitmockery: expectations declared while arranging have not all been met:\n%s",
			strings.Join(quiet.failures, "\n"))
	})
}

// collectingT gathers testify's assertion output instead of failing anything,
// so Satisfied can report it as the step's own error.
type collectingT struct {
	failures []string
}

func (c *collectingT) Logf(string, ...any) {}

func (c *collectingT) Errorf(format string, args ...any) {
	c.failures = append(c.failures, strings.TrimSpace(fmt.Sprintf(format, args...)))
}

func (c *collectingT) FailNow() {}

// Build satisfies mokkit.ContainerBuilder. Registering after this panics.
func (b *Builder) Build(context.Context) (mokkit.Container, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.built = true

	return &container{regs: append([]registration(nil), b.regs...)}, nil
}

type container struct {
	regs []registration
}

func (c *container) BeginScope(_ context.Context, sc mokkit.StageContext) (mokkit.Scope, error) {
	if sc.T == nil {
		return nil, errors.New("mokkitmockery: the stage has no test to report expectations to")
	}

	// The constructor registers AssertExpectations with the test's cleanup.
	// Stage.Close is registered after BeginScope and cleanups run last-first,
	// so scopes are released before expectations are checked.
	t := &reporter{TB: sc.T}

	exp := &expectations{}
	items := make(map[reflect.Type]any, len(c.regs)*2+1)
	items[reflect.TypeFor[*expectations]()] = exp

	for _, reg := range c.regs {
		m := reg.make(t)
		items[reg.mock] = m
		items[reg.iface] = m

		if a, ok := m.(asserter); ok {
			exp.mocks = append(exp.mocks, a)
		}
	}

	return &scope{items: items}, nil
}

type scope struct {
	items map[reflect.Type]any
}

func (s *scope) TryResolveType(typ reflect.Type) (any, bool) {
	v, ok := s.items[typ]

	return v, ok
}

// Close does nothing: each mock asserts its expectations through the cleanup
// its constructor registered on the stage's test.
func (s *scope) Close() error { return nil }
