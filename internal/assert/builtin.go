package assert

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// equals passes when the output, ignoring surrounding whitespace, matches exactly.
// Models often add a trailing newline, which should not fail a test.
type equals struct{ want string }

func newEquals(decode func(any) error) (Assertion, error) {
	var s string
	if err := decode(&s); err != nil {
		return nil, err
	}
	return equals{want: strings.TrimSpace(s)}, nil
}

func (a equals) Name() string { return fmt.Sprintf("equals %q", a.want) }

func (a equals) Check(_ context.Context, out Output) Result {
	if got := strings.TrimSpace(out.Text); got != a.want {
		return fail("output %q does not equal %q", truncate(got, 80), a.want)
	}
	return pass()
}

// contains covers contains, icontains and not_contains.
type contains struct {
	substr     string
	negate     bool
	ignoreCase bool
}

func newContains(negate, ignoreCase bool) builder {
	return func(decode func(any) error) (Assertion, error) {
		var s string
		if err := decode(&s); err != nil {
			return nil, err
		}
		if s == "" {
			return nil, errors.New("value must not be empty")
		}
		return contains{substr: s, negate: negate, ignoreCase: ignoreCase}, nil
	}
}

func (a contains) Name() string {
	switch {
	case a.negate:
		return fmt.Sprintf("not_contains %q", a.substr)
	case a.ignoreCase:
		return fmt.Sprintf("icontains %q", a.substr)
	default:
		return fmt.Sprintf("contains %q", a.substr)
	}
}

func (a contains) Check(_ context.Context, out Output) Result {
	text, substr := out.Text, a.substr
	if a.ignoreCase {
		text, substr = strings.ToLower(text), strings.ToLower(substr)
	}
	found := strings.Contains(text, substr)
	switch {
	case a.negate && found:
		return fail("output contains %q", a.substr)
	case !a.negate && !found:
		return fail("output does not contain %q", a.substr)
	}
	return pass()
}

type regex struct{ re *regexp.Regexp }

func newRegex(decode func(any) error) (Assertion, error) {
	var s string
	if err := decode(&s); err != nil {
		return nil, err
	}
	re, err := regexp.Compile(s)
	if err != nil {
		return nil, err
	}
	return regex{re: re}, nil
}

func (a regex) Name() string { return fmt.Sprintf("regex /%s/", a.re) }

func (a regex) Check(_ context.Context, out Output) Result {
	if !a.re.MatchString(out.Text) {
		return fail("output does not match /%s/", a.re)
	}
	return pass()
}

// maxLength counts characters (runes), not bytes, so non-English output is measured fairly.
type maxLength struct{ max int }

func newMaxLength(decode func(any) error) (Assertion, error) {
	var n int
	if err := decode(&n); err != nil {
		return nil, err
	}
	if n <= 0 {
		return nil, fmt.Errorf("value must be positive, got %d", n)
	}
	return maxLength{max: n}, nil
}

func (a maxLength) Name() string { return fmt.Sprintf("max_length %d", a.max) }

func (a maxLength) Check(_ context.Context, out Output) Result {
	if n := utf8.RuneCountInString(out.Text); n > a.max {
		return fail("output is %d characters, limit is %d", n, a.max)
	}
	return pass()
}

type maxLatency struct{ max time.Duration }

func newMaxLatency(decode func(any) error) (Assertion, error) {
	var ms int
	if err := decode(&ms); err != nil {
		return nil, err
	}
	if ms <= 0 {
		return nil, fmt.Errorf("value must be positive, got %d", ms)
	}
	return maxLatency{max: time.Duration(ms) * time.Millisecond}, nil
}

func (a maxLatency) Name() string { return fmt.Sprintf("max_latency_ms %d", a.max.Milliseconds()) }

func (a maxLatency) Check(_ context.Context, out Output) Result {
	if out.Latency > a.max {
		return fail("took %s, limit is %s", out.Latency.Round(time.Millisecond), a.max)
	}
	return pass()
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
