package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/blashkar/evalwright/internal/runner"
	"github.com/blashkar/evalwright/internal/suite"
)

// execute runs the CLI with args and returns stdout and stderr.
func execute(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return executeContext(context.Background(), t, args...)
}

func executeContext(ctx context.Context, t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err = cmd.ExecuteContext(ctx)
	return out.String(), errOut.String(), err
}

func writeSuite(t *testing.T, src string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "suite.yaml")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInitWritesValidSuiteOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evalwright.yaml")

	if _, _, err := execute(t, "init", path); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := suite.Load(path); err != nil {
		t.Errorf("init wrote an invalid suite: %v", err)
	}
	if _, _, err := execute(t, "init", path); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("second init error = %v, want refusal to overwrite", err)
	}
}

func TestRunOfflineExample(t *testing.T) {
	resultsPath := filepath.Join(t.TempDir(), "results.json")
	stdout, stderr, err := execute(t, "run", "../../examples/offline.yaml", "-o", resultsPath, "--parallel", "2")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, "PASS  offline") {
		t.Errorf("stdout missing PASS verdict:\n%s", stdout)
	}
	// Progress goes to stderr so stdout stays clean for piping.
	if !strings.Contains(stderr, "▶ mock:echo") || !strings.Contains(stderr, "✓ password-reset #1") {
		t.Errorf("stderr missing progress:\n%s", stderr)
	}
	if strings.Contains(stdout, "▶") {
		t.Errorf("progress leaked into stdout:\n%s", stdout)
	}

	b, err := os.ReadFile(resultsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(b) {
		t.Errorf("results file is not valid JSON:\n%s", b)
	}
}

func TestRunQuiet(t *testing.T) {
	_, stderr, err := execute(t, "run", "../../examples/offline.yaml", "-q")
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Errorf("--quiet still printed progress:\n%s", stderr)
	}
}

func TestRunFailingSuiteReturnsErrSuiteFailed(t *testing.T) {
	path := writeSuite(t, "name: x\nmodels: [mock:echo]\nprompt: hello\ncases: [{assert: [{contains: goodbye}]}]\n")
	stdout, _, err := execute(t, "run", path)
	if !errors.Is(err, errSuiteFailed) {
		t.Fatalf("run error = %v, want errSuiteFailed", err)
	}
	if !strings.Contains(stdout, `output does not contain "goodbye"`) {
		t.Errorf("output missing failure detail:\n%s", stdout)
	}
}

func TestRunOllamaUnreachable(t *testing.T) {
	t.Setenv("OLLAMA_HOST", "127.0.0.1:1") // nothing listens on port 1
	path := writeSuite(t, "name: x\nmodels: [ollama:llama3.2:3b]\nprompt: hi\ncases: [{assert: [{contains: hi}]}]\n")

	_, _, err := execute(t, "run", path)
	if err == nil || errors.Is(err, errSuiteFailed) || !strings.Contains(err.Error(), "is it running?") {
		t.Errorf("error = %v, want a configuration error telling the user to start Ollama", err)
	}
}

func TestRunRejectsBadParallel(t *testing.T) {
	_, _, err := execute(t, "run", "../../examples/offline.yaml", "--parallel", "0")
	if err == nil || !strings.Contains(err.Error(), "--parallel must be at least 1") {
		t.Errorf("error = %v", err)
	}
}

func TestRunPrintsSuiteWarningsEvenWhenQuiet(t *testing.T) {
	path := writeSuite(t, "name: x\nmodels: [mock:echo]\noptions: {temperature: 0}\nsamples: 3\nprompt: hi\ncases: [{assert: [{contains: hi}]}]\n")
	_, stderr, err := execute(t, "run", path, "-q")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "warning: ") || !strings.Contains(stderr, "every sample gives the same answer") {
		t.Errorf("stderr missing temperature warning:\n%s", stderr)
	}
}

func TestRunChecksEverySuiteBeforeRunningAny(t *testing.T) {
	bad := writeSuite(t, "name: x\nmodels: [mock:echo]\nprompt: hi\ncases: [{assert: [{contans: hi}]}]\n")
	resultsPath := filepath.Join(t.TempDir(), "results.json")

	stdout, _, err := execute(t, "run", "../../examples/offline.yaml", bad, "-o", resultsPath)
	if err == nil || !strings.Contains(err.Error(), bad) {
		t.Fatalf("error = %v, want one naming %s", err, bad)
	}
	if stdout != "" {
		t.Errorf("a suite ran before the broken one was reported:\n%s", stdout)
	}
	if _, err := os.Stat(resultsPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("results file written for a run that never started: %v", err)
	}
}

func TestRunInterruptedStillWritesResults(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A fake Ollama that answers the first case, then gets Ctrl-C during the second.
	var mu sync.Mutex
	chats := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/show" {
			fmt.Fprint(w, `{}`)
			return
		}
		var body struct{ Messages []any }
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Messages) == 0 { // warm-up
			fmt.Fprint(w, `{"done":true}`)
			return
		}
		mu.Lock()
		chats++
		n := chats
		mu.Unlock()
		if n == 1 {
			fmt.Fprint(w, `{"message":{"role":"assistant","content":"hi"},"done":true}`)
			return
		}
		cancel()
		<-r.Context().Done()
	}))
	defer srv.Close()
	t.Setenv("OLLAMA_HOST", srv.URL)

	path := writeSuite(t, "name: x\nmodels: [ollama:m]\nprompt: hi\ncases: [{assert: [{contains: hi}]}, {assert: [{contains: hi}]}]\n")
	resultsPath := filepath.Join(t.TempDir(), "results.json")
	_, _, err := executeContext(ctx, t, "run", path, "-o", resultsPath)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}

	b, err := os.ReadFile(resultsPath)
	if err != nil {
		t.Fatalf("partial results were not saved: %v", err)
	}
	var reps []runner.Report
	if err := json.Unmarshal(b, &reps); err != nil {
		t.Fatal(err)
	}
	if len(reps) != 1 || !reps[0].Interrupted || len(reps[0].Results) != 1 || !reps[0].Results[0].Pass {
		t.Errorf("saved results = %s, want 1 interrupted report with the 1 finished case", b)
	}
}
