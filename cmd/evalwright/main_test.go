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
)

// execute runs the CLI with args and returns its stdout.
func execute(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

func TestInitThenRunExample(t *testing.T) {
	dir := t.TempDir()
	suitePath := filepath.Join(dir, "evalwright.yaml")
	resultsPath := filepath.Join(dir, "results.json")

	if _, err := execute(t, "init", suitePath); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := execute(t, "init", suitePath); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("second init error = %v, want refusal to overwrite", err)
	}

	out, err := execute(t, "run", suitePath, "-o", resultsPath)
	if err != nil {
		t.Fatalf("run example suite: %v\n%s", err, out)
	}
	if !strings.Contains(out, "PASS  example") {
		t.Errorf("output missing PASS verdict:\n%s", out)
	}

	b, err := os.ReadFile(resultsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(b) {
		t.Errorf("results file is not valid JSON:\n%s", b)
	}
}

func TestRunFailingSuiteReturnsErrSuiteFailed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.yaml")
	src := "name: x\nmodels: [mock:echo]\nprompt: hello\ncases: [{assert: [{contains: goodbye}]}]\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, "run", path)
	if !errors.Is(err, errSuiteFailed) {
		t.Fatalf("run error = %v, want errSuiteFailed", err)
	}
	if !strings.Contains(out, `output does not contain "goodbye"`) {
		t.Errorf("output missing failure detail:\n%s", out)
	}
}
