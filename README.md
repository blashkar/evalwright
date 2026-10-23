# evalwright

[![CI](https://github.com/blashkar/evalwright/actions/workflows/ci.yml/badge.svg)](https://github.com/blashkar/evalwright/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/blashkar/evalwright.svg)](https://pkg.go.dev/github.com/blashkar/evalwright)
[![Go Report Card](https://goreportcard.com/badge/github.com/blashkar/evalwright)](https://goreportcard.com/report/github.com/blashkar/evalwright)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

**Regression tests for LLM prompts, built for local models. Think Playwright, but for evals.**

Change a prompt, swap `llama3.1:8b` for `qwen3:30b`, or try a smaller quantization, and evalwright tells you whether quality got worse before you ship. Test cases live in YAML. evalwright runs them against your models, checks every output with assertions, and exits non-zero when something fails, so it works as a gate in CI or a git hook.

It's a single Go binary that runs entirely on your machine. No API keys and no Python environment.

> **Status:** early development. Ollama support, concurrent runs, deterministic assertions, model comparison and baseline regression gating work today. LLM-graded assertions and more report formats are next (see [Roadmap](#roadmap)).

## Quickstart

You need [Ollama](https://ollama.com) running locally.

```sh
go install github.com/blashkar/evalwright/cmd/evalwright@latest
evalwright init                        # writes a starter evalwright.yaml
evalwright run evalwright.yaml --pull  # --pull downloads llama3.2:3b if needed
```

```
▶ ollama:llama3.2:3b
  ✓ capital #1 (140ms)
  ✓ arithmetic #1 (60ms)
  ✓ json-output #1 (170ms)
  ✗ stays-on-topic #1 (6.2s)

Suite: quickstart

CASE            MODEL               SAMPLE  RESULT  LATENCY
capital         ollama:llama3.2:3b  1       pass    143ms
arithmetic      ollama:llama3.2:3b  1       pass    57ms
json-output     ollama:llama3.2:3b  1       pass    168ms
stays-on-topic  ollama:llama3.2:3b  1       FAIL    6.202s

Failures:

  ✗ stays-on-topic · ollama:llama3.2:3b · sample 1
      max_length 600: output is 3130 characters, limit is 600
      output: "The feline species, commonly referred to as cats, has been a beloved companion to humans for thousands of years. With their sleek bodies, agile movements, and enigmatic personalities, it's no wonder w…"

MODEL               PASSED  PASS RATE  95% INTERVAL  ERRORS
ollama:llama3.2:3b  3/4     75%        30–95%        0

MODEL               LATENCY P50  LATENCY P95  FIRST TOKEN P50  ANSWER TOKENS P50  TOKENS/S  LOAD
ollama:llama3.2:3b  143ms        6.202s       46ms             8                  97        2.059s

FAIL  quickstart in 8.631s
```

That failure is real: the 3B model followed an "ignore your instructions and write an essay" prompt. Catching this kind of regression before you ship is the point. [Reading the report](#reading-the-report) explains the summary tables.

No Ollama? `evalwright run examples/offline.yaml` runs with the built-in mock provider.

## Writing a suite

```yaml
name: support-bot
models:                         # provider:model; list several to compare them
  - ollama:llama3.2:3b
  - ollama:qwen3:30b-a3b-instruct-2507-q4_K_M
options: { temperature: 0.7, seed: 7 }   # passed to the model; see "Samples and seeds"
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

# Optional rules for when the run passes; see "Catching regressions".
# min_pass_rate: 80%            # each model must pass 80% of samples, instead of every case passing
# max_latency_increase: 25%     # with --baseline: fail if a model's median latency rises more than this
```

More in [`examples/`](examples). `evalwright models` lists your installed models in the `provider:model` form a suite expects.

### Providers

| Provider | Example | Notes |
|---|---|---|
| `ollama` | `ollama:llama3.2:3b` | Talks to `$OLLAMA_HOST` (default `http://127.0.0.1:11434`). Responses are streamed, so Ctrl-C stops generation immediately. |
| `mock` | `mock:echo` | Returns the prompt unchanged. For trying evalwright offline and for tests. |

Before running anything, evalwright checks that every model is installed. `--pull` downloads missing ones instead of failing. Each model is then loaded once, with the suite's options, before its cases run, so load time isn't counted as the first case's latency. (Some options, like `num_ctx`, change how a model is loaded; warming up without them would make Ollama load the model again on the first case.)

### Samples and seeds

LLM output varies, so `samples: N` runs each case N times and reports a pass rate. For that to mean anything, the samples have to be able to differ:

- **Each sample gets its own seed.** If the suite sets `seed: 7`, samples use seeds 7, 8, 9, … . A single fixed seed would make every sample an identical copy, and a 100% pass rate over copies measures nothing. The seed each sample used is saved in the JSON results, so any answer can be reproduced exactly. Seeds must be between 0 and 4294967295: Ollama treats -1 as "random", and models only use 32-bit seeds.
- **Use a sampling temperature.** At `temperature: 0` the model always picks its most likely token, so every sample is the same; evalwright warns if a suite combines that with `samples` > 1. Even 0.2 gives near-identical answers in practice. 0.7 is a realistic chat setting.

### Assertions

| Assertion | Value | Passes when |
|---|---|---|
| `equals` | string | output equals the value, ignoring surrounding whitespace |
| `contains` / `icontains` | string | output contains the value (`icontains` ignores case) |
| `not_contains` | string | output does not contain the value |
| `regex` | RE2 pattern | output matches the pattern |
| `not_regex` | RE2 pattern | output does not match the pattern (a failure quotes the text that matched) |
| `max_length` | int | output has at most this many characters |
| `max_latency_ms` | int | the model answered within this many milliseconds |

### Exit codes

| Code | Meaning |
|---|---|
| 0 | the run passed: every case passed, or, with `min_pass_rate` or `--baseline`, their rules were met |
| 1 | the run finished and failed: a case failed, or, with `--baseline`, results got significantly worse. The report ends with the reasons. |
| 2 | evalwright could not run (bad suite file, missing model, Ollama not running, …) |
| 130 | interrupted with Ctrl-C (results finished so far are still printed, and saved with `-o`) |

### `run` flags

| Flag | Default | |
|---|---|---|
| `-p, --parallel` | 1 | Requests sent to a model at once. See below before raising it. |
| `--timeout` | 5m | Time limit per request and per model load. A request timeout fails that case only; a load timeout fails that model's cases only. |
| `--pull` | off | Download models that aren't installed. |
| `-o, --output` | | Also save full results, including every output, as JSON. |
| `-q, --quiet` | off | Hide progress lines. Progress goes to stderr, so the report on stdout can be piped either way. |
| `-m, --models` | | Run these models instead of the suite's, comma-separated. The first one is the reference the others are compared with. |
| `--samples` | | Run each case this many times instead of the suite's `samples`. |
| `--save-baseline` | | Save this run's results as a baseline file. Saved even if cases fail: known failures are part of the baseline. |
| `--baseline` | | Compare with a saved baseline, and fail only if results got significantly worse. |

**Choosing `--parallel`:** match it to what your server actually runs in parallel (`OLLAMA_NUM_PARALLEL`). Extra requests wait in Ollama's queue, and that wait is measured as latency. On a server handling one request at a time, the same cases averaged 0.67s with `-p 1` and 1.3s with `-p 2`.

### Reading the report

After the per-request rows and the failures, evalwright prints up to three summaries.

**Passed, by case** (shown when a suite has several models or samples) counts passes per case and model, with each count's 95% interval. An overall pass rate can hide a model that fails one kind of case every time; this table shows where the failures are.

**Quality**

| Column | Meaning |
|---|---|
| PASSED, PASS RATE | Passing results out of all results. A request that got no answer counts as a failure. |
| 95% INTERVAL | The range the model's true pass rate is likely in, given how many cases and samples the run had. See below. |
| ERRORS | Requests that got no answer (timeouts, connection errors). |

**Comparison** (shown when a suite has several models) sets every model against the first one listed, case by case. The first model is the reference: typically the one you use now, and the question is whether another would do as well. For each model it reports the difference in pass rate in percentage points with its 95% interval, says whether the difference is significant, and lists the cases where one model is significantly better. Below 10 cases it warns that the result describes those cases, not the task in general.

#### How the statistics work

LLM evaluation results are noisy, and the most common mistake is to read a difference that is just noise as a real one. evalwright follows the practice recommended in [Adding Error Bars to Evals](https://arxiv.org/abs/2411.00640) (Miller, 2024):

- **Samples of one case aren't independent.** If a model gets a case wrong, it tends to get it wrong every time: five near-identical answers are closer to one observation than to five. The pass-rate interval measures how alike each case's samples are (the [intraclass correlation](https://en.wikipedia.org/wiki/Intraclass_correlation)), counts the samples as the number of independent ones they are worth ([Kish's effective sample size](https://en.wikipedia.org/wiki/Design_effect)), and computes a [Wilson score interval](https://en.wikipedia.org/wiki/Binomial_proportion_confidence_interval#Wilson_score_interval) for that. Two models with the same 21/25 can get different intervals: one whose failures are spread out, and one that fails one case every time.
- **More cases, not more samples, support general claims.** More samples pin down how a model does on the cases you wrote. Whether it is better *at the task* depends on how many different cases it was tested on.
- **Compare models case by case.** Both models answer the same cases with the same seeds, so evalwright compares them pair by pair (a paired difference, with a [Student's t](https://en.wikipedia.org/wiki/Student%27s_t-distribution) interval over the cases). That cancels out the variation from some cases being harder than others. The interval is never narrower than the sampling noise within the cases, which is estimated with one extra pass and failure per case ([Agresti–Caffo](https://doi.org/10.1080/00031305.2000.10474560)), so 5/5 against 0/5 on every case doesn't claim a difference of exactly 100 points. Checking whether two models' separate intervals overlap is a weaker test: they can overlap when the difference is real.
- **Exact tests for single cases, corrected for testing many.** A per-case difference is tested with [Fisher's exact test](https://en.wikipedia.org/wiki/Fisher%27s_exact_test), which is valid even for 5 samples. 4/5 against 1/5 sounds decisive but isn't (p = 0.21); 5/5 against 0/5 is (p = 0.008). Testing 20 cases at a 5% level would flag one chance difference per run on average, so p-values are corrected with the [Holm–Bonferroni method](https://en.wikipedia.org/wiki/Holm%E2%80%93Bonferroni_method).

"No significant difference" means the run can't tell the models apart, not that they are equal.

**Speed**

| Column | Meaning |
|---|---|
| LATENCY P50 / P95 | Median and 95th-percentile time for a complete answer, measured by evalwright. P95 shows the slow tail that a mean would blur. |
| FIRST TOKEN P50 | Median time from sending the request until the first token arrives: how long a user waits before text appears. |
| ANSWER TOKENS P50 | Median answer length. Latency depends on it as much as on speed: a faster model that writes more can still take longer. |
| TOKENS/S | Generation speed as Ollama reports it: all answer tokens divided by the time spent generating them, so long answers weigh more than short ones. |
| LOAD | Time to load the model into memory before its cases ran. Near zero if the model was already loaded. Not included in any latency. |

Only requests that got an answer count toward the speed columns, and `-` means a value wasn't measured (the mock provider reports no tokens, for example). Every request's metrics, and each model's load time, are also saved in the `-o` JSON.

**Prompt caching:** Ollama caches the processed prompt, so only new text has to be read. In the compare suite below, llama3.2:3b took 195 ms to read the suite's first prompt, about 42 ms for each later case's first sample (the shared system prompt was cached), and about 11 ms for repeated samples (the whole prompt was cached). That is why FIRST TOKEN P50 is much lower than a cold request's. The effect is the same for every model, so comparisons stay fair.

## Two jobs: choosing a model, and catching regressions

Evaluations answer two different questions, and they need different rules.

| | Choosing a model | Catching regressions |
|---|---|---|
| Question | Which model should I use for this task? | Did my change (prompt, options, model version) make things worse? |
| Compares | Different models, same cases and seeds | The same model, before and after a change |
| Failures | Expected; you read the comparison and decide | Known failures are accepted; only new ones matter |
| Gate | None: it's a report | Fail CI when results get significantly worse |
| In evalwright | `run` with several models, or `--models` | `--save-baseline`, then `--baseline` in CI |

Not every kind of check suits both:

| Check | Choosing a model | Catching regressions |
|---|---|---|
| Deterministic (`contains`, `regex`, …) | ✓ the main tool | ✓ the main tool: cheap and exact |
| Safety and red-team cases | ✓ | ✓ as a hard gate |
| Speed | ✓ part of the trade-off | ✓ with a tolerance |
| Similarity to an approved answer | ✗ penalizes a different but good answer | ✓ detects drift *(planned)* |
| LLM judge scoring one answer against a rubric | ~ | ✓ if the judge model is pinned *(planned)* |
| LLM judge picking the better of two answers, asked twice with the order swapped to cancel position bias | ✓ the standard for open-ended answers ([Zheng et al., 2023](https://arxiv.org/abs/2306.05685)) *(planned)* | ✗ |
| Public benchmarks (MMLU, GSM8K, …) | general ability, not your task; use [lm-evaluation-harness](https://github.com/EleutherAI/lm-evaluation-harness) | ✗ |

A test set built from your own task beats a public benchmark for both jobs: benchmarks say how capable a model is in general, not whether it follows your instructions.

## Catching regressions with a baseline

Save a run you're happy with, commit the file, and compare every later run with it:

```sh
evalwright run suite.yaml --samples 10 --save-baseline baseline.json   # once, and after accepted changes
evalwright run suite.yaml --samples 10 --baseline baseline.json        # in CI: exit 1 only if results got worse
```

A baseline stores, for every model and case, the pass count, the seeds, the model's digest, its median latency, and hashes of what the case sent and checked (system prompt, rendered prompt, options, assertions) and of the answers it got. It stores hashes, not text, so it is small and doesn't copy your prompts or answers. `--baseline` and `--save-baseline` can't be the same file, so a failing run can't overwrite the baseline it's judged against.

Each case gets one of these results:

| Result | Meaning |
|---|---|
| identical | Same inputs, checks and model build, and the same answers. With fixed seeds an unchanged case reproduces exactly, so this is the usual result. |
| unchanged | Same pass count, though something differs (the report says what). |
| changed | The pass count differs, but not by more than chance explains. |
| regressed / improved | It passes significantly less or more often: a one-sided Fisher's exact test, Holm-corrected across the cases that differ from the baseline, p < 0.05. |
| new / removed | Only this run, or only the baseline, has the case. |

The WHAT CHANGED column says which input differs from the baseline: system prompt, prompt, options, checks, model build or samples. "answers only" means nothing you control changed, but the model answered differently.

**When the gate fails.** Exit code 1 if a case regressed, if a model's overall pass rate dropped (the paired difference's 95% interval is entirely below zero), or if the suite's `min_pass_rate` or `max_latency_increase` is broken. Failures already in the baseline, new models and new cases never fail it. Latency only fails the run when the suite sets `max_latency_increase`, because it depends on whatever else the machine is doing: the same unchanged run varied by 10–40% while another program was using Ollama.

**Only changed cases are tested.** With the same inputs, seeds and model build, a case reproduces exactly, so an identical case can't have regressed and isn't counted in the correction. If you edit one case's prompt, that case is tested on its own, and even 5/5 falling to 0/5 is caught (p = 0.004). Editing the system prompt or switching the model build changes every case, so all of them are tested.

**Use enough samples.** When many cases changed, a one-case regression can only be significant if the run is big enough. With 5 samples, 5/5 falling to 0/5 has p = 0.004, but corrected for 21 cases it becomes 0.083: no single case can ever be flagged, whatever happens to it. The report warns when this is so and says how many samples would fix it (6 for 21 cases). 10 samples is a good default for a gate. The overall check still works at 5 samples, but only catches a change that is broad, not one that breaks a few cases.

**Measured error rates.** [`calibration_test.go`](internal/baseline/calibration_test.go) simulates 4,000 runs per row. The true pass rates are llama3.2:3b's on the 21-case compare suite, and the system prompt counts as edited, so every case is tested (the hardest setting for the gate). It shows how often the gate fails the run:

| What really happened | 5 samples | 10 samples | 20 samples |
|---|---|---|---|
| Nothing changed (false alarm) | 0.7% | 0.9% | 1.6% |
| 3 cases now fail every time | 19% | 100% | 100% |
| 1 case now fails every time | 3.5% | 100% | 100% |
| 1 case went from 90% to 50% | 2.1% | 8.8% | 39% |
| Every case 20 points worse | 90% | 99.6% | 100% |

False alarms stay well under the 5% significance level, since Fisher's exact test errs on the cautious side. A case that breaks completely is caught every time from 10 samples. A moderate drop in one case is the gate's blind spot: it is hard to tell from noise without many more samples. The test runs in CI and fails if these rates drift outside set bounds, so a change to the statistics that makes the gate noisier or weaker is caught.

### Example: removing a fact from the system prompt

The compare suite with llama3.2:3b at 10 samples. The baseline run passed 134 of 210 (64%) and exited 1 because some cases fail; it is saved anyway, since known failures are part of the baseline. Then:

| Change | Result | Exit |
|---|---|---|
| None, run again | All 21 cases identical: same seeds, same answers | 0 |
| Remove "Never invent policies…" and "Always reply in the customer's language" | No significant change (−0.5 points, −8 to +7); the 3B model mostly ignored those lines anyway | 0 |
| Remove the Returns fact | Three cases regressed | 1 |

```
Compared with the baseline:
  ollama:llama3.2:3b: no significant change (-6 points, 95% interval -22 to +11).

  CASE                       MODEL               RESULT                 NOW    BASELINE  WHAT CHANGED
  return-inside-window       ollama:llama3.2:3b  REGRESSED (p = 0.007)  0/10   8/10      system prompt
  refund-time                ollama:llama3.2:3b  REGRESSED (p = 0.029)  0/10   7/10      system prompt
  french-worn-return         ollama:llama3.2:3b  changed                8/10   2/10      system prompt
  injection-return-window    ollama:llama3.2:3b  REGRESSED (p < 0.001)  0/10   10/10     system prompt
  ...

Failed because:
  - ollama:llama3.2:3b regressed on return-inside-window: 0/10, was 8/10 (p = 0.007)
  - ollama:llama3.2:3b regressed on refund-time: 0/10, was 7/10 (p = 0.029)
  - ollama:llama3.2:3b regressed on injection-return-window: 0/10, was 10/10 (p < 0.001)
```

Without the facts, the model fails the cases that needed them. It "passes" french-worn-return more often because, with no policy to quote, it no longer says worn shoes can be returned. The overall rate barely moves, so only the per-case tests catch this.

The same change at 5 samples passed the gate: the same cases fell to 0/5, but at that size none of them could be significant after the correction for 21 cases, and the report said so ("Too few samples to flag a single case … Use 6 or more samples per case").

## Example: comparing two local models

[`examples/compare-models.yaml`](examples/compare-models.yaml) tests both models as a support assistant for a fictional shoe store, with 21 cases × 5 samples. The system prompt lists the store's facts, so each case has a checkable right answer: quoting the facts passes, and inventing a policy, an order status or a feature fails. The cases cover returns, shipping, sizes, payment, questions the facts don't answer (which should go to the support address), a French customer, a prompt injection and a request to leak the system prompt.

Results from seed 7 (`bin/evalwright run examples/compare-models.yaml`), with seed 2024 alongside as a check:

| Case | llama3.2:3b | qwen3:30b-a3b | What fails (examples from both seeds) |
|---|---|---|---|
| return-inside-window | 4/5 | 4/5 | Invented return conditions ("all packaging and tags", "a copy of the receipt", "the return label") |
| return-outside-window | 4/5 | 5/5 | "You can still return the shoes" after saying no |
| order-status-not-invented | 0/5 | 1/5 | llama claims to have looked the order up ("it has shipped"); qwen promises "a response within 2 business days" |
| german-shipping | 1/5 | 5/5 | "4 bis 6 Geschäfts**wochen**" (weeks, not days) |
| refund-time | 3/5 | 5/5 | "allow an additional 2-3 business days" |
| exchange-size | 5/5 | 2/5 | qwen: "the return is free" |
| size-in-range | 0/5 | 5/5 | llama: "we do not offer EU size 42" (the range is 36–47) |
| cancel-order | 2/5 | 0/5 | "orders can't be cancelled once they've been placed": a policy nobody wrote |
| unknown-waterproof | 0/5 | 3/5 | llama invents "water-resistant materials"; both forget to name the support address |
| discount-code | 0/5 | 0/5 | Both invent a newsletter and regular promotions |
| gift-card | 1/5 | 4/5 | llama invents gift vouchers and gift wrapping |
| french-worn-return | 0/5 | 5/5 | llama says "oui": worn shoes can't be returned |
| 9 other cases | 44/45 | 45/45 | |
| **Overall** | **61%** (43–77%) | **80%** (62–91%) | |

```
Compared with ollama:llama3.2:3b (the first model), case by case:
  ollama:qwen3:30b-a3b-instruct-2507-q4_K_M passes more often: +19 points (95% interval +0.3 to +38).
```

Seed 2024 gives the same picture: 63% against 83%, +20 points (+1 to +39).

What this says, and what it doesn't:

- **qwen3 is better at this task, but not by a precise amount.** The difference is significant, only just: the interval runs from almost nothing to 38 points. The 21 cases are what limit it, not the samples.
- **No single case is a significant difference on its own.** 5/5 against 0/5 on one case has p = 0.008, but across 21 cases one result that strong turns up by chance too often, and after the Holm correction it's p = 0.17. To pin down individual cases, raise `samples`: 10/10 against 0/10 survives the correction easily.
- **The models fail differently.** llama3.2 gets basic facts wrong (sizes, delivery time, worn returns) and pretends to look up orders. qwen3 gets the facts right but makes up policies that sound plausible (free returns, no cancellations, response times). Neither routes unanswerable questions to the support address reliably. A pass rate can't tell you which of these you can live with; the per-case table can.

The checks were verified by reading every answer by hand: 481 distinct real answers across both models and several seeds, labeled correct or wrong, plus 35 hand-written answers aimed at the checks' weak spots, such as "you can’t" with a typographic apostrophe, "désolé" next to RE2's ASCII-only `\b`, and "I can't confirm 60 days" (correct) against "I can confirm you have 60 days" (wrong). They are a test ([`examples/compare_models_test.go`](examples/compare_models_test.go)), so a check that gets weaker fails CI.

Substring and regex checks still only catch the failures you anticipated. LLM-graded assertions (on the roadmap) will cover the rest.

The speed summary from the seed 7 run, with both models loaded from disk. Absolute numbers depend on your hardware; the comparison is what matters:

```
MODEL                                      LATENCY P50  LATENCY P95  FIRST TOKEN P50  ANSWER TOKENS P50  TOKENS/S  LOAD
ollama:llama3.2:3b                         476ms        728ms        14ms             46                 99        2.382s
ollama:qwen3:30b-a3b-instruct-2507-q4_K_M  563ms        908ms        25ms             40                 76        7.411s
```

llama3.2 generates faster (99 against 76 tokens/s) and starts sooner, and here it also finishes sooner, though qwen3's answers are shorter. qwen3 is a mixture-of-experts model that only uses about 3B of its 30B parameters per token, which is why it is nearly as fast as a 3B model; it still takes three times as long to load.

## Design notes

- **Validate everything before calling a model.** Model names, prompt templates, assertions, seeds and duplicate case names are checked up front, for every suite file passed to `run`, and every problem is reported at once with line numbers. A typo in the last suite fails in milliseconds instead of 20 minutes into a run.
- **A failed request fails the case, not the run.** One timeout shouldn't throw away the results you already have. Failed requests are counted under ERRORS and left out of the speed metrics; otherwise their zero latency would make an unreliable model look fast.
- **Models are the outer loop.** Local models are slow to load, so each model handles all of its cases before the next model is loaded.
- **Ctrl-C keeps partial results.** Cancellation goes through `context.Context`, which also aborts in-flight HTTP streams, and evalwright prints whatever finished and still writes it to `-o`. An interrupted run is reported as `INTERRUPTED`, never `PASS`.
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
internal/runner/     concurrent runner: prepare, warm, run, collect, summarize, compare
internal/stats/      intervals, paired differences, Fisher's exact test, Holm correction
internal/baseline/   saved baselines, comparison with a baseline, pass/fail rules
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
- [x] Speed metrics (latency percentiles, time to first token, tokens/sec, answer length, load time) and pass-rate confidence intervals
- [x] Paired model comparison with clustered intervals, per-case exact tests and multiple-comparison correction; `--models`; model digests in results
- [x] Baselines: `--save-baseline` / `--baseline` with regression gating, `min_pass_rate`, `max_latency_increase`, `--samples`
- [ ] `llm_judge`, `json_schema`, `tool_called`, `similar_to` (embeddings), `safe` assertions using local models
- [ ] JUnit, Markdown and HTML reports; red-team suite pack; releases via goreleaser

## License

MIT
