package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blashkar/evalwright/internal/ollama"
)

// fakeOllama is an in-memory Ollama server that knows about a set of
// installed models and records the chat requests it receives.
type fakeOllama struct {
	mu        sync.Mutex
	installed map[string]bool
	chats     []map[string]any
	loads     []map[string]any // bodies of load requests
}

func (f *fakeOllama) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	model, _ := body["model"].(string)

	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/api/show":
		if !f.installed[model] {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"error":"model '%s' not found"}`, model)
			return
		}
		fmt.Fprint(w, `{"details":{}}`)
	case "/api/pull":
		fmt.Fprintln(w, `{"status":"pulling manifest"}`)
		fmt.Fprintln(w, `{"status":"pulling abc","digest":"sha256:abc","total":2000000000,"completed":1000000000}`)
		fmt.Fprintln(w, `{"status":"pulling abc","digest":"sha256:abc","total":2000000000,"completed":2000000000}`)
		fmt.Fprintln(w, `{"status":"success"}`)
		f.installed[model] = true
	case "/api/chat":
		if msgs, _ := body["messages"].([]any); len(msgs) == 0 {
			f.loads = append(f.loads, body)
			fmt.Fprint(w, `{"done":true,"done_reason":"load"}`)
			return
		}
		f.chats = append(f.chats, body)
		fmt.Fprintln(w, `{"message":{"role":"assistant","content":"Par"},"done":false}`)
		fmt.Fprintln(w, `{"message":{"role":"assistant","content":"is"},"done":false}`)
		fmt.Fprintln(w, `{"message":{"role":"assistant","content":""},"done":true,"prompt_eval_count":12,"prompt_eval_duration":3000000,"eval_count":2,"eval_duration":40000000}`)
	default:
		http.NotFound(w, r)
	}
}

// The fake's state is written by the server goroutine, so tests read it under the lock.
func (f *fakeOllama) chat(i int) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.chats[i]
}

func (f *fakeOllama) isInstalled(model string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.installed[model]
}

func (f *fakeOllama) loaded() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loads
}

func newFakeOllama(t *testing.T, installed ...string) (*fakeOllama, *ollama.Client) {
	t.Helper()
	f := &fakeOllama{installed: map[string]bool{}}
	for _, m := range installed {
		f.installed[m] = true
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c, err := ollama.NewClient(srv.URL, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return f, c
}

func TestOllamaGenerate(t *testing.T) {
	fake, client := newFakeOllama(t, "llama3.2:3b")
	o := &Ollama{Client: client}

	resp, err := o.Generate(context.Background(), Request{
		Model:   "llama3.2:3b",
		System:  "Be brief.",
		Prompt:  "Capital of France?",
		Options: map[string]any{"temperature": 0},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Text != "Paris" {
		t.Errorf("Text = %q, want streamed chunks joined into Paris", resp.Text)
	}
	m := resp.Metrics
	if m.PromptTokens != 12 || m.PromptDuration != 3*time.Millisecond || m.OutputTokens != 2 || m.GenerationDuration != 40*time.Millisecond {
		t.Errorf("Metrics = %+v, want the counts and durations from the final chunk", m)
	}
	if m.TimeToFirstToken <= 0 || m.TimeToFirstToken > resp.Latency {
		t.Errorf("TimeToFirstToken = %s, want it measured and within the latency %s", m.TimeToFirstToken, resp.Latency)
	}
	if tps := m.TokensPerSecond(); tps != 50 {
		t.Errorf("TokensPerSecond = %v, want 2 tokens / 40ms = 50", tps)
	}

	got := fake.chat(0)
	msgs := got["messages"].([]any)
	if len(msgs) != 2 || msgs[0].(map[string]any)["role"] != "system" || msgs[1].(map[string]any)["content"] != "Capital of France?" {
		t.Errorf("messages sent = %v", msgs)
	}
	if got["options"].(map[string]any)["temperature"] != 0.0 {
		t.Errorf("options sent = %v", got["options"])
	}
}

func TestOllamaGenerateOmitsEmptySystem(t *testing.T) {
	fake, client := newFakeOllama(t, "m")
	if _, err := (&Ollama{Client: client}).Generate(context.Background(), Request{Model: "m", Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	if msgs := fake.chat(0)["messages"].([]any); len(msgs) != 1 {
		t.Errorf("sent %d messages, want only the user message", len(msgs))
	}
}

func TestOllamaPrepare(t *testing.T) {
	t.Run("installed", func(t *testing.T) {
		_, client := newFakeOllama(t, "llama3.2:3b")
		if err := (&Ollama{Client: client}).Prepare(context.Background(), "llama3.2:3b"); err != nil {
			t.Errorf("Prepare: %v", err)
		}
	})

	t.Run("missing without pull", func(t *testing.T) {
		_, client := newFakeOllama(t)
		err := (&Ollama{Client: client}).Prepare(context.Background(), "llama3.2:1b")
		if err == nil || !strings.Contains(err.Error(), "ollama pull llama3.2:1b") {
			t.Errorf("error = %v, want a hint to pull the model", err)
		}
	})

	t.Run("missing with pull", func(t *testing.T) {
		fake, client := newFakeOllama(t)
		var log bytes.Buffer
		o := &Ollama{Client: client, AutoPull: true, Log: &log}
		if err := o.Prepare(context.Background(), "llama3.2:1b"); err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		if !fake.isInstalled("llama3.2:1b") {
			t.Error("model was not pulled")
		}
		for _, want := range []string{"Pulling llama3.2:1b", " 50% of 2.0 GB", "100% of 2.0 GB"} {
			if !strings.Contains(log.String(), want) {
				t.Errorf("log missing %q:\n%s", want, log.String())
			}
		}
	})
}

func TestOllamaWarm(t *testing.T) {
	fake, client := newFakeOllama(t, "m")
	// num_ctx changes how a model is loaded, so the load must use the
	// suite's options or Ollama would reload the model on the first case.
	opts := map[string]any{"num_ctx": 8192}
	if err := (&Ollama{Client: client}).Warm(context.Background(), "m", opts); err != nil {
		t.Fatalf("Warm: %v", err)
	}
	loads := fake.loaded()
	if len(loads) != 1 || loads[0]["model"] != "m" {
		t.Fatalf("loads = %v, want one load of m", loads)
	}
	if got, _ := loads[0]["options"].(map[string]any); got["num_ctx"] != float64(8192) {
		t.Errorf("load options = %v, want num_ctx 8192", loads[0]["options"])
	}
}

func TestFormatBytes(t *testing.T) {
	for n, want := range map[int64]string{999: "999 B", 1500: "1.5 kB", 2019393189: "2.0 GB"} {
		if got := formatBytes(n); got != want {
			t.Errorf("formatBytes(%d) = %q, want %q", n, got, want)
		}
	}
}
