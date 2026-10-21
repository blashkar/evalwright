package provider

import (
	"context"
	"time"
)

// Mock is a deterministic Provider for tests and offline demos.
type Mock struct {
	// Reply produces the response text. If nil, Mock echoes the prompt back.
	Reply func(req Request) (string, error)
}

// Generate implements Provider.
func (m *Mock) Generate(ctx context.Context, req Request) (Response, error) {
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	start := time.Now()
	if m.Reply == nil {
		return Response{Text: req.Prompt, Latency: time.Since(start)}, nil
	}
	text, err := m.Reply(req)
	if err != nil {
		return Response{}, err
	}
	return Response{Text: text, Latency: time.Since(start)}, nil
}
