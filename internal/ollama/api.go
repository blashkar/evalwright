package ollama

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"
)

// Message is one turn of a chat conversation.
type Message struct {
	Role    string `json:"role"` // "system", "user" or "assistant"
	Content string `json:"content"`
	// Thinking is a reasoning model's thinking, which Ollama streams
	// separately from Content.
	Thinking string `json:"thinking,omitempty"`
}

// ChatRequest asks a model to continue a conversation.
type ChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	// Options are model parameters such as temperature, seed and num_ctx.
	Options map[string]any `json:"options,omitempty"`
}

// ChatResponse is one chunk of a streamed chat response. The final chunk has
// Done set and carries the Metrics for the whole response.
type ChatResponse struct {
	Model      string    `json:"model"`
	CreatedAt  time.Time `json:"created_at"`
	Message    Message   `json:"message"`
	Done       bool      `json:"done"`
	DoneReason string    `json:"done_reason,omitempty"`
	Metrics
}

// Metrics are the token counts and timings Ollama reports when a response is done.
// Ollama sends durations in nanoseconds, which decode directly into time.Duration.
type Metrics struct {
	TotalDuration      time.Duration `json:"total_duration"`
	LoadDuration       time.Duration `json:"load_duration"`
	PromptEvalCount    int           `json:"prompt_eval_count"`
	PromptEvalDuration time.Duration `json:"prompt_eval_duration"`
	EvalCount          int           `json:"eval_count"`
	EvalDuration       time.Duration `json:"eval_duration"`
}

// chatRequest is the wire format; Chat always streams.
type chatRequest struct {
	ChatRequest
	Stream bool `json:"stream"`
}

// Chat streams a chat response, calling fn for each chunk as it arrives.
// It returns an error if the stream ends before the model is done.
func (c *Client) Chat(ctx context.Context, req ChatRequest, fn func(ChatResponse) error) error {
	resp, err := c.do(ctx, http.MethodPost, "/api/chat", chatRequest{ChatRequest: req, Stream: true})
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	done := false
	err = stream(resp.Body, func(r ChatResponse) error {
		done = r.Done
		return fn(r)
	})
	if err != nil {
		return err
	}
	if !done {
		return errors.New("ollama: response ended before the model finished")
	}
	return nil
}

// Load loads model into memory without generating anything, so the next
// request doesn't pay the load time. Pass the options later requests will
// use: some, like num_ctx, decide how the model is loaded, and a request
// with different ones makes Ollama load the model again.
func (c *Client) Load(ctx context.Context, model string, options map[string]any) error {
	resp, err := c.do(ctx, http.MethodPost, "/api/chat", chatRequest{
		ChatRequest: ChatRequest{Model: model, Messages: []Message{}, Options: options},
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.Copy(io.Discard, resp.Body)
	return err
}

// Model is an installed model, as listed by List.
type Model struct {
	Name       string       `json:"name"`
	Size       int64        `json:"size"`
	Digest     string       `json:"digest"`
	ModifiedAt time.Time    `json:"modified_at"`
	Details    ModelDetails `json:"details"`
}

// ModelDetails describes a model's architecture and quantization.
type ModelDetails struct {
	Format            string `json:"format"`
	Family            string `json:"family"`
	ParameterSize     string `json:"parameter_size"`
	QuantizationLevel string `json:"quantization_level"`
}

// List returns the models installed on the server.
func (c *Client) List(ctx context.Context) ([]Model, error) {
	var body struct {
		Models []Model `json:"models"`
	}
	if err := c.getJSON(ctx, http.MethodGet, "/api/tags", nil, &body); err != nil {
		return nil, err
	}
	return body.Models, nil
}

// ShowResponse describes one installed model.
type ShowResponse struct {
	Details      ModelDetails `json:"details"`
	Capabilities []string     `json:"capabilities"`
	ModifiedAt   time.Time    `json:"modified_at"`
}

// Show returns details about model. If the model is not installed, the
// error satisfies IsNotFound.
func (c *Client) Show(ctx context.Context, model string) (*ShowResponse, error) {
	var out ShowResponse
	if err := c.getJSON(ctx, http.MethodPost, "/api/show", map[string]string{"model": model}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PullProgress reports the state of a download. Total and Completed are in
// bytes and refer to the layer named by Digest.
type PullProgress struct {
	Status    string `json:"status"`
	Digest    string `json:"digest,omitempty"`
	Total     int64  `json:"total,omitempty"`
	Completed int64  `json:"completed,omitempty"`
}

// Pull downloads model, calling fn with progress updates as they arrive.
func (c *Client) Pull(ctx context.Context, model string, fn func(PullProgress) error) error {
	resp, err := c.do(ctx, http.MethodPost, "/api/pull", map[string]any{"model": model, "stream": true})
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	success := false
	err = stream(resp.Body, func(p PullProgress) error {
		success = p.Status == "success"
		return fn(p)
	})
	if err != nil {
		return err
	}
	if !success {
		return errors.New("ollama: pull ended without success")
	}
	return nil
}

// RunningModel is a model loaded in memory, as listed by Running.
type RunningModel struct {
	Name string `json:"name"`
	// Size is the memory the loaded model takes, and SizeVRAM the part of it
	// in GPU memory: equal if the model runs entirely on the GPU, zero if it
	// runs on the CPU.
	Size     int64 `json:"size"`
	SizeVRAM int64 `json:"size_vram"`
	// ContextLength is the context window it was loaded with. Older servers
	// don't report it.
	ContextLength int `json:"context_length,omitempty"`
}

// Running returns the models currently loaded in memory.
func (c *Client) Running(ctx context.Context) ([]RunningModel, error) {
	var body struct {
		Models []RunningModel `json:"models"`
	}
	if err := c.getJSON(ctx, http.MethodGet, "/api/ps", nil, &body); err != nil {
		return nil, err
	}
	return body.Models, nil
}

// Version returns the server's version. It is a cheap way to check that the
// server is reachable.
func (c *Client) Version(ctx context.Context) (string, error) {
	var body struct {
		Version string `json:"version"`
	}
	if err := c.getJSON(ctx, http.MethodGet, "/api/version", nil, &body); err != nil {
		return "", err
	}
	return body.Version, nil
}
