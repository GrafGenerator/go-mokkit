package bag_test

import (
	"context"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/GrafGenerator/go-mokkit"
	"github.com/GrafGenerator/go-mokkit/container/bag"
)

type Clock interface{ Now() string }

type fixedClock struct{ at string }

func (c *fixedClock) Now() string { return c.at }

type UserRepository interface{ ByID(id string) string }

type fakeUsers struct{ prefix string }

func (f *fakeUsers) ByID(id string) string { return f.prefix + id }

// Service is the real subject, built over whatever the composition provides.
type Service struct {
	Users  UserRepository
	Clock  Clock
	log    *closeLog
	closed bool
}

func (s *Service) Describe() string { return s.Users.ByID("1") + "@" + s.Clock.Now() }

func (s *Service) Close() error {
	s.closed = true
	if s.log != nil {
		s.log.record("service")
	}

	return nil
}

func setupWith(t *testing.T, builders ...mokkit.ContainerBuilder) *mokkit.Setup {
	t.Helper()

	setup, err := mokkit.NewSetup(context.Background(), builders...)
	if err != nil {
		t.Fatalf("NewSetup: %v", err)
	}

	return setup
}

func TestInstanceIsSharedByEveryStage(t *testing.T) {
	clock := &fixedClock{at: "noon"}
	b := bag.New()
	bag.Instance[Clock](b, clock)

	setup := setupWith(t, b)
	first := setup.EnterStage(t)
	second := setup.EnterStage(t)

	if mokkit.Resolve[Clock](first) != Clock(clock) {
		t.Error("expected the registered instance")
	}
	if mokkit.Resolve[Clock](first) != mokkit.Resolve[Clock](second) {
		t.Error("an Instance is shared across stages")
	}
}

func TestInstanceCanBeExposedConcretelyAndBehindAnInterface(t *testing.T) {
	users := &fakeUsers{prefix: "u-"}
	b := bag.New()
	bag.Instance[UserRepository](b, users)
	bag.Instance[*fakeUsers](b, users)

	stage := setupWith(t, b).EnterStage(t)

	// This is how a double is arranged through its concrete type and consumed
	// by the subject through its interface — the same instance either way.
	if mokkit.Resolve[UserRepository](stage) != UserRepository(users) {
		t.Error("expected the interface registration")
	}
	if mokkit.Resolve[*fakeUsers](stage) != users {
		t.Error("expected the concrete registration")
	}
}

func TestScopedIsBuiltOncePerStageAndFreshBetweenStages(t *testing.T) {
	var builds int
	b := bag.New()
	bag.Instance[Clock](b, &fixedClock{at: "noon"})
	bag.Instance[UserRepository](b, &fakeUsers{prefix: "u-"})
	bag.Scoped(b, func(r mokkit.Resolver) *Service {
		builds++

		return &Service{Users: mokkit.Resolve[UserRepository](r), Clock: mokkit.Resolve[Clock](r)}
	})

	setup := setupWith(t, b)

	stage := setup.EnterStage(t)
	first := mokkit.Resolve[*Service](stage)
	if mokkit.Resolve[*Service](stage) != first {
		t.Error("a scoped service is built once per stage")
	}
	if builds != 1 {
		t.Errorf("expected 1 build, got %d", builds)
	}

	if mokkit.Resolve[*Service](setup.EnterStage(t)) == first {
		t.Error("a new stage must get its own scoped instance, or tests leak into each other")
	}
}

func TestAFactoryReachesCollaboratorsInAnotherContainer(t *testing.T) {
	// The doubles live in one container and the real subject in another. That
	// the subject receives the test's doubles is the whole of what the C#
	// mock-to-DI bridge did.
	doubles := bag.New()
	bag.Instance[UserRepository](doubles, &fakeUsers{prefix: "u-"})
	bag.Instance[Clock](doubles, &fixedClock{at: "noon"})

	app := bag.New()
	bag.Scoped(app, func(r mokkit.Resolver) *Service {
		return &Service{Users: mokkit.Resolve[UserRepository](r), Clock: mokkit.Resolve[Clock](r)}
	})

	stage := setupWith(t, doubles, app).EnterStage(t)

	if got := mokkit.Resolve[*Service](stage).Describe(); got != "u-1@noon" {
		t.Errorf("expected the subject to be built over the registered doubles, got %q", got)
	}
}

func TestAFactoryCanResolveThroughTheStageHost(t *testing.T) {
	// A step resolves with a method on its Host rather than the free function,
	// and must reach exactly what the free one does.
	b := bag.New()
	bag.Instance[Clock](b, &fixedClock{at: "noon"})

	stage := setupWith(t, b).EnterStage(t)
	h := stage.Host()

	if h.Resolve[Clock]().Now() != "noon" {
		t.Error("expected the registered clock")
	}
	if _, ok := h.TryResolve[UserRepository](); ok {
		t.Error("nothing was registered as UserRepository")
	}
}

func TestConcurrentResolveBuildsExactlyOnce(t *testing.T) {
	// All's branches resolve at the same time; two instances would mean one
	// branch arranging a double the subject never sees.
	var mu sync.Mutex
	builds := 0

	b := bag.New()
	bag.Instance[UserRepository](b, &fakeUsers{prefix: "u-"})
	bag.Instance[Clock](b, &fixedClock{at: "noon"})
	bag.Scoped(b, func(r mokkit.Resolver) *Service {
		mu.Lock()
		builds++
		mu.Unlock()

		return &Service{Users: mokkit.Resolve[UserRepository](r), Clock: mokkit.Resolve[Clock](r)}
	})

	stage := setupWith(t, b).EnterStage(t)

	const n = 32
	got := make([]*Service, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)

		go func() {
			defer wg.Done()
			got[i] = mokkit.Resolve[*Service](stage)
		}()
	}
	wg.Wait()

	if builds != 1 {
		t.Errorf("expected exactly one build, got %d", builds)
	}
	for i := range got {
		if got[i] != got[0] {
			t.Fatalf("concurrent resolves disagreed at %d", i)
		}
	}
}

func TestInstancesSurviveTheStageAndScopedValuesAreClosedNewestFirst(t *testing.T) {
	// An Instance is shared by every stage, so closing it with the first test
	// would hand the second a dead one; a Scoped value belongs to one stage and
	// is released newest first, so a service is closed before what it was built
	// over.
	log := &closeLog{}

	b := bag.New()
	bag.Instance[Clock](b, &closingClock{at: "noon", log: log})
	bag.Scoped(b, func(mokkit.Resolver) *closingUsers { return &closingUsers{prefix: "u-", log: log} })
	bag.Scoped(b, func(r mokkit.Resolver) *Service {
		return &Service{
			Users: mokkit.Resolve[*closingUsers](r),
			Clock: mokkit.Resolve[Clock](r),
			log:   log,
		}
	})

	setup := setupWith(t, b)

	var svc *Service
	t.Run("inner", func(t *testing.T) {
		svc = mokkit.Resolve[*Service](setup.EnterStage(t))
		if svc.closed {
			t.Fatal("the service must stay open for the test")
		}
	})

	if !svc.closed {
		t.Error("a scoped io.Closer must be closed when its stage ends")
	}
	if got := strings.Join(log.names(), ", "); got != "service, users" {
		t.Errorf("expected the service closed before what it was built over, got %q", got)
	}
	if log.count("clock") != 0 {
		t.Error("an Instance is the caller's to close, not the stage's")
	}
}

func TestAliasExposesOnePerStageInstanceUnderBothTypes(t *testing.T) {
	b := bag.New()
	bag.Scoped(b, func(mokkit.Resolver) *fakeUsers { return &fakeUsers{prefix: "u-"} })
	bag.Alias[UserRepository, *fakeUsers](b)

	setup := setupWith(t, b)
	stage := setup.EnterStage(t)

	concrete := mokkit.Resolve[*fakeUsers](stage)
	if mokkit.Resolve[UserRepository](stage) != UserRepository(concrete) {
		t.Error("the alias must resolve to the very instance the vocabulary arranges")
	}
	// Still per-stage: a new test gets its own.
	if mokkit.Resolve[*fakeUsers](setup.EnterStage(t)) == concrete {
		t.Error("an aliased scoped instance must not leak between stages")
	}
}

func TestAnAliasClosesTheInstanceExactlyOnce(t *testing.T) {
	// An alias owns nothing. The earlier one registered a second Scoped factory
	// for the target, so the one instance landed on the closer list twice and
	// was closed twice — a double Close on a real client is a panic, and on a
	// double it is a lie about what the test did.
	t.Run("resolved through the interface only", func(t *testing.T) {
		log := closesAfterOneStage(t, func(stage *mokkit.Stage) {
			if mokkit.Resolve[UserRepository](stage).ByID("1") != "u-1" {
				t.Error("expected the aliased double")
			}
		})

		if got := log.count("users"); got != 1 {
			t.Errorf("expected exactly one Close, got %d", got)
		}
	})

	t.Run("resolved both ways", func(t *testing.T) {
		log := closesAfterOneStage(t, func(stage *mokkit.Stage) {
			concrete := mokkit.Resolve[*closingUsers](stage)
			if mokkit.Resolve[UserRepository](stage) != UserRepository(concrete) {
				t.Error("both types must resolve to one instance")
			}
		})

		if got := log.count("users"); got != 1 {
			t.Errorf("expected exactly one Close, got %d", got)
		}
	})
}

// closesAfterOneStage composes an aliased scoped double, runs use against a
// stage that ends before this returns, and reports what that stage closed.
func closesAfterOneStage(t *testing.T, use func(*mokkit.Stage)) *closeLog {
	t.Helper()

	log := &closeLog{}
	b := bag.New()
	bag.Scoped(b, func(mokkit.Resolver) *closingUsers { return &closingUsers{prefix: "u-", log: log} })
	bag.Alias[UserRepository, *closingUsers](b)

	setup := setupWith(t, b)

	// The subtest is what gives the stage a lifetime: its cleanup closes the
	// scope, so the assertions above run against a stage that has ended.
	t.Run("stage", func(t *testing.T) { use(setup.EnterStage(t)) })

	return log
}

func TestAliasingATypeThatCannotBeUsedAsTheTargetPanicsAtRegistration(t *testing.T) {
	b := bag.New()
	bag.Scoped(b, func(mokkit.Resolver) *fixedClock { return &fixedClock{at: "noon"} })

	// The check is at registration rather than at resolve, so a mis-wired
	// composition fails on the line that wired it instead of in whichever test
	// happens to resolve the interface first.
	defer expectPanic(t, "bag: *bag_test.fixedClock cannot be used as bag_test.UserRepository")
	bag.Alias[UserRepository, *fixedClock](b)
}

func TestResolvingFromAScopeAfterTheStageClosedPanics(t *testing.T) {
	b := bag.New()
	bag.Scoped(b, func(mokkit.Resolver) *fakeUsers { return &fakeUsers{prefix: "u-"} })

	container, err := b.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// The scope is opened directly, because a closed stage has already dropped
	// its scopes: what is under test is the scope a goroutine outliving its
	// test would still be holding.
	scope, err := container.BeginScope(context.Background(), mokkit.StageContext{T: t, StageID: t.Name()})
	if err != nil {
		t.Fatalf("BeginScope: %v", err)
	}
	if mokkit.Resolve[*fakeUsers](scope).ByID("1") != "u-1" {
		t.Fatal("expected the double while the scope is open")
	}
	if err := scope.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	defer expectPanic(t, "resolved after the stage closed")
	mokkit.Resolve[*fakeUsers](scope)
}

func TestRegisteringTheSameTypeTwicePanics(t *testing.T) {
	defer expectPanic(t, "already registered")
	b := bag.New()
	bag.Instance[Clock](b, &fixedClock{at: "noon"})
	bag.Instance[Clock](b, &fixedClock{at: "midnight"})
}

func TestRegisteringAfterBuildPanics(t *testing.T) {
	b := bag.New()
	if _, err := b.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer expectPanic(t, "after the container was built")
	bag.Instance[Clock](b, &fixedClock{at: "noon"})
}

func TestRegisteredReportsWhatIsThere(t *testing.T) {
	b := bag.New()
	if bag.Registered[Clock](b) {
		t.Error("nothing registered yet")
	}
	bag.Instance[Clock](b, &fixedClock{at: "noon"})
	if !bag.Registered[Clock](b) {
		t.Error("expected Clock to be registered")
	}
}

func TestAnUnregisteredTypeIsNotResolved(t *testing.T) {
	stage := setupWith(t, bag.New()).EnterStage(t)
	if _, ok := mokkit.TryResolve[Clock](stage); ok {
		t.Error("nothing was registered")
	}
}

// --- test support ------------------------------------------------------------

// closeLog records what a scope closed, in order, so a test can tell one Close
// from two and newest-first from oldest-first.
type closeLog struct {
	mu     sync.Mutex
	closed []string
}

func (l *closeLog) record(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.closed = append(l.closed, name)
}

func (l *closeLog) names() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return slices.Clone(l.closed)
}

func (l *closeLog) count(name string) int {
	n := 0
	for _, got := range l.names() {
		if got == name {
			n++
		}
	}

	return n
}

// closingUsers is a double that owns a resource, which is what makes it worth
// aliasing: the vocabulary arranges it concretely, the subject receives it as a
// UserRepository, and only one of those may close it.
type closingUsers struct {
	prefix string
	log    *closeLog
}

func (u *closingUsers) ByID(id string) string { return u.prefix + id }

func (u *closingUsers) Close() error {
	u.log.record("users")

	return nil
}

// closingClock is registered as an Instance, so a stage must leave it open.
type closingClock struct {
	at  string
	log *closeLog
}

func (c *closingClock) Now() string { return c.at }

func (c *closingClock) Close() error {
	c.log.record("clock")

	return nil
}

// recordingTB is a mokkit.TB that captures reports instead of failing. Fatalf
// calls runtime.Goexit exactly as *testing.T does, so callers must use
// runGoexit.
type recordingTB struct {
	name     string
	mu       sync.Mutex
	errors   []string
	fatals   []string
	cleanups []func()
	failed   bool
}

func (r *recordingTB) Helper()      {}
func (r *recordingTB) Name() string { return r.name }

func (r *recordingTB) Cleanup(fn func()) {
	r.mu.Lock()
	r.cleanups = append(r.cleanups, fn)
	r.mu.Unlock()
}

func (r *recordingTB) Failed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.failed
}
func (r *recordingTB) FailNow() { r.mu.Lock(); r.failed = true; r.mu.Unlock(); runtime.Goexit() }

func (r *recordingTB) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.errors = append(r.errors, fmt.Sprintf(format, args...))
	r.failed = true
}

func (r *recordingTB) Fatalf(format string, args ...any) {
	r.mu.Lock()
	r.fatals = append(r.fatals, fmt.Sprintf(format, args...))
	r.failed = true
	r.mu.Unlock()
	runtime.Goexit()
}

// Fatals reports what was fatal'd, copied, so an assertion does not read the
// slice while a step is still appending to it.
func (r *recordingTB) Fatals() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.fatals)
}

func runGoexit(fn func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	<-done
}

// expectPanic is always used as `defer expectPanic(t, ...)`, which makes it the
// deferred function itself — so recover here is direct and does work.
//
//nolint:revive // the rule cannot see the deferred call site.
func expectPanic(t *testing.T, want string) {
	t.Helper()

	r := recover()
	if r == nil {
		t.Fatalf("expected a panic containing %q", want)
	}
	if got, ok := r.(string); !ok || !strings.Contains(got, want) {
		t.Fatalf("expected a panic containing %q, got %v", want, r)
	}
}
