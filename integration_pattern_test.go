package mokkit_test

import (
	"context"
	"os"
	"sync/atomic"
	"testing"

	"github.com/GrafGenerator/go-mokkit"
	"github.com/GrafGenerator/go-mokkit/container/bag"
)

// This file is the answer to "how does this work for integration and end-to-end
// tests", where the composition owns a container, a pool or a broker and must
// not be rebuilt per test.
//
// The split is the same one C# makes: a Setup is composed once and is expensive;
// a Stage is a scope over it and is entered per test. What differs is only where
// the isolation comes from — a scoped unit of work that rolls back when the
// stage closes, rather than a framework reset.

// database stands in for the expensive thing: a container, a pool, a broker.
// It is built once and shared by every test.
type database struct {
	builds atomic.Int32
	rows   map[string]string
}

func (d *database) commit(rows map[string]string) {
	for k, v := range rows {
		d.rows[k] = v
	}
}

// unitOfWork is the per-stage scope over the shared database. Writes land in it,
// and are thrown away when the stage closes unless the test commits — which is
// what keeps tests isolated without resetting anything global.
type unitOfWork struct {
	db         *database
	pending    map[string]string
	committed  bool
	rolledBack bool
}

func (u *unitOfWork) write(key, value string) { u.pending[key] = value }

func (u *unitOfWork) read(key string) string {
	if v, ok := u.pending[key]; ok {
		return v
	}

	return u.db.rows[key]
}

func (u *unitOfWork) Commit() { u.db.commit(u.pending); u.committed = true }

// Close is what bag calls when the stage ends, so a test that does not commit
// leaves nothing behind.
func (u *unitOfWork) Close() error {
	if !u.committed {
		u.rolledBack = true
		u.pending = nil
	}

	return nil
}

// isolationUnit stands in for the other shape of isolation: the scope a test
// never talks to — a schema to drop, a set of tables to truncate — whose only
// job is to clean up on the way out. It counts what happened to it, because
// what matters about it is whether it was built and closed at all.
type isolationUnit struct{ log *isolationLog }

type isolationLog struct{ builds, closes atomic.Int32 }

func (u *isolationUnit) Close() error {
	u.log.closes.Add(1)

	return nil
}

var (
	integrationSetup *mokkit.Setup
	isolationSetup   *mokkit.Setup
	sharedDatabase   = &database{rows: map[string]string{}}
	isolation        = &isolationLog{}
)

// TestMain is where a suite's "run once" belongs. The composition is expensive
// and must be paid for once, and init is the wrong hook for it twice over: the
// order of package-level initialisation is not the reader's to see, and
// gochecknoinits — common in strict lint configs —
// rejects it outright.
func TestMain(m *testing.M) {
	integrationSetup = mustCompose(integrationComposition())
	isolationSetup = mustCompose(isolationComposition())

	os.Exit(m.Run())
}

func mustCompose(b *bag.Builder) *mokkit.Setup {
	setup, err := mokkit.NewSetup(context.Background(), b)
	if err != nil {
		panic("composing the integration suite: " + err.Error())
	}

	return setup
}

func integrationComposition() *bag.Builder {
	b := bag.New()

	// Instance: composed once, shared by every stage. This is where a
	// Testcontainer or a connection pool belongs.
	bag.Instance(b, sharedDatabase)
	sharedDatabase.builds.Add(1)

	// Scoped: opened per stage and closed with it. Because it implements
	// io.Closer, bag rolls it back on the way out.
	bag.Scoped(b, func(r mokkit.Resolver) *unitOfWork {
		return &unitOfWork{db: mokkit.Resolve[*database](r), pending: map[string]string{}}
	})

	return b
}

func integrationStage(t *testing.T) *mokkit.Stage {
	t.Helper()

	return integrationSetup.EnterStage(t)
}

func TestIntegration_TheExpensiveCompositionIsBuiltOnce(t *testing.T) {
	integrationStage(t)
	integrationStage(t)

	if got := sharedDatabase.builds.Load(); got != 1 {
		t.Errorf("the shared database must be built once, not %d times", got)
	}
}

func TestIntegration_AStageSeesOnlyItsOwnWrites(t *testing.T) {
	stage := integrationStage(t)

	work := mokkit.Resolve[*unitOfWork](stage)
	work.write("client:1", "Acme Corporation")

	if got := work.read("client:1"); got != "Acme Corporation" {
		t.Errorf("a stage must read its own uncommitted writes, got %q", got)
	}
}

func TestIntegration_UncommittedWorkIsRolledBackWithTheStage(t *testing.T) {
	var work *unitOfWork

	// The inner test's stage closes when it ends, exactly as a real one would.
	t.Run("writes without committing", func(t *testing.T) {
		work = mokkit.Resolve[*unitOfWork](integrationStage(t))
		work.write("client:2", "Globex")
	})

	if !work.rolledBack {
		t.Error("the unit of work must be rolled back when its stage closes")
	}
	if got := sharedDatabase.rows["client:2"]; got != "" {
		t.Errorf("nothing may reach the shared database, got %q", got)
	}

	// A later stage therefore starts clean, which is what makes the shared
	// composition safe to reuse.
	next := mokkit.Resolve[*unitOfWork](integrationStage(t))
	if got := next.read("client:2"); got != "" {
		t.Errorf("a fresh stage must not see the previous test's writes, got %q", got)
	}
}

// --- isolation must not depend on what a test happens to use -----------------

func isolationComposition() *bag.Builder {
	b := bag.New()

	// Scoped builds on first resolve. That is right for a service, and wrong
	// for isolation: see enterIsolatedStage.
	bag.Scoped(b, func(mokkit.Resolver) *isolationUnit {
		isolation.builds.Add(1)

		return &isolationUnit{log: isolation}
	})

	return b
}

// enterIsolatedStage is what a fixture does for a suite whose isolation is a
// scope rather than a rollback the test drives: it resolves the unit while
// entering, so the unit exists, so its Close is guaranteed to run. Isolation
// that only happens for tests that touched it is not isolation.
func enterIsolatedStage(t *testing.T) *mokkit.Stage {
	t.Helper()

	stage := isolationSetup.EnterStage(t)
	mokkit.Resolve[*isolationUnit](stage) // built now, so its Close is guaranteed

	return stage
}

func TestIntegration_AnIsolationScopeMustBeResolvedEagerly(t *testing.T) {
	builds, closes := isolation.builds.Load(), isolation.closes.Load()

	// A test that never touches the unit: bag never builds it, so there is
	// nothing to close, and the suite's isolation silently did not happen.
	t.Run("entered without resolving", func(t *testing.T) {
		isolationSetup.EnterStage(t)
	})

	if got := isolation.builds.Load(); got != builds {
		t.Errorf("a scoped unit must not be built until something resolves it, got %d builds", got-builds)
	}
	if got := isolation.closes.Load(); got != closes {
		t.Errorf("what was never built cannot clean up, got %d closes", got-closes)
	}

	// The same test through the fixture, which resolves at stage entry.
	t.Run("entered through the fixture", func(t *testing.T) {
		enterIsolatedStage(t)
	})

	if got := isolation.builds.Load(); got != builds+1 {
		t.Errorf("entering must build the isolation unit, got %d builds", got-builds)
	}
	if got := isolation.closes.Load(); got != closes+1 {
		t.Errorf("the stage must close the isolation unit exactly once, got %d closes", got-closes)
	}
}

// --- one Setup per configuration, not per test -------------------------------

// When the subject itself is built from configuration that varies per test, one
// Setup per configuration is the answer — cached, so the expensive part is still
// paid once each.
type suiteConfig struct{ strictMode bool }

var configuredSetups = map[suiteConfig]*mokkit.Setup{}

func setupFor(t *testing.T, cfg suiteConfig) *mokkit.Setup {
	t.Helper()

	if s, ok := configuredSetups[cfg]; ok {
		return s
	}

	b := bag.New()
	bag.Instance(b, sharedDatabase)
	bag.Instance(b, cfg)

	s, err := mokkit.NewSetup(context.Background(), b)
	if err != nil {
		t.Fatalf("composing for %+v: %v", cfg, err)
	}
	configuredSetups[cfg] = s

	return s
}

func TestIntegration_OneSetupPerConfigurationNotPerTest(t *testing.T) {
	strict := setupFor(t, suiteConfig{strictMode: true})
	lenient := setupFor(t, suiteConfig{strictMode: false})

	if strict == lenient {
		t.Fatal("different configurations are different subjects and need their own composition")
	}
	if again := setupFor(t, suiteConfig{strictMode: true}); again != strict {
		t.Error("a configuration already composed must be reused, not rebuilt")
	}

	if got := mokkit.Resolve[suiteConfig](strict.EnterStage(t)); !got.strictMode {
		t.Error("the stage must resolve its own composition's configuration")
	}

	// Both compositions still share the one expensive dependency.
	if got := sharedDatabase.builds.Load(); got != 1 {
		t.Errorf("the shared database must still be built once, got %d", got)
	}
}
