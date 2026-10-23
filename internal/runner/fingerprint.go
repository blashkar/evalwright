package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/blashkar/evalwright/internal/suite"
)

// CaseInfo records what a case sent and checked, so a later run can tell
// whether the case changed.
type CaseInfo struct {
	Name        string      `json:"name"`
	Fingerprint Fingerprint `json:"fingerprint"`
}

// Fingerprint identifies a case's inputs and checks by hashes of each part,
// so a comparison can say which part changed without storing the prompts.
type Fingerprint struct {
	System  string `json:"system"`
	Prompt  string `json:"prompt"`
	Options string `json:"options"`
	Checks  string `json:"checks"`
}

// Changes names the parts that differ between f and other, in a fixed order.
func (f Fingerprint) Changes(other Fingerprint) []string {
	var out []string
	for _, p := range []struct {
		name string
		a, b string
	}{
		{"system prompt", f.System, other.System},
		{"prompt", f.Prompt, other.Prompt},
		{"options", f.Options, other.Options},
		{"checks", f.Checks, other.Checks},
	} {
		if p.a != p.b {
			out = append(out, p.name)
		}
	}
	return out
}

func fingerprint(s *suite.Suite, c *preparedCase) Fingerprint {
	checks := make([]string, len(c.Assert))
	for i, a := range c.Assert {
		checks[i] = a.Canonical()
	}
	options, err := json.Marshal(s.Options) // sorts map keys
	if err != nil {
		options = fmt.Appendf(nil, "%v", s.Options)
	}
	return Fingerprint{
		System:  hash(s.System),
		Prompt:  hash(c.prompt),
		Options: hash(string(options)),
		Checks:  hash(strings.Join(checks, "\n")),
	}
}

// hash returns a short hex SHA-256 of s: enough to tell versions apart, and
// it doesn't reveal the text.
func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}
