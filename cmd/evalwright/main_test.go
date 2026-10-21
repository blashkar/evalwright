package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blashkar/evalwright/internal/suite"
)

// execute runs the CLI with args and returns stdout and stderr.
func execute(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err = cmd.ExecuteContext(context.Background())
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
