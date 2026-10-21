package suite

import (
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
