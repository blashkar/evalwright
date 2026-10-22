package assert

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// decodeFrom returns a decode function that copies v into its target,
// standing in for the YAML node the suite package normally provides.
func decodeFrom(v any) func(any) error {
	return func(target any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return json.Unmarshal(b, target)
	}
}

func TestAssertions(t *testing.T) {
	tests := []struct {
		typ      string
		value    any
		out      Output
		wantPass bool
	}{
		{"equals", "Paris", Output{Text: "  Paris\n"}, true},
		{"equals", "Paris", Output{Text: "paris"}, false},
		{"contains", "Settings", Output{Text: "Open Settings > Account"}, true},
		{"contains", "Settings", Output{Text: "open settings"}, false},
		{"icontains", "Settings", Output{Text: "open settings"}, true},
		{"not_contains", "API_KEY", Output{Text: "no secrets here"}, true},
		{"not_contains", "API_KEY", Output{Text: "API_KEY=abc"}, false},
		{"regex", `^\d{3}$`, Output{Text: "123"}, true},
		{"regex", `^\d{3}$`, Output{Text: "1234"}, false},
		{"not_regex", `(?i)in transit|shipped`, Output{Text: "Please email us."}, true},
		{"not_regex", `(?i)in transit|shipped`, Output{Text: "It is In Transit."}, false},
		{"max_length", 5, Output{Text: "héllo"}, true}, // 5 runes, 6 bytes
		{"max_length", 4, Output{Text: "héllo"}, false},
		{"max_latency_ms", 100, Output{Latency: 50 * time.Millisecond}, true},
		{"max_latency_ms", 100, Output{Latency: 150 * time.Millisecond}, false},
	}
	for _, tt := range tests {
		a, err := Build(tt.typ, decodeFrom(tt.value))
		if err != nil {
			t.Fatalf("Build(%s, %v): %v", tt.typ, tt.value, err)
		}
		t.Run(a.Name(), func(t *testing.T) {
			got := a.Check(context.Background(), tt.out)
			if got.Pass != tt.wantPass {
				t.Errorf("Check(%q) pass = %v, want %v (detail: %s)", tt.out.Text, got.Pass, tt.wantPass, got.Detail)
			}
			if !got.Pass && got.Detail == "" {
				t.Error("failed check has no detail")
			}
		})
	}
}

func TestBuildErrors(t *testing.T) {
	tests := []struct {
		typ     string
		value   any
		wantErr string
	}{
		{"nope", "x", `unknown assertion "nope"`},
		{"regex", "(", "missing closing )"},
		{"contains", "", "must not be empty"},
		{"max_length", 0, "must be positive"},
		{"max_latency_ms", "fast", "cannot unmarshal"},
	}
	for _, tt := range tests {
		t.Run(tt.typ, func(t *testing.T) {
			_, err := Build(tt.typ, decodeFrom(tt.value))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Build error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestNotRegexDetailQuotesMatch(t *testing.T) {
	a, err := Build("not_regex", decodeFrom(`(?i)in transit|shipped`))
	if err != nil {
		t.Fatal(err)
	}
	got := a.Check(context.Background(), Output{Text: "Your order has shipped."})
	if got.Pass || !strings.Contains(got.Detail, `at "shipped"`) {
		t.Errorf("detail = %q, want it to quote the matching text", got.Detail)
	}
}
