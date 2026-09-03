// Package allure is a mokkit.Observer that writes Allure 2 result files, so
// any Allure consumer — TestOps, the allure CLI — renders a mokkit suite with
// each test reading as its scenario. It leans on nothing outside the standard
// library, because the core module is dependency-free and allure-results is
// plain JSON files in a directory.
package allure

import (
	"crypto/md5" //nolint:gosec // md5 is Allure's historyId convention, not a security boundary.
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	mokkit "github.com/GrafGenerator/go-mokkit"
)

// Allure's status vocabulary. "broken" is the report's word for a crash — the
// test infrastructure gave way, as opposed to an assertion honestly failing.
const (
	statusPassed = "passed"
	statusFailed = "failed"
	statusBroken = "broken"
)

// stageFinished is the only stage this reporter ever writes: a result exists
// only once its mokkit stage has closed, so there is nothing in-progress to
// report.
const stageFinished = "finished"

// defaultSuite names the suite when WithSuite was not used.
const defaultSuite = "mokkit"

const (
	dirPerm  = 0o755
	filePerm = 0o644
)

// An Option configures a Reporter at construction.
type Option func(*Reporter)

// WithSuite sets the suite label and the fullName prefix. The fullName feeds
// historyId, so renaming the suite re-keys every test's history in TestOps —
// pick the name once.
func WithSuite(name string) Option {
	return func(r *Reporter) { r.suite = name }
}

// A Reporter is a mokkit.Observer that writes one Allure result file per stage
// closed. It is safe for concurrent use, as the Observer contract demands.
//
// An Observer must never fail a test, so write failures are collected rather
// than reported; a TestMain that cares checks Err after m.Run.
type Reporter struct {
	dir   string
	suite string

	mu       sync.Mutex
	inflight map[string]*inflight
	errs     []error
}

// inflight is one stage between StageEntered and StageClosed: what the result
// file will say, accumulated as the events arrive.
type inflight struct {
	test  string
	start time.Time
	steps []stepResult
}

// New creates the results directory and returns a Reporter writing into it. It
// errors when the directory cannot be created — the one moment failing is
// still cheap, since no test has run yet.
func New(dir string, opts ...Option) (*Reporter, error) {
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, err
	}

	r := &Reporter{dir: dir, suite: defaultSuite, inflight: make(map[string]*inflight)}
	for _, o := range opts {
		o(r)
	}

	return r, nil
}

// Err reports every write failure so far, joined. It is how a TestMain learns
// the report is incomplete without any test having been failed over it.
func (r *Reporter) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	return errors.Join(r.errs...)
}

// StageEntered opens the stage's in-flight result, stamping receipt time as
// the test's start.
func (r *Reporter) StageEntered(test, stageID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inflight[stageID] = &inflight{test: test, start: time.Now()}
}

// StepRan appends one flat step in arrival order. Phases interleave legally in
// mokkit — a suite may Arrange again after Act — so steps are not grouped by
// phase; the phase rides in the name instead.
func (r *Reporter) StepRan(e mokkit.StepEvent) {
	step := stepResult{
		Name:   e.Phase + ": " + e.Step,
		Status: stepStatus(e.Err),
		Stage:  stageFinished,
		Start:  e.Started.UnixMilli(),
		Stop:   e.Started.Add(e.Duration).UnixMilli(),
	}
	if e.Err != nil {
		step.StatusDetails = &statusDetails{Message: firstLine(e.Err.Error()), Trace: e.Err.Error()}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.inflight[e.StageID]
	if !ok {
		return
	}
	f.steps = append(f.steps, step)
}

// StageClosed writes the stage's result file. A stage entered but never closed
// never writes — no partial files for a test binary that died mid-run.
func (r *Reporter) StageClosed(_, stageID string, failed bool) {
	stop := time.Now()

	r.mu.Lock()
	f, ok := r.inflight[stageID]
	delete(r.inflight, stageID)
	r.mu.Unlock()
	if !ok {
		return
	}

	// Marshal and write outside the lock, so a slow disk never stalls the
	// other stages' events.
	if err := r.write(r.buildResult(f, stop, failed)); err != nil {
		r.mu.Lock()
		r.errs = append(r.errs, err)
		r.mu.Unlock()
	}
}

func (r *Reporter) buildResult(f *inflight, stop time.Time, failed bool) result {
	fullName := r.suite + "/" + f.test
	steps := f.steps
	if steps == nil {
		// An empty list, not null: consumers vary in how forgiving their
		// parsers are.
		steps = []stepResult{}
	}

	res := result{
		UUID:      newUUID(),
		HistoryID: historyID(fullName),
		Name:      f.test,
		FullName:  fullName,
		Status:    statusPassed,
		Stage:     stageFinished,
		Start:     f.start.UnixMilli(),
		Stop:      stop.UnixMilli(),
		Labels: []label{
			{Name: "language", Value: "go"},
			{Name: "framework", Value: "mokkit"},
			{Name: "suite", Value: r.suite},
			{Name: "package", Value: r.suite},
		},
		Steps: steps,
	}
	if failed {
		res.Status, res.StatusDetails = verdict(steps)
	}

	return res
}

// verdict decides what a failed test's result says. A broken step outranks a
// failed one even when the failure came first, because a crash is the louder
// fact; a test that failed with no step to blame is reported as exactly that,
// since an assertion not routed through a chain does happen and hiding it
// would make the report lie.
func verdict(steps []stepResult) (string, *statusDetails) {
	for i := range steps {
		if steps[i].Status == statusBroken {
			return statusBroken, steps[i].StatusDetails
		}
	}
	for i := range steps {
		if steps[i].Status == statusFailed {
			return statusFailed, steps[i].StatusDetails
		}
	}

	return statusFailed, &statusDetails{
		Message: "the test failed outside mokkit's steps (an assertion not routed through a chain)",
	}
}

func (r *Reporter) write(res result) error {
	data, err := json.Marshal(res)
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(r.dir, res.UUID+"-result.json"), data, filePerm)
}

// stepStatus maps a step's error to Allure's word for it: nil passed, a panic
// broken, anything else failed.
func stepStatus(err error) string {
	if err == nil {
		return statusPassed
	}

	var p *mokkit.PanicError
	if errors.As(err, &p) {
		return statusBroken
	}

	return statusFailed
}

// firstLine keeps statusDetails.message scannable — the full text, stack
// included, lives in trace.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}

	return s
}

// historyID keys a test's history in TestOps: md5 of the fullName, hex — the
// convention Allure adapters share, stable across runs by construction.
func historyID(fullName string) string {
	sum := md5.Sum([]byte(fullName)) //nolint:gosec // md5 is Allure's historyId convention, not a security boundary.

	return hex.EncodeToString(sum[:])
}

// newUUID renders 16 random bytes in the canonical 8-4-4-4-12 shape, with the
// version and variant bits set so consumers that validate see a v4 UUID. Done
// by hand because pulling a uuid module for one filename would be the module's
// first dependency.
func newUUID() string {
	var b [16]byte
	// crypto/rand.Read is documented never to fail on supported platforms.
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40 //nolint:mnd // RFC 4122 version bits.
	b[8] = b[8]&0x3f | 0x80 //nolint:mnd // RFC 4122 variant bits.

	h := hex.EncodeToString(b[:])

	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// result is one Allure 2 test result, the schema of a <uuid>-result.json.
type result struct {
	UUID          string         `json:"uuid"`
	HistoryID     string         `json:"historyId"`
	Name          string         `json:"name"`
	FullName      string         `json:"fullName"`
	Status        string         `json:"status"`
	StatusDetails *statusDetails `json:"statusDetails,omitempty"`
	Stage         string         `json:"stage"`
	Start         int64          `json:"start"`
	Stop          int64          `json:"stop"`
	Labels        []label        `json:"labels"`
	Steps         []stepResult   `json:"steps"`
}

type stepResult struct {
	Name          string         `json:"name"`
	Status        string         `json:"status"`
	StatusDetails *statusDetails `json:"statusDetails,omitempty"`
	Stage         string         `json:"stage"`
	Start         int64          `json:"start"`
	Stop          int64          `json:"stop"`
}

type statusDetails struct {
	Message string `json:"message"`
	Trace   string `json:"trace,omitempty"`
}

type label struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
