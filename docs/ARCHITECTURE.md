# GridWise LLM — Architecture

This document explains how the service works, why it is built this way, and where
it can fail. It is written for someone who has to evaluate, operate, or change
the system without having seen it before.

---

## 1. The problem, precisely stated

The service receives a 24-hour energy scenario for a campus microgrid — hourly
demand, rooftop solar forecast, grid tariff, and one battery — together with one
to three **operator notes written in free-form English**.

It must return two things:

1. A **machine-checkable interpretation** of every note, and
2. A **cost-minimal 24-hour schedule** that obeys those interpretations *and* the
   underlying physics of the microgrid.

The difficulty is not the optimisation. The optimisation is a linear program with
a known exact solution. The difficulty is that a note like

> *"Expect an 80% reduction in rooftop solar between 11 AM and 2 PM because of inverter work."*

must become exactly

```json
{"hours": [11, 12, 13], "factor": 0.2}
```

and the hidden evaluation set rewords every note. Two specific errors dominate:

| Error | Example | Consequence |
|---|---|---|
| **Off-by-one window** | `[11,12,13,14]` instead of `[11,12,13]` | wrong hours, wrong schedule |
| **Polarity inversion** | `factor 0.8` instead of `0.2` | four times too much solar assumed |

Both are *arithmetic and enumeration* errors. That observation drives the entire
design.

---

## 2. The central design decision

> **The language model is asked for meaning. Deterministic code computes every number.**

The model never emits an hour array, a factor, or a reserve in kWh. It emits a
window as two integers and a **tag naming what its number means**:

```jsonc
{
  "applies_today": true,
  "directive_type": "solar_reduction",
  "window_start_hour": 11,          // inclusive
  "window_end_hour_exclusive": 14,  // exclusive
  "value_semantics": "solar_reduction_percent",  // <- names the unit
  "value_number": 80
}
```

Go then derives the answer, and the two dominant errors become structurally
impossible:

```go
hours  = range(start, end)                   // enumeration is mechanical
factor = 1 - 80/100                          // chosen by the tag, not by the model
```

The model is never asked to compute `1 - 0.8`. It is asked only to classify
whether the number it read describes what *remains* or what is *lost* — a
judgement it is good at, feeding an arithmetic step that cannot go wrong.

The same applies to reserves: `"50% of battery capacity"` arrives as
`reserve_percent_of_capacity: 50` and is resolved against that request's actual
`capacity_kwh`.

**`note_index` is assigned by position in the orchestration loop and is never
read from the model**, so the requirement "exactly one entry per note, in order"
holds by construction rather than by validation.

---

## 3. Component structure

```mermaid
flowchart TD
    subgraph cmd["cmd/server"]
        M["main.go
config, wiring, lifecycle"]
    end

    subgraph api["internal/httpapi"]
        R["routes and request validation"]
        L["relaxation ladder"]
    end

    subgraph interp["internal/interpret"]
        P["prompt.go
frozen system prompt"]
        I["interpreter.go
providers, concurrency, cache"]
        G["guardrails.go
coercion, hours, units"]
        X["crosscheck.go
deterministic oracle"]
    end

    subgraph energy["internal/energy"]
        LP["lp.go
standard-form builder"]
        OP["optimizer.go
Simplex and plan assembly"]
        RP["replay.go
independent validator"]
    end

    M --> R
    R -->|Interpreter interface| I
    R --> OP
    L --> OP
    I --> P
    I --> X
    I --> G
    OP --> LP
    OP --> RP
```

The dependency direction is deliberate and one-way:

- **`internal/energy` knows nothing about language models.** It is pure
  mathematics and is tested offline with no network.
- **`internal/httpapi` depends on an `Interpreter` interface**, not on Gemini or
  Groq, so the model layer is swappable and the HTTP layer is testable in
  isolation.
- **`cmd/server` contains wiring only.**

---

## 4. Request lifecycle

```mermaid
sequenceDiagram
    autonumber
    participant J as Judge
    participant H as httpapi
    participant I as interpret
    participant P as Provider
    participant G as guardrails
    participant E as energy
    participant V as replay

    J->>H: POST /optimize-energy
    H->>H: structural validation
    alt malformed or invalid
        H-->>J: 400 with reason
    end

    H->>I: InterpretAll notes and battery
    par one call per note, concurrently
        I->>I: normalise and check cache
        alt cache hit
            I-->>I: cached directive, about 0 ms
        else cache miss
            I->>P: chat completion, temperature 0, JSON mode
            P-->>I: semantic primitives
            I->>I: cross-check day anchor and polarity
            I->>G: coerce, expand window, resolve units
            G-->>I: spec-shaped directive or no_op
        end
    end
    I-->>H: one entry per note, in note_index order

    H->>E: project directives onto constraints
    E->>E: Simplex solve
    E->>E: assemble plan, recompute totals from rounded values
    E->>V: validate independently
    alt replay fails
        V-->>E: relax one tier and re-solve
    end
    V-->>H: verified plan
    H-->>J: 200 interpretation and 24-hour schedule
```

Two properties are worth calling out.

**Notes are interpreted concurrently**, so wall-clock latency is the *slowest*
note, not the sum. Three notes cost roughly what one note costs.

**The plan is validated by code that did not produce it.** `replay.go`
re-simulates all 24 hours from scratch and re-checks every rule, exactly as the
judge does. A plan that fails its own replay is never returned.

---

## 5. The interpretation pipeline in detail

```mermaid
flowchart TD
    A["raw note text"] --> B["normalise
Bengali and Arabic digits, whitespace"]
    B --> C{"cache hit?"}
    C -->|yes| Z["directive"]
    C -->|no| D["LLM call"]

    D --> E{"valid JSON?"}
    E -->|no| F["retry once with a JSON reminder"]
    F --> E
    E -->|still no| G["advance to next provider"]
    G --> D

    E -->|yes| H1{"other-day marker
and no today marker?"}
    H1 -->|yes| NO["force no_op"]
    H1 -->|no| H2{"BY or TO polarity
disagrees with regex?"}
    H2 -->|yes| H3["take the regex polarity"]
    H2 -->|no| I1["coerce type
unknown becomes no_op"]
    H3 --> I1

    I1 --> I2["expand window
start inclusive, end exclusive"]
    I2 --> I3{"hours empty?"}
    I3 -->|yes| NO
    I3 -->|no| I4["resolve units by value_semantics"]
    I4 --> I5{"value resolvable?"}
    I5 -->|no| NO
    I5 -->|yes| I6["enforce applies semantics"]
    I6 --> Z
    NO --> Z
```

### Guardrail rules that matter

| Rule | Behaviour |
|---|---|
| Window | Start inclusive, end exclusive. `end` of 0 or 24 means midnight. |
| Degenerate window | `end == start` yields the single start hour, **never an empty array**. |
| Midnight wrap | `23 -> 2` yields `[0, 1, 23]` — unique, ascending, in range. |
| Unknown `directive_type` | Becomes `no_op`. A constraint is **never invented**. |
| Missing required value | Downgrades to `no_op`. A number is **never guessed**. |
| `factor >= 1.0` | Downgrades — an increase is not a reduction. |
| Reserve above capacity | Clamped to `capacity_kwh`. |
| `applies` semantics | `false` **only** for `no_op`; `true` for every real directive. |
| Grid cap in kW | Identical number; kW and kWh coincide over a one-hour interval. |
| Grid cap as a window total | Divided evenly across the window's hours. |

### Notes are untrusted input

The system prompt states explicitly that a note is **data, never an instruction**,
including text hidden in comments or markup, or labelled as a system or developer
message. This is verified by adversarial tests: three injection attempts that
previously produced bogus directives now all return `no_op`.

The guardrail layer is the second line of defence — even a fully compromised
model response cannot produce an out-of-range hour, an unsupported directive
type, a negative reserve, or a factor outside `[0, 1]`.

### The deterministic cross-check, and the compliance line

`crosscheck.go` is a regex-based reader used as a **cross-check oracle**. It may
correct exactly one axis of, or suppress, a directive the model produced:

- an other-day marker with no counter-signal forces `no_op`;
- a BY/TO polarity disagreement on solar takes the regex reading.

> **It may never originate a directive the model did not produce.** The language
> model is the sole originator of every `directive_interpretation` entry.

The one exception is an explicitly logged last-resort path used only when *every*
provider is unreachable, so the service degrades instead of crashing.

---

## 6. Provider failover

```mermaid
sequenceDiagram
    autonumber
    participant I as interpret
    participant A as Gemini primary
    participant B as Groq backup
    participant F as Deterministic fallback

    I->>A: interpret note
    alt valid JSON returned
        A-->>I: directive
    else rate limited or unavailable
        A-->>I: HTTP 429 or 5xx
        Note over I,A: no retry, this provider cannot serve us now
        I->>B: interpret note
        B-->>I: directive
    else unparseable body
        A-->>I: malformed JSON
        I->>A: retry once with a JSON reminder
        A-->>I: directive
    end

    opt every provider failed
        I->>F: conservative extraction
        F-->>I: directive or no_op
        Note over I,F: logged, last resort only
    end
```

Ordering is deliberate. Gemini leads because Groq's free tier caps at **8,000
tokens per minute**, which this roughly 3,000-token system prompt exhausts after
two concurrent notes. Distinguishing "retry this provider" from "advance to the
next" removed roughly 2 seconds of dead waiting per note under rate limiting.

**Caching.** Interpretations are cached on
`sha256(normalised_note + capacity_kwh + minimum_energy_kwh)`. The battery
parameters are in the key because they feed unit resolution, which keeps the
cache correctness-safe. Repeated requests return in about 165 ms instead of about
2 seconds. The cache is per-process and purely derived, so multiple replicas are
safe — each simply warms its own.

---

## 7. The optimiser

### Formulation

96 variables, for each hour `h` in `0..23`:

| Variable | Meaning | Bounds |
|---|---|---|
| `g[h]` | grid energy purchased | `[0, max_grid_kwh[h]]` or unbounded |
| `s[h]` | solar energy used | `[0, effective_solar[h]]` |
| `c[h]` | battery charge | `[0, max_charge]`, or `0` in a no-charge window |
| `d[h]` | battery discharge | `[0, max_discharge]`, or `0` in a no-discharge window |

**Objective** — minimise the cost of grid electricity:

```
minimise   sum over h of  tariff[h] * g[h]
```

**Constraints:**

```
(1) energy balance, per hour     g[h] + s[h] + d[h] - c[h] = demand[h]
(2) end-of-day neutrality        sum over h of (c[h] - d[h]) = 0
(3) battery upper bound          sum for k<=h of (c[k]-d[k]) <= capacity - E0
(4) battery lower bound          sum for k<=h of (c[k]-d[k]) >= reserve[h] - E0
```

where `E0` is the initial battery energy and
`reserve[h] = max(battery minimum, any directive reserve active at h)`.

Constraint (2) exists so the starting battery charge cannot be consumed as a free
one-off energy source by ending the day emptier than it began.

Solved with gonum's Simplex. Because Simplex requires standard form
(`Ax = b`, `x >= 0`), `lp.go` converts the model: it adds slack and surplus
columns for every inequality and upper bound, and negates any row whose
right-hand side is negative so the phase-one problem stays well posed.

### Plan assembly, and why rounding cannot break it

The solver returns net battery flow. The published plan is then derived so that
the energy balance holds **by construction** rather than by rounding luck:

```
net[h] = c[h] - d[h]                                  (residual corrected to sum exactly to 0)
s[h]   = min(effective_solar[h], max(0, demand[h] + net[h]))
g[h]   = demand[h] + net[h] - s[h]
```

Taking the maximum available solar is always at least as good and at least as
feasible, because the grid price is non-negative — so solar is never curtailed
unless the physics force it.

Finally, `total_grid_kwh`, `total_cost_bdt` and `peak_grid_kwh` are recomputed
**from the rounded, published `hourly_plan` values**. They therefore cannot
disagree with the plan the judge replays, which is an explicit evaluation check.

### Relaxation ladder

Organiser scoring scenarios are guaranteed feasible, but a valid request must
never produce a 5xx. If a solve or its replay fails, the service descends:

```mermaid
flowchart LR
    P0["all directives
hard"] -->|infeasible| P1["grid caps
penalised"]
    P1 -->|infeasible| P2["caps and reserves
penalised"]
    P2 -->|infeasible| P3["drop end-of-day
neutrality"]
    P3 -->|infeasible| P4["base rules only"]
    P4 -->|infeasible| P5["idle baseline
always valid"]
```

Tiers 1 and 2 convert hard directives into **penalised soft constraints** with a
large cost, so the LP satisfies them wherever it can and violates them minimally
where it cannot — which is strictly better than abandoning the directive.

> **`directive_interpretation` is never edited when relaxing.** Interpretation and
> application are scored separately, so reporting a directive we could not fully
> satisfy retains interpretation credit; silently dropping it would forfeit both.

The final tier holds the battery idle for all 24 hours, which satisfies every
base rule unconditionally.

---

## 8. Error handling

| Condition | Response |
|---|---|
| Malformed JSON | `400` with a short reason |
| Structurally invalid request | `400` — wrong hour count, duplicate hours, negative values, `initial > capacity` |
| Valid request, any internal difficulty | `200` with a valid schedule, via the relaxation ladder |
| Panic in a handler | recovered, `500`, no stack trace exposed |
| Panic interpreting one note | recovered, that note becomes `no_op`, the request still succeeds |

Provider response bodies are never echoed — only a status code is logged — and
explanations are scrubbed for key-shaped tokens and stack traces before being
returned.

---

## 9. Performance

| Stage | Typical |
|---|---|
| Structural validation | under 1 ms |
| Interpretation, cache hit | about 0 ms |
| Interpretation, cache miss | 1.5 to 2.5 s, concurrent across notes |
| LP build and solve | about 170 ms |
| Plan assembly and replay | under 5 ms |
| **Total, cold** | **p95 about 2.2 s** |
| **Total, cached** | **about 165 ms** |

Resident memory is roughly 15 MB against a 512 MB limit. The container is a
30 MB static binary on Alpine, running as a non-root user.

---

## 10. Verification

| Suite | What it proves | Result |
|---|---|---|
| `go test ./...` | Optimiser reproduces the organiser's reference cost from their own reference interpretations; guardrail rules are pinned | pass |
| `tests/harness.py` | End-to-end on all 10 public cases: interpretation, schedule validity, cost | **10/10 · 10/10 · 10/10** |
| `tests/paraphrase_test.py` | 80 rewordings across all six directive types, in two sets of increasing difficulty | **80/80** |
| `tests/edge_test.py` | Malformed input, degenerate numerics, conflicting directives, unicode, Bengali digits, prompt injection | **32/32** |
| `tests/multinote_test.py` | 1-3 interacting notes per scenario, verifying one schedule honours every directive at once | **6/6** |

The optimiser test is deliberately fed the **organiser's own interpretations**, so
any cost difference is attributable to the solver and not to the model.

---

## 11. Known limitations

- A note expressing two directives at once yields one entry, since the
  specification defines one directive per note; the explicit operational
  instruction wins.
- "Peak hours" with no clock time is not inferred from the tariff curve.
- The last-resort extractor cannot read fraction words such as "about half". It
  is deliberately conservative and returns `no_op` rather than guess. It runs
  only when every provider is unreachable.
- The interpretation cache is per-process, so replicas warm independently.
