package allure_test

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	mokkit "github.com/GrafGenerator/go-mokkit"
	"github.com/GrafGenerator/go-mokkit/report/allure"
)

// stubTB is the minimal mokkit.TB these tests drive stages with — mokkit's own
// fakeTB is internal to the root package. Fatalf calls runtime.Goexit exactly
// as *testing.T does, so anything that may fatal runs under runGoexit.
type stubTB struct {
	name string

	mu       sync.Mutex
	failed   bool
	cleanups []func()
}

func (s *stubTB) Helper()      {}
func (s *stubTB) Name() string { return s.name }

func (s *stubTB) Cleanup(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanups = append(s.cleanups, fn)
}

func (s *stubTB) Errorf(string, ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failed = true
}

func (s *stubTB) Fatalf(string, ...any) {
	s.mu.Lock()
	s.failed = true
	s.mu.Unlock()
	runtime.Goexit()
}

func (s *stubTB) FailNow() {
	s.mu.Lock()
	s.failed = true
	s.mu.Unlock()
	runtime.Goexit()
}

func (s *stubTB) Failed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.failed
}

func (s *stubTB) runCleanups() {
	s.mu.Lock()
	cleanups := append([]func(){}, s.cleanups...)
	s.cleanups = nil
	s.mu.Unlock()

	for i := len(cleanups) - 1; i >= 0; i-- {
		cleanups[i]()
	}
}

// runGoexit runs fn on its own goroutine so a stubTB.Fatalf can Goexit without
// killing the test, and returns once fn has finished or been unwound.
func runGoexit(fn func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	<-done
}

// runStage drives one whole stage lifecycle — enter, body, cleanups — against
// a real mokkit Setup observed by r.
func runStage(t *testing.T, r *allure.Reporter, testName string, body func(tb *stubTB, stage *mokkit.Stage)) {
	t.Helper()

	setup, err := mokkit.NewSetup(context.Background())
	if err != nil {
		t.Fatalf("NewSetup: %v", err)
	}
	setup.Observe(r)

	tb := &stubTB{name: testName}
	runGoexit(func() {
		body(tb, setup.EnterStage(tb))
	})
	tb.runCleanups()
}

// resultFile mirrors the fields a result file must carry, for decoding.
type resultFile struct {
	UUID          string       `json:"uuid"`
	HistoryID     string       `json:"historyId"`
	Name          string       `json:"name"`
	FullName      string       `json:"fullName"`
	Status        string       `json:"status"`
	StatusDetails *detailsFile `json:"statusDetails"`
	Stage         string       `json:"stage"`
	Start         int64        `json:"start"`
	Stop          int64        `json:"stop"`
	Labels        []labelFile  `json:"labels"`
	Steps         []stepFile   `json:"steps"`
}

type detailsFile struct {
	Message string `json:"message"`
	Trace   string `json:"trace"`
}

type labelFile struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type stepFile struct {
	Name          string       `json:"name"`
	Status        string       `json:"status"`
	StatusDetails *detailsFile `json:"statusDetails"`
	Stage         string       `json:"stage"`
	Start         int64        `json:"start"`
	Stop          int64        `json:"stop"`
}

// readResults decodes every result file in dir, keyed by file name.
func readResults(t *testing.T, dir string) map[string]resultFile {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading results dir: %v", err)
	}

	results := make(map[string]resultFile, len(entries))
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		var res resultFile
		if err := json.Unmarshal(data, &res); err != nil {
			t.Fatalf("decoding %s: %v", e.Name(), err)
		}
		results[e.Name()] = res
	}

	return results
}

// readOneResult expects exactly one result file and decodes it.
func readOneResult(t *testing.T, dir string) resultFile {
	t.Helper()

	results := readResults(t, dir)
	if len(results) != 1 {
		t.Fatalf("want exactly one result file, got %d: %v", len(results), fileNames(results))
	}
	for name := range results {
		return results[name]
	}

	return resultFile{}
}

func fileNames(results map[string]resultFile) []string {
	names := make([]string, 0, len(results))
	for name := range results {
		names = append(names, name)
	}

	return names
}

func labelValue(res resultFile, name string) string {
	for _, l := range res.Labels {
		if l.Name == name {
			return l.Value
		}
	}

	return ""
}

func newReporter(t *testing.T, opts ...allure.Option) (r *allure.Reporter, dir string) {
	t.Helper()

	dir = filepath.Join(t.TempDir(), "allure-results")
	r, err := allure.New(dir, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	return r, dir
}

func TestAPassingTestWritesACompleteResult(t *testing.T) {
	r, dir := newReporter(t, allure.WithSuite("checkout"))

	runStage(t, r, "TestCheckout/paying", func(_ *stubTB, stage *mokkit.Stage) {
		stage.Arrange().Add("CartExists", func(context.Context, mokkit.Host) error { return nil })
		stage.Inspect().Add("TotalIsRight", func(context.Context, mokkit.Host) error { return nil })
	})

	res := readOneResult(t, dir)

	if res.UUID == "" || res.Name != "TestCheckout/paying" || res.FullName != "checkout/TestCheckout/paying" {
		t.Errorf("identity fields wrong: %+v", res)
	}
	wantHistory := hex.EncodeToString(md5sum("checkout/TestCheckout/paying"))
	if res.HistoryID != wantHistory {
		t.Errorf("historyId: want md5 of fullName %q, got %q", wantHistory, res.HistoryID)
	}
	if res.Status != "passed" || res.Stage != "finished" || res.StatusDetails != nil {
		t.Errorf("verdict fields wrong: %+v", res)
	}
	if res.Start <= 0 || res.Stop < res.Start {
		t.Errorf("timestamps wrong: start=%d stop=%d", res.Start, res.Stop)
	}
	for name, want := range map[string]string{
		"language": "go", "framework": "mokkit", "suite": "checkout", "package": "checkout",
	} {
		if got := labelValue(res, name); got != want {
			t.Errorf("label %s: want %q, got %q", name, want, got)
		}
	}

	if len(res.Steps) != 2 {
		t.Fatalf("want 2 steps, got %+v", res.Steps)
	}
	first, second := res.Steps[0], res.Steps[1]
	if first.Name != "arrange: CartExists" || second.Name != "inspect: TotalIsRight" {
		t.Errorf("step names wrong: %q, %q", first.Name, second.Name)
	}
	for _, s := range res.Steps {
		if s.Status != "passed" || s.Stage != "finished" || s.StatusDetails != nil || s.Stop < s.Start {
			t.Errorf("step fields wrong: %+v", s)
		}
	}
}

func md5sum(s string) []byte {
	sum := md5.Sum([]byte(s))

	return sum[:]
}

func TestAFailingStepMarksTheTestFailed(t *testing.T) {
	r, dir := newReporter(t)

	runStage(t, r, "TestOutOfStock", func(_ *stubTB, stage *mokkit.Stage) {
		stage.Arrange().Add("StockExists", func(context.Context, mokkit.Host) error {
			return errors.New("no stock left\nwarehouse said so")
		})
	})

	res := readOneResult(t, dir)

	if res.Status != "failed" {
		t.Errorf("want status failed, got %q", res.Status)
	}
	if res.StatusDetails == nil || res.StatusDetails.Message != "no stock left" {
		t.Errorf("want the error's first line as the message, got %+v", res.StatusDetails)
	}
	if !strings.Contains(res.StatusDetails.Trace, "warehouse said so") {
		t.Errorf("want the full error text as the trace, got %q", res.StatusDetails.Trace)
	}
	if len(res.Steps) != 1 || res.Steps[0].Status != "failed" || res.Steps[0].StatusDetails == nil {
		t.Errorf("step verdict wrong: %+v", res.Steps)
	}
}

func TestAPanickingStepMarksTheTestBroken(t *testing.T) {
	r, dir := newReporter(t)

	runStage(t, r, "TestCrash", func(_ *stubTB, stage *mokkit.Stage) {
		stage.Act().Add("Explodes", func(context.Context, mokkit.Host) error {
			panic("wires crossed")
		})
	})

	res := readOneResult(t, dir)

	if res.Status != "broken" {
		t.Errorf("want status broken, got %q", res.Status)
	}
	if res.StatusDetails == nil || !strings.Contains(res.StatusDetails.Message, "panic: wires crossed") {
		t.Errorf("want the panic in the message, got %+v", res.StatusDetails)
	}
	if len(res.Steps) != 1 || res.Steps[0].Status != "broken" {
		t.Errorf("want the step broken, got %+v", res.Steps)
	}
}

func TestAFailureOutsideStepsIsReportedHonestly(t *testing.T) {
	r, dir := newReporter(t)

	runStage(t, r, "TestSideChannel", func(tb *stubTB, stage *mokkit.Stage) {
		stage.Arrange().Add("AllFine", func(context.Context, mokkit.Host) error { return nil })
		// An assertion not routed through a chain: the test fails, and no
		// step carries the blame.
		tb.Errorf("assert.Equal went straight to t")
	})

	res := readOneResult(t, dir)

	if res.Status != "failed" {
		t.Errorf("want status failed, got %q", res.Status)
	}
	if res.StatusDetails == nil || !strings.Contains(res.StatusDetails.Message, "outside mokkit's steps") {
		t.Errorf("want an honest outside-steps message, got %+v", res.StatusDetails)
	}
	if len(res.Steps) != 1 || res.Steps[0].Status != "passed" {
		t.Errorf("the step itself passed and must say so: %+v", res.Steps)
	}
}

func TestConcurrentStagesWriteSeparateUncorruptedFiles(t *testing.T) {
	r, dir := newReporter(t)

	setup, err := mokkit.NewSetup(context.Background())
	if err != nil {
		t.Fatalf("NewSetup: %v", err)
	}
	setup.Observe(r)

	const stepsPerStage = 25
	var wg sync.WaitGroup
	for _, name := range []string{"TestLeft", "TestRight"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tb := &stubTB{name: name}
			stage := setup.EnterStage(tb)
			for i := range stepsPerStage {
				stage.Inspect().Add(fmt.Sprintf("Check%d", i), func(context.Context, mokkit.Host) error { return nil })
			}
			tb.runCleanups()
		}()
	}
	wg.Wait()

	results := readResults(t, dir)
	if len(results) != 2 {
		t.Fatalf("want two result files, got %v", fileNames(results))
	}

	seen := map[string]bool{}
	for name, res := range results {
		seen[res.Name] = true
		if len(res.Steps) != stepsPerStage {
			t.Errorf("%s: want %d steps for %s, got %d", name, stepsPerStage, res.Name, len(res.Steps))
		}
		if res.Status != "passed" {
			t.Errorf("%s: want passed, got %q", name, res.Status)
		}
	}
	if !seen["TestLeft"] || !seen["TestRight"] {
		t.Errorf("want one file per test, got %v", seen)
	}
}

func TestHistoryIDIsStableAcrossRunsWhileUUIDIsNot(t *testing.T) {
	r, dir := newReporter(t)

	for range 2 {
		runStage(t, r, "TestRetried", func(_ *stubTB, stage *mokkit.Stage) {
			stage.Act().Add("Runs", func(context.Context, mokkit.Host) error { return nil })
		})
	}

	results := readResults(t, dir)
	if len(results) != 2 {
		t.Fatalf("want two result files, got %v", fileNames(results))
	}

	all := make([]resultFile, 0, len(results))
	for _, res := range results {
		all = append(all, res)
	}
	if all[0].HistoryID != all[1].HistoryID {
		t.Errorf("historyId must be stable across runs: %q vs %q", all[0].HistoryID, all[1].HistoryID)
	}
	if all[0].UUID == all[1].UUID {
		t.Errorf("uuid must be unique per run, got %q twice", all[0].UUID)
	}
}

func TestErrSurfacesWriteFailures(t *testing.T) {
	r, dir := newReporter(t)
	if err := r.Err(); err != nil {
		t.Fatalf("a fresh reporter has nothing to report, got %v", err)
	}

	// The directory disappears between New and the first write — the write
	// fails, the test must not, and Err carries the story.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("removing results dir: %v", err)
	}

	runStage(t, r, "TestUnwritable", func(_ *stubTB, stage *mokkit.Stage) {
		stage.Act().Add("Runs", func(context.Context, mokkit.Host) error { return nil })
	})

	if err := r.Err(); err == nil {
		t.Error("want Err to surface the write failure, got nil")
	}
}
