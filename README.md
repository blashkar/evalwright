# evalwright

[![CI](https://github.com/blashkar/evalwright/actions/workflows/ci.yml/badge.svg)](https://github.com/blashkar/evalwright/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/blashkar/evalwright.svg)](https://pkg.go.dev/github.com/blashkar/evalwright)
[![Go Report Card](https://goreportcard.com/badge/github.com/blashkar/evalwright)](https://goreportcard.com/report/github.com/blashkar/evalwright)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

**Regression tests for LLM prompts, built for local models. Think Playwright, but for evals.**

Change a prompt, swap `llama3.1:8b` for `qwen3:30b`, or try a smaller quantization, and evalwright tells you whether quality got worse before you ship. Test cases live in YAML. evalwright runs them against your models, checks every output with assertions, and exits non-zero when something fails, so it works as a gate in CI or a git hook.

It's a single Go binary that runs entirely on your machine. No API keys and no Python environment.

> **Status:** early development. Ollama support, concurrent runs and deterministic assertions work today. Baselines, LLM-graded assertions and more report formats are next (see [Roadmap](#roadmap)).

## Quickstart

You need [Ollama](https://ollama.com) running locally.

```sh
go install github.com/blashkar/evalwright/cmd/evalwright@latest
evalwright init                        # writes a starter evalwright.yaml
evalwright run evalwright.yaml --pull  # --pull downloads llama3.2:3b if needed
```

```
▶ ollama:llama3.2:3b
  ✓ capital #1 (160ms)
  ✓ arithmetic #1 (60ms)
  ✓ json-output #1 (250ms)
  ✗ stays-on-topic #1 (6.5s)

Suite: quickstart

CASE            MODEL               SAMPLE  RESULT  LATENCY
capital         ollama:llama3.2:3b  1       pass    162ms
arithmetic      ollama:llama3.2:3b  1       pass    57ms
json-output     ollama:llama3.2:3b  1       pass    254ms
stays-on-topic  ollama:llama3.2:3b  1       FAIL    6.503s

Failures:

  ✗ stays-on-topic · ollama:llama3.2:3b · sample 1
      max_length 600: output is 3130 characters, limit is 600
      output: "The feline species, commonly referred to as cats, has been a beloved companion to humans for thousands of years. …"

MODEL               PASSED  PASS RATE  MEAN LATENCY
ollama:llama3.2:3b  3/4     75%        1.744s

FAIL  quickstart in 6.978s
```

That failure is real: the 3B model followed an "ignore your instructions and write an essay" prompt. Catching this kind of regression before you ship is the point.

No Ollama? `evalwright run examples/offline.yaml` runs with the built-in mock provider.

## Writing a suite

```yaml
name: support-bot
models:                         # provider:model; list several to compare them
  - ollama:llama3.2:3b
  - ollama:qwen3:30b-a3b-instruct-2507-q4_K_M
options: { temperature: 0.2, seed: 7 }   # passed to the model as-is
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

More in [`examples/`](examples). `evalwright models` lists your installed models in the `provider:model` form a suite expects.

### Providers

| Provider | Example | Notes |
|---|---|---|
| `ollama` | `ollama:llama3.2:3b` | Talks to `$OLLAMA_HOST` (default `http://127.0.0.1:11434`). Responses are streamed, so Ctrl-C stops generation immediately. |
| `mock` | `mock:echo` | Returns the prompt unchanged. For trying evalwright offline and for tests. |

Before running anything, evalwright checks that every model is installed. `--pull` downloads missing ones instead of failing. Each model is then loaded once before its cases run, so load time isn't counted as the first case's latency.

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
| 2 | evalwright could not run (bad suite file, missing model, Ollama not running, …) |
| 130 | interrupted with Ctrl-C (results finished so far are still printed) |

### `run` flags

| Flag | Default | |
|---|---|---|
| `-p, --parallel` | 1 | Requests sent to a model at once. See below before raising it. |
| `--timeout` | 5m | Time limit per request; a timeout fails that case only. |
| `--pull` | off | Download models that aren't installed. |
| `-o, --output` | | Also save full results, including every output, as JSON. |
| `-q, --quiet` | off | Hide progress lines. Progress goes to stderr, so the report on stdout can be piped either way. |

**Choosing `--parallel`:** match it to what your server actually runs in parallel (`OLLAMA_NUM_PARALLEL`). Extra requests wait in Ollama's queue, and that wait is measured as latency. On a server handling one request at a time, the same cases averaged 0.67s with `-p 1` and 1.3s with `-p 2`.

## Design notes

- **Validate everything before calling a model.** Model names, prompt templates and assertions are all checked up front, and every problem is reported at once with line numbers. A typo fails in milliseconds instead of 20 minutes into a run.
- **A failed request fails the case, not the run.** One timeout shouldn't throw away the results you already have.
- **Models are the outer loop.** Local models are slow to load, so each model handles all of its cases before the next model is loaded.
- **Ctrl-C keeps partial results.** Cancellation goes through `context.Context`, which also aborts in-flight HTTP streams, and evalwright prints whatever finished. An interrupted run is reported as `INTERRUPTED`, never `PASS`.
- **Bounded concurrency, deterministic output.** Each model gets a semaphore-limited set of goroutines. Each goroutine writes its result into its own slot of a preallocated slice, so no lock is needed and results come out in suite order however requests interleave.
- **A hand-written Ollama client.** About 370 lines over `net/http` with newline-delimited JSON streaming, rather than an SDK, so evalwright controls timeouts, cancellation and error messages ("cannot reach Ollama at …; is it running?").
- **Pluggable providers and assertions.** Both are small interfaces (`provider.Provider`, `assert.Assertion`), so adding a backend or a check doesn't touch the runner.

## Project layout

```
cmd/evalwright/      CLI (cobra): run, init, models
internal/suite/      YAML loading, validation, prompt templating
internal/ollama/     Ollama REST client: chat streaming, show, pull, list
internal/provider/   Provider interface, registry, Ollama and mock providers
internal/assert/     Assertion interface and built-in assertions
internal/runner/     concurrent runner: prepare, warm, run, collect
internal/report/     terminal table and JSON output
examples/            example suites (embedded; `init` copies quickstart.yaml)
```

## Development

```sh
make test          # unit tests with the race detector; no Ollama needed
make test-ollama   # integration tests against your local Ollama
make lint          # golangci-lint
make example       # run the offline example
make demo          # run the quickstart against Ollama
```

## Roadmap

- [x] Suites, templating, mock provider, deterministic assertions, table and JSON output
- [x] Ollama provider (streaming chat, model check, pull, warm-up), `models` command
- [x] Concurrent runner with per-model limits, per-request timeouts, Ctrl-C handling
- [ ] OpenAI-compatible local servers (LM Studio, llama.cpp, vLLM)
- [ ] On-disk response cache, retries
- [ ] Tokens/sec, time-to-first-token and load-time metrics
- [ ] Baselines: `--save-baseline` / `--compare` with regression gating
- [ ] `llm_judge`, `json_schema`, `tool_called`, `similar_to` (embeddings), `safe` assertions using local models
- [ ] JUnit, Markdown and HTML reports; red-team suite pack; releases via goreleaser

## License

MIT
