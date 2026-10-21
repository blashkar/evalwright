package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Chunks captured from a real Ollama 0.32 server (llama3.2:3b).
const chatStream = `{"model":"llama3.2:3b","created_at":"2026-10-21T22:54:02.664324Z","message":{"role":"assistant","content":"Hello"},"done":false}
{"model":"llama3.2:3b","created_at":"2026-10-21T22:54:02.67514Z","message":{"role":"assistant","content":" from"},"done":false}
{"model":"llama3.2:3b","created_at":"2026-10-21T22:54:02.685165Z","message":{"role":"assistant","content":" me!"},"done":false}
{"model":"llama3.2:3b","created_at":"2026-10-21T22:54:02.705218Z","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","total_duration":5751374125,"load_duration":5639626167,"prompt_eval_count":31,"prompt_eval_duration":68234000,"eval_count":5,"eval_duration":40878000}
`

// newTestClient starts a fake Ollama server with handler and returns a client for it.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := NewClient(srv.URL, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestParseHost(t *testing.T) {
	tests := []struct {
		in, want, wantErr string
	}{
		{in: "", want: "http://127.0.0.1:11434"},
		{in: "localhost", want: "http://localhost:11434"},
		{in: "192.0.2.10:8080", want: "http://192.0.2.10:8080"},
		{in: "http://box/", want: "http://box:11434"},
		{in: "https://ollama.example.com", want: "https://ollama.example.com"},
		{in: "[::1]", want: "http://[::1]:11434"},
		{in: "ftp://box", wantErr: "scheme must be http or https"},
		{in: "http://:11434", wantErr: "missing host name"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			u, err := parseHost(tt.in)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if u.String() != tt.want {
				t.Errorf("parseHost(%q) = %s, want %s", tt.in, u, tt.want)
			}
		})
	}
}

func TestChatStreams(t *testing.T) {
	var got chatRequest
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/chat" {
			t.Errorf("request = %s %s, want POST /api/chat", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		fmt.Fprint(w, chatStream)
	})

	req := ChatRequest{
		Model:    "llama3.2:3b",
		Messages: []Message{{Role: "user", Content: "hi"}},
		Options:  map[string]any{"temperature": 0.0, "seed": 42},
	}
	var text strings.Builder
	var final ChatResponse
	err := c.Chat(context.Background(), req, func(r ChatResponse) error {
		text.WriteString(r.Message.Content)
		final = r
		return nil
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if !got.Stream || got.Model != "llama3.2:3b" || got.Options["seed"] != 42.0 || len(got.Messages) != 1 {
		t.Errorf("server received %+v", got)
	}
	if text.String() != "Hello from me!" {
		t.Errorf("assembled text = %q", text.String())
	}
	if !final.Done || final.EvalCount != 5 || final.EvalDuration != 40878*time.Microsecond {
		t.Errorf("final chunk metrics = %+v", final.Metrics)
	}
}

func TestChatErrors(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		check   func(error) bool
		wantErr string
	}{
		{
			name: "model not found",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `{"error":"model 'nope' not found"}`)
			},
			check:   IsNotFound,
			wantErr: "ollama: model 'nope' not found (HTTP 404)",
		},
		{
			name: "error mid-stream",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprintln(w, `{"message":{"role":"assistant","content":"Hel"},"done":false}`)
				fmt.Fprintln(w, `{"error":"out of memory"}`)
			},
			wantErr: "ollama: out of memory",
		},
		{
			name: "stream cut short",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprintln(w, `{"message":{"role":"assistant","content":"Hel"},"done":false}`)
			},
			wantErr: "ended before the model finished",
		},
		{
			name: "non-JSON error page",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "bad gateway", http.StatusBadGateway)
			},
			wantErr: "bad gateway (HTTP 502)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, tt.handler)
			err := c.Chat(context.Background(), ChatRequest{Model: "m"}, func(ChatResponse) error { return nil })
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
			}
			if tt.check != nil && !tt.check(err) {
				t.Errorf("error %v did not satisfy check", err)
			}
		})
	}
}

func TestChatCancelStopsStream(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"message":{"role":"assistant","content":"first"},"done":false}`)
		w.(http.Flusher).Flush()
		<-r.Context().Done() // a slow model: never sends the rest
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	err := c.Chat(ctx, ChatRequest{Model: "m"}, func(ChatResponse) error {
		cancel() // e.g. the user pressed Ctrl-C after the first token
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("Chat did not return promptly after cancellation")
	}
}

func TestConnectionRefused(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.URL
	srv.Close() // nothing listens here any more

	c, err := NewClient(addr, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Version(context.Background())
	if err == nil || !strings.Contains(err.Error(), "is it running?") {
		t.Errorf("error = %v, want a hint to start Ollama", err)
	}
}

func TestShowListVersion(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/show":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["model"] != "llama3.2:3b" {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprintf(w, `{"error":"model '%s' not found"}`, body["model"])
				return
			}
			fmt.Fprint(w, `{"details":{"family":"llama","parameter_size":"3.2B","quantization_level":"Q4_K_M"},"capabilities":["completion","tools"]}`)
		case "/api/tags":
			fmt.Fprint(w, `{"models":[{"name":"llama3.2:3b","size":2019393189,"digest":"a80c4f17","details":{"parameter_size":"3.2B","quantization_level":"Q4_K_M"}}]}`)
		case "/api/version":
			fmt.Fprint(w, `{"version":"0.32.15"}`)
		default:
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()

	show, err := c.Show(ctx, "llama3.2:3b")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if show.Details.QuantizationLevel != "Q4_K_M" || len(show.Capabilities) != 2 {
		t.Errorf("Show = %+v", show)
	}
	if _, err := c.Show(ctx, "missing:1b"); !IsNotFound(err) {
		t.Errorf("Show(missing) error = %v, want not found", err)
	}

	models, err := c.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(models) != 1 || models[0].Name != "llama3.2:3b" || models[0].Details.ParameterSize != "3.2B" {
		t.Errorf("List = %+v", models)
	}

	v, err := c.Version(ctx)
	if err != nil || v != "0.32.15" {
		t.Errorf("Version = %q, %v", v, err)
	}
}

func TestPull(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, `{"status":"pulling manifest"}`)
		fmt.Fprintln(w, `{"status":"pulling 74701a8c35f6","digest":"sha256:74701a8c","total":1000,"completed":500}`)
		fmt.Fprintln(w, `{"status":"pulling 74701a8c35f6","digest":"sha256:74701a8c","total":1000,"completed":1000}`)
		fmt.Fprintln(w, `{"status":"verifying sha256 digest"}`)
		fmt.Fprintln(w, `{"status":"success"}`)
	})
	var updates []PullProgress
	err := c.Pull(context.Background(), "llama3.2:1b", func(p PullProgress) error {
		updates = append(updates, p)
		return nil
	})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if len(updates) != 5 || updates[2].Completed != 1000 {
		t.Errorf("progress updates = %+v", updates)
	}
}

func TestPullWithoutSuccess(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, `{"status":"pulling manifest"}`)
	})
	err := c.Pull(context.Background(), "m", func(PullProgress) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "without success") {
		t.Errorf("error = %v, want pull failure", err)
	}
}
