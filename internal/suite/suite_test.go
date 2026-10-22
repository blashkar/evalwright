package suite

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validSuite = `
name: support
models: [mock:echo]
prompt: "Q: {{.question}}"
cases:
  - vars: { question: "hi" }
    assert:
      - contains: "hi"
  - name: named
    vars: { question: "bye" }
    assert:
      - regex: "b.e"
`

func TestParseValid(t *testing.T) {
	s, err := Parse(strings.NewReader(validSuite), "test.yaml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if s.Samples != 1 {
		t.Errorf("Samples = %d, want default 1", s.Samples)
	}
	if got := s.Cases[0].Name; got != "case-1" {
		t.Errorf("unnamed case got name %q, want case-1", got)
	}
	if got := s.Cases[1].Name; got != "named" {
		t.Errorf("named case got name %q, want named", got)
	}

	a := s.Cases[0].Assert[0]
	var v string
	if err := a.Decode(&v); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if a.Type != "contains" || v != "hi" {
		t.Errorf("assertion = %s %q, want contains \"hi\"", a.Type, v)
	}

	got, err := s.Render(s.Cases[1])
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != "Q: bye" {
		t.Errorf("Render = %q, want %q", got, "Q: bye")
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{"empty file", "", "suite file is empty"},
		{"unknown field", "name: x\npromt: typo\n", "field promt not found"},
		{
			"missing everything",
			"name: \"\"\n",
			"name is required",
		},
		{
			"both prompts",
			"name: x\nmodels: [m:a]\nprompt: a\nprompt_file: b\ncases: [{assert: [{contains: a}]}]\n",
			"cannot both be set",
		},
		{
			"negative samples",
			"name: x\nmodels: [m:a]\nprompt: a\nsamples: -1\ncases: [{assert: [{contains: a}]}]\n",
			"samples must be at least 1",
		},
		{
			"case without assertions",
			"name: x\nmodels: [m:a]\nprompt: a\ncases: [{name: c1}]\n",
			`case "c1": at least one assertion`,
		},
		{
			"assertion with two keys",
			"name: x\nmodels: [m:a]\nprompt: a\ncases: [{assert: [{contains: a, regex: b}]}]\n",
			"exactly one key",
		},
		{
			"duplicate case names",
			"name: x\nmodels: [m:a]\nprompt: a\ncases: [{name: c, assert: [{contains: a}]}, {name: c, assert: [{contains: a}]}]\n",
			`case name "c" is used more than once`,
		},
		{
			"case name clashes with a default name",
			"name: x\nmodels: [m:a]\nprompt: a\ncases: [{name: case-2, assert: [{contains: a}]}, {assert: [{contains: a}]}]\n",
			`case name "case-2" is used more than once`,
		},
		{
			"duplicate models",
			"name: x\nmodels: [m:a, m:b, m:a]\nprompt: a\ncases: [{assert: [{contains: a}]}]\n",
			`model "m:a" is listed more than once`,
		},
		{
			"bad template",
			"name: x\nmodels: [m:a]\nprompt: \"{{.oops\"\ncases: [{assert: [{contains: a}]}]\n",
			"parsing prompt template",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tt.yaml), "test.yaml")
			if err == nil {
				t.Fatal("Parse succeeded, want error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestRenderMissingVar(t *testing.T) {
	s, err := Parse(strings.NewReader(validSuite), "test.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Render(Case{Name: "c", Vars: map[string]any{"other": 1}})
	if err == nil || !strings.Contains(err.Error(), "question") {
		t.Errorf("Render error = %v, want missing key error mentioning question", err)
	}
}

func TestLoadPromptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "p.txt"), []byte("Hello {{.who}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	suitePath := filepath.Join(dir, "s.yaml")
	src := "name: x\nmodels: [m:a]\nprompt_file: p.txt\ncases: [{vars: {who: Go}, assert: [{contains: Go}]}]\n"
	if err := os.WriteFile(suitePath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := Load(suitePath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, err := s.Render(s.Cases[0])
	if err != nil {
		t.Fatal(err)
	}
	if got != "Hello Go" {
		t.Errorf("Render = %q, want %q", got, "Hello Go")
	}
}

func TestSampleSeed(t *testing.T) {
	tests := []struct {
		name    string
		options string
		want    []int64 // seeds for samples 1..3; nil means no seed
	}{
		{"no seed", "{temperature: 0.7}", nil},
		{"int seed", "{seed: 7}", []int64{7, 8, 9}},
		{"zero seed", "{seed: 0}", []int64{0, 1, 2}},
		{"largest seeds", "{seed: 4294967293}", []int64{4294967293, 4294967294, 4294967295}},
		{"whole float seed", "{seed: 42.0}", []int64{42, 43, 44}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := "name: x\nmodels: [m:a]\nprompt: p\nsamples: 3\noptions: " + tt.options + "\ncases: [{assert: [{contains: a}]}]\n"
			s, err := Parse(strings.NewReader(src), "test.yaml")
			if err != nil {
				t.Fatal(err)
			}
			for n := 1; n <= 3; n++ {
				got, ok := s.SampleSeed(n)
				if tt.want == nil {
					if ok {
						t.Errorf("SampleSeed(%d) = %d, want no seed", n, got)
					}
					continue
				}
				if !ok || got != tt.want[n-1] {
					t.Errorf("SampleSeed(%d) = %d, %v; want %d", n, got, ok, tt.want[n-1])
				}
			}
		})
	}
}

func TestOptionsValidation(t *testing.T) {
	tests := []struct {
		options, wantErr string
	}{
		{"{seed: 4.5}", "options.seed must be a whole number, got 4.5"},
		{`{seed: "abc"}`, "options.seed must be a whole number"},
		{`{temperature: hot}`, "options.temperature must be a number"},
		// -1 means "random seed" to Ollama, so the run couldn't be reproduced.
		{"{seed: -1}", "options.seed must be 0 or more, got -1"},
		// Models use 32-bit seeds; larger ones would repeat smaller ones.
		{"{seed: 4294967294}", "samples would use seeds 4294967294 to 4294967296"},
	}
	for _, tt := range tests {
		t.Run(tt.options, func(t *testing.T) {
			src := "name: x\nmodels: [m:a]\nprompt: p\nsamples: 3\noptions: " + tt.options + "\ncases: [{assert: [{contains: a}]}]\n"
			_, err := Parse(strings.NewReader(src), "test.yaml")
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestWarnings(t *testing.T) {
	tests := []struct {
		name     string
		samples  int
		options  string
		wantWarn bool
	}{
		{"greedy decoding with samples", 3, "{temperature: 0}", true},
		{"greedy decoding with samples (float)", 3, "{temperature: 0.0}", true},
		{"greedy decoding single sample", 1, "{temperature: 0}", false},
		{"sampling temperature", 3, "{temperature: 0.7}", false},
		{"no temperature (server default)", 3, "{}", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := fmt.Sprintf("name: x\nmodels: [m:a]\nprompt: p\nsamples: %d\noptions: %s\ncases: [{assert: [{contains: a}]}]\n", tt.samples, tt.options)
			s, err := Parse(strings.NewReader(src), "test.yaml")
			if err != nil {
				t.Fatal(err)
			}
			if got := len(s.Warnings()) > 0; got != tt.wantWarn {
				t.Errorf("warnings = %v, want warning: %v", s.Warnings(), tt.wantWarn)
			}
		})
	}
}
