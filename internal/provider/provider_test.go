package provider

import (
	"context"
	"strings"
	"testing"
)

func TestResolve(t *testing.T) {
	mock := &Mock{}
	reg := Registry{"mock": mock}

	tests := []struct {
		spec      string
		wantModel string
		wantErr   string
	}{
		{spec: "mock:echo", wantModel: "echo"},
		{spec: "mock:qwen3:30b", wantModel: "qwen3:30b"}, // only the first colon splits
		{spec: "echo", wantErr: "want provider:model"},
		{spec: "mock:", wantErr: "want provider:model"},
		{spec: "ollama:llama3.2:3b", wantErr: `unknown provider "ollama" (available: mock)`},
	}
	for _, tt := range tests {
		t.Run(tt.spec, func(t *testing.T) {
			p, model, err := reg.Resolve(tt.spec)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if p != mock || model != tt.wantModel {
				t.Errorf("Resolve = (%v, %q), want (mock, %q)", p, model, tt.wantModel)
			}
		})
	}
}

func TestMockEchoAndCancel(t *testing.T) {
	m := &Mock{}
	resp, err := m.Generate(context.Background(), Request{Prompt: "hello"})
	if err != nil || resp.Text != "hello" {
		t.Fatalf("Generate = (%q, %v), want (hello, nil)", resp.Text, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Generate(ctx, Request{Prompt: "hello"}); err == nil {
		t.Error("Generate with cancelled context succeeded, want error")
	}
}
