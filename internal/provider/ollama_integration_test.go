//go:build ollama

// Integration tests against a real Ollama server. They don't run in CI;
// run them locally with `make test-ollama`. The model defaults to
// llama3.2:3b and can be changed with EVALWRIGHT_TEST_MODEL.
package provider

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/blashkar/evalwright/internal/ollama"
)

func TestOllamaIntegration(t *testing.T) {
	model := os.Getenv("EVALWRIGHT_TEST_MODEL")
	if model == "" {
		model = "llama3.2:3b"
	}
	client, err := ollama.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, err := client.Version(ctx); err != nil {
		t.Skipf("Ollama not reachable: %v", err)
	}

	o := &Ollama{Client: client, AutoPull: true, Log: testLog{t}}
	info, err := o.Prepare(ctx, model)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if info.Digest == "" || info.ParameterSize == "" {
		t.Errorf("Prepare returned no model details: %+v", info)
	}
	if err := o.Warm(ctx, model, nil); err != nil {
		t.Fatalf("Warm: %v", err)
	}

	resp, err := o.Generate(ctx, Request{
		Model:   model,
		System:  "You reply with exactly one lowercase word and nothing else.",
		Prompt:  "Reply with the word: pong",
		Options: map[string]any{"temperature": 0, "seed": 1},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	t.Logf("%s answered %q in %s", model, resp.Text, resp.Latency)
	if !strings.Contains(strings.ToLower(resp.Text), "pong") {
		t.Errorf("response %q does not contain pong", resp.Text)
	}
	m := resp.Metrics
	t.Logf("metrics: first token %s, %d prompt tokens, %d output tokens, %.0f tokens/s",
		m.TimeToFirstToken, m.PromptTokens, m.OutputTokens, m.TokensPerSecond())
	if m.TimeToFirstToken <= 0 || m.TimeToFirstToken > resp.Latency || m.PromptTokens == 0 || m.OutputTokens == 0 || m.TokensPerSecond() <= 0 {
		t.Errorf("metrics missing or inconsistent: %+v (latency %s)", m, resp.Latency)
	}

	// AutoPull is on, so the pull itself must fail for a model that doesn't exist.
	if _, err := o.Prepare(ctx, "evalwright-no-such-model:0b"); err == nil {
		t.Error("Prepare of a nonexistent model succeeded")
	} else {
		t.Logf("nonexistent model: %v", err)
	}
}

type testLog struct{ t *testing.T }

func (l testLog) Write(p []byte) (int, error) {
	l.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}
