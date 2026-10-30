// Package suite loads and validates evalwright test suites written in YAML.
package suite

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"

	"go.yaml.in/yaml/v3"
)

// Suite is a set of test cases that are run against one or more models.
type Suite struct {
	Name   string   `yaml:"name"`
	Models []string `yaml:"models"`
	// Options are passed to every model, e.g. {temperature: 0.7, seed: 42}.
	// A seed is the seed of the first sample; see SampleSeed.
	Options    map[string]any `yaml:"options"`
	System     string         `yaml:"system"`
	Prompt     string         `yaml:"prompt"`
	PromptFile string         `yaml:"prompt_file"`
	Samples    int            `yaml:"samples"`
	Cases      []Case         `yaml:"cases"`

	// MinPassRate, if set, is the lowest pass rate each model may have.
	// Without a baseline it replaces the default rule that every case must
	// pass; with one, it applies on top of the regression check.
	MinPassRate *Percent `yaml:"min_pass_rate"`
	// MaxLatencyIncrease, if set, is how much slower than the baseline each
	// model's median latency may get. It only applies with a baseline.
	MaxLatencyIncrease *Percent `yaml:"max_latency_increase"`

	tmpl *template.Template
}

// Case is a single input to the prompt template plus the checks its output must pass.
type Case struct {
	Name   string         `yaml:"name"`
	Vars   map[string]any `yaml:"vars"`
	Assert []AssertSpec   `yaml:"assert"`
}

// AssertSpec is one assertion as written in YAML, for example `contains: "Settings"`.
// The suite package only records the assertion's type and raw value; the assert
// package decodes the value, because only it knows the shape each type expects.
type AssertSpec struct {
	Type string
	Line int

	value yaml.Node
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (a *AssertSpec) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode || len(n.Content) != 2 {
		return fmt.Errorf("line %d: an assertion must have exactly one key, like `contains: \"text\"`", n.Line)
	}
	a.Type = n.Content[0].Value
	a.Line = n.Line
	a.value = *n.Content[1]
	return nil
}

// Decode decodes the assertion's value into v.
func (a AssertSpec) Decode(v any) error {
	return a.value.Decode(v)
}

// Canonical returns the assertion as a stable string of its type and value,
// without line numbers, comments or formatting, so it changes only when the
// check itself does.
func (a AssertSpec) Canonical() string {
	var v any
	if err := a.value.Decode(&v); err != nil {
		return a.Type + ": " + a.value.Value
	}
	b, err := json.Marshal(v) // sorts map keys
	if err != nil {
		return fmt.Sprintf("%s: %v", a.Type, v)
	}
	return a.Type + ": " + string(b)
}

// Percent is a fraction written as a percentage in YAML, like "80%".
type Percent float64

// UnmarshalYAML implements yaml.Unmarshaler.
func (p *Percent) UnmarshalYAML(n *yaml.Node) error {
	num, ok := strings.CutSuffix(strings.TrimSpace(n.Value), "%")
	f, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
	if n.Kind != yaml.ScalarNode || !ok || err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return fmt.Errorf("line %d: want a percentage like \"80%%\", got %q", n.Line, n.Value)
	}
	*p = Percent(f / 100)
	return nil
}

func (p Percent) String() string {
	return strconv.FormatFloat(float64(p)*100, 'f', -1, 64) + "%"
}

// Load reads and validates the suite at path. A prompt_file is resolved
// relative to the suite file's directory.
func Load(path string) (*Suite, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Parse(f, path)
}

// Parse reads and validates a suite from r. path is used for error messages
// and to resolve prompt_file.
func Parse(r io.Reader, path string) (*Suite, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true) // reject typos like "promt:" instead of silently ignoring them

	var s Suite
	if err := dec.Decode(&s); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s: suite file is empty", path)
		}
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	s.applyDefaults()
	if err := s.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	prompt := s.Prompt
	if s.PromptFile != "" {
		b, err := os.ReadFile(filepath.Join(filepath.Dir(path), s.PromptFile))
		if err != nil {
			return nil, fmt.Errorf("%s: reading prompt_file: %w", path, err)
		}
		prompt = string(b)
	}

	// missingkey=error turns a forgotten variable into an error instead of "<no value>".
	tmpl, err := template.New("prompt").Option("missingkey=error").Parse(prompt)
	if err != nil {
		return nil, fmt.Errorf("%s: parsing prompt template: %w", path, err)
	}
	s.tmpl = tmpl
	return &s, nil
}

func (s *Suite) applyDefaults() {
	if s.Samples == 0 {
		s.Samples = 1
	}
	for i := range s.Cases {
		if s.Cases[i].Name == "" {
			s.Cases[i].Name = fmt.Sprintf("case-%d", i+1)
		}
	}
}

// validate checks the suite's structure and reports every problem at once,
// so users can fix them all in one pass.
func (s *Suite) validate() error {
	var errs []error
	if s.Name == "" {
		errs = append(errs, errors.New("name is required"))
	}
	if len(s.Models) == 0 {
		errs = append(errs, errors.New("at least one model is required"))
	}
	// Results are grouped by model and by case name, so a repeated name
	// would silently merge two sets of results into one.
	for _, m := range duplicates(s.Models) {
		errs = append(errs, fmt.Errorf("model %q is listed more than once", m))
	}
	switch {
	case s.Prompt == "" && s.PromptFile == "":
		errs = append(errs, errors.New("one of prompt or prompt_file is required"))
	case s.Prompt != "" && s.PromptFile != "":
		errs = append(errs, errors.New("prompt and prompt_file cannot both be set"))
	}
	if s.Samples < 1 {
		errs = append(errs, fmt.Errorf("samples must be at least 1, got %d", s.Samples))
	}
	if len(s.Cases) == 0 {
		errs = append(errs, errors.New("at least one case is required"))
	}
	names := make([]string, len(s.Cases))
	for i, c := range s.Cases {
		names[i] = c.Name
		if len(c.Assert) == 0 {
			errs = append(errs, fmt.Errorf("case %q: at least one assertion is required", c.Name))
		}
	}
	for _, n := range duplicates(names) {
		errs = append(errs, fmt.Errorf("case name %q is used more than once (unnamed cases are named case-1, case-2, ...)", n))
	}
	if v, ok := s.Options["seed"]; ok {
		if err := checkSeed(v, s.Samples); err != nil {
			errs = append(errs, err)
		}
	}
	if p := s.MinPassRate; p != nil && (*p < 0 || *p > 1) {
		errs = append(errs, fmt.Errorf("min_pass_rate must be between 0%% and 100%%, got %s", p))
	}
	if p := s.MaxLatencyIncrease; p != nil && *p < 0 {
		errs = append(errs, fmt.Errorf("max_latency_increase must be 0%% or more, got %s", p))
	}
	if v, ok := s.Options["temperature"]; ok {
		if _, ok := number(v); !ok {
			errs = append(errs, fmt.Errorf("options.temperature must be a number, got %v", v))
		}
	}
	return errors.Join(errs...)
}

// maxSeed is the largest seed a model can use: llama.cpp keeps only the
// low 32 bits, so larger seeds would repeat smaller ones.
const maxSeed = math.MaxUint32

// checkSeed checks that every sample's seed (seed to seed+samples-1) is one
// a model will actually use as given.
func checkSeed(v any, samples int) error {
	seed, ok := wholeNumber(v)
	switch {
	case !ok:
		return fmt.Errorf("options.seed must be a whole number, got %v", v)
	case seed < 0:
		// Ollama treats -1 as "pick a random seed", so the run couldn't be reproduced.
		return fmt.Errorf("options.seed must be 0 or more, got %d; to get random answers, leave seed out", seed)
	case samples > 0 && seed > maxSeed-int64(samples-1):
		return fmt.Errorf("options.seed is too large: samples would use seeds %d to %d, but models use 32-bit seeds (at most %d)",
			seed, seed+int64(samples-1), int64(maxSeed))
	}
	return nil
}

// duplicates returns the values that appear more than once in list, once each.
func duplicates(list []string) []string {
	seen := map[string]int{}
	var dups []string
	for _, v := range list {
		seen[v]++
		if seen[v] == 2 {
			dups = append(dups, v)
		}
	}
	return dups
}

// SampleSeed returns the seed for sample n (counting from 1), and false if
// the suite doesn't set a seed.
//
// Sample n uses seed+n-1. With one fixed seed every sample would produce the
// exact same answer, and a pass rate over identical answers measures nothing.
// Deriving a seed per sample keeps the samples independent while the whole
// run stays reproducible.
func (s *Suite) SampleSeed(n int) (int64, bool) {
	v, ok := s.Options["seed"]
	if !ok {
		return 0, false
	}
	seed, _ := wholeNumber(v) // checked in validate
	return seed + int64(n-1), true
}

// SetSamples changes how many times each case runs, and checks the suite
// again, since the samples' seeds depend on it.
func (s *Suite) SetSamples(n int) error {
	s.Samples = n
	return s.validate()
}

// Warnings reports settings that are valid but probably not what the author
// intended, such as sampling a model whose output can't vary.
func (s *Suite) Warnings() []string {
	var warns []string
	t, set := number(s.Options["temperature"])
	if set && t == 0 && s.Samples > 1 {
		warns = append(warns, fmt.Sprintf(
			"samples is %d but options.temperature is 0, so every sample gives the same answer; "+
				"raise the temperature (e.g. 0.7) to measure how answers vary, or use samples: 1", s.Samples))
	}
	// A single answer is what it is, but a pass rate or a comparison
	// depends on how the answers were sampled.
	if !set && (s.Samples > 1 || len(s.Models) > 1) {
		warns = append(warns,
			"options.temperature isn't set, so each model samples at its own default, and defaults differ between models "+
				"and can change with a new build; set it, e.g. options: {temperature: 0.7}. "+
				"The report's Sampling table shows the settings each model used")
	}
	return warns
}

// wholeNumber converts a YAML-decoded value to int64 if it is a whole number.
func wholeNumber(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case uint64:
		if n <= math.MaxInt64 {
			return int64(n), true
		}
	case float64:
		if n == math.Trunc(n) && math.Abs(n) <= 1<<53 {
			return int64(n), true
		}
	}
	return 0, false
}

// number converts a YAML-decoded value to float64 if it is numeric.
func number(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint64:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

// Render fills the prompt template with the case's variables.
func (s *Suite) Render(c Case) (string, error) {
	var b strings.Builder
	if err := s.tmpl.Execute(&b, c.Vars); err != nil {
		return "", fmt.Errorf("case %q: rendering prompt: %w", c.Name, err)
	}
	return b.String(), nil
}
