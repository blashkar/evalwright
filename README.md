# evalwright

[![CI](https://github.com/blashkar/evalwright/actions/workflows/ci.yml/badge.svg)](https://github.com/blashkar/evalwright/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/blashkar/evalwright.svg)](https://pkg.go.dev/github.com/blashkar/evalwright)
[![Go Report Card](https://goreportcard.com/badge/github.com/blashkar/evalwright)](https://goreportcard.com/report/github.com/blashkar/evalwright)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

**Regression tests for LLM prompts, built for local models. Think Playwright, but for evals.**

Change a prompt, swap `llama3.1:8b` for `qwen3:30b`, or try a smaller quantization, and evalwright tells you whether quality got worse before you ship. Test cases live in YAML. evalwright runs them against your models, checks every output with assertions, and exits non-zero when something fails, so it works as a gate in CI or a git hook.

It's a single Go binary that runs entirely on your machine. No API keys and no Python environment.

> **Status:** early development. The core runner and assertions work today with the offline `mock` provider. Ollama support, concurrency, baselines and LLM-graded assertions are next (see [Roadmap](#roadmap)).

## Quickstart

```sh
go install github.com/blashkar/evalwright/cmd/evalwright@latest
evalwright init            # writes an example evalwright.yaml
evalwright run evalwright.yaml
```

```
Suite: example

CASE            MODEL      SAMPLE  RESULT  LATENCY
password-reset  mock:echo  1       pass    0s
no-secrets      mock:echo  1       pass    0s

MODEL      PASSED  PASS RATE  MEAN LATENCY
mock:echo  2/2     100%       0s

PASS  example in 0s
```

## Writing a suite

```yaml
name: support-bot
models: [mock:echo]             # provider:model
system: You are a concise support assistant.
prompt: |                       # Go text/template; or use prompt_file: path.txt
  Question: {{.question}}
samples: 3                      # run each case 3 times (LLM output varies)
cases:
  - name: password-reset
    vars: { question: "How do I reset my password?" }
    assert:
      - icontains: "settings"
      - max_length: 400
      - max_latency_ms: 5000
```

### Assertions

| Assertion | Value | Passes when |
|---|---|---|
| `equals` | string | output equals the value, ignoring surrounding whitespace |
| `contains` / `icontains` | string | output contains the value (`icontains` ignores case) |
| `not_contains` | string | output does not contain the value |
| `regex` | RE2 pattern | output matches the pattern |
| `max_length` | int | output has at most this many characters |
| `max_latency_ms` | int | the model answered within this many milliseconds |

### Exit codes

| Code | Meaning |
|---|---|
| 0 | every case passed |
| 1 | the run finished and at least one case failed |
| 2 | evalwright could not run (bad suite file, unknown model, …) |

Use `-o results.json` to save the full results, including every output, as JSON.

## Design notes

- **Validate everything before calling a model.** Model names, prompt templates and assertions are all checked up front, and every problem is reported at once with line numbers. A typo fails in milliseconds instead of 20 minutes into a run.
- **A failed request fails the case, not the run.** One timeout shouldn't throw away the results you already have.
- **Models are the outer loop.** Local models are slow to load, so each model handles all of its cases before the next model is loaded.
- **Ctrl-C keeps partial results.** Cancellation goes through `context.Context`, and evalwright prints whatever finished.
- **Pluggable providers and assertions.** Both are small interfaces (`provider.Provider`, `assert.Assertion`), so adding a backend or a check doesn't touch the runner.

## Project layout

```
cmd/evalwright/        CLI (cobra)
internal/suite/      YAML loading, validation, prompt templating
internal/provider/   Provider interface, registry, mock provider
internal/assert/     Assertion interface and built-in assertions
internal/runner/     runs cases against models, collects results
internal/report/     terminal table and JSON output
```

## Development

```sh
make test      # go test -race ./...
make lint      # golangci-lint
make example   # build and run the example suite
```

## Roadmap

- [x] Suites, templating, mock provider, deterministic assertions, table and JSON output
- [ ] Ollama provider (chat, streaming, model pull/inspect) and OpenAI-compatible local servers
- [ ] Concurrent runner with per-model limits, retries, on-disk response cache
- [ ] Tokens/sec, time-to-first-token and load-time metrics
- [ ] Baselines: `--save-baseline` / `--compare` with regression gating
- [ ] `llm_judge`, `json_schema`, `tool_called`, `similar_to` (embeddings), `safe` assertions using local models
- [ ] JUnit, Markdown and HTML reports; red-team suite pack; releases via goreleaser

## License

MIT
