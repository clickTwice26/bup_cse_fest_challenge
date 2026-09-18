# GridWise LLM — BUP CSE Fest 2026 Hackathon (Online Preliminary)

An HTTP service that reads free-text campus **operator notes** with a language model,
validates the interpretation deterministically, applies it to a 24-hour energy
optimisation, and returns both the machine-checkable interpretation and a cost-minimal
schedule.

* `GET /health` → `{"status":"ok"}`
* `POST /optimize-energy` → interpretation + 24-hour plan

Go 1.27 · gonum Simplex (exact LP) · Groq / Gemini / xAI via OpenAI-compatible APIs.

---

## Documentation

| Document | Contents |
|---|---|
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | How it works and why: the central design decision, component structure, request lifecycle, interpretation pipeline, provider failover, the LP formulation, the relaxation ladder, and known limitations. With sequence and flow diagrams. |
| [docs/API.md](docs/API.md) | Endpoint reference: request and response schemas, directive types, response invariants, status codes, a worked example, and the full interpretation conventions. |

---

## Project layout

```
cmd/server/            entrypoint: config, wiring, HTTP server
internal/
  energy/              domain model, exact LP optimiser, replay validator
    model.go             request/response types, directive constants
    lp.go                standard-form LP builder (slacks, sign handling)
    optimizer.go         constraint projection, Simplex solve, plan assembly
    replay.go            independent re-validation of a finished plan
  interpret/           note understanding
    prompt.go            frozen system prompt
    interpreter.go       provider failover, concurrency, cache, repair ladder
    guardrails.go        coercion, hour enumeration, unit resolution
    crosscheck.go        deterministic cross-check + last-resort extractor
  httpapi/             routes, validation, error mapping, relaxation ladder
testdata/              organiser public sample pack
tests/                 end-to-end suites (harness, paraphrase, edge)
docs/                  problem statement and rubric
```

`internal/energy` has no dependency on `internal/interpret`: the optimiser knows
nothing about language models. `internal/httpapi` depends on an `Interpreter`
interface rather than a concrete provider, so the LLM layer is swappable.

---

## Architecture

```
request
  → structural validation            400 on malformed or invalid input
  → LLM interpreter                  one call PER NOTE, issued concurrently
  → deterministic guardrails         all arithmetic + hour enumeration
  → exact LP optimiser               gonum Simplex, standard form
  → replay validator                 independent re-check of our own output
  → response
```

### The core design decision

The model is asked for **meaning**, never for final numbers. It returns
`window_start_hour` / `window_end_hour_exclusive`, plus a `value_semantics` tag naming
what its number *means* — and deterministic Go code does every conversion:

```go
hours  = range(start, end)                       // off-by-one is structurally impossible
factor = value/100  or  1 - value/100            // chosen by value_semantics, not by the model
reserve = value  or  value/100 * capacity_kwh    // percentages resolved against the request
```

This removes the two failure modes that dominate this problem — **off-by-one hour arrays**
and **reduction-vs-remaining inversion** ("an 80% reduction" means `factor = 0.2`, not `0.8`).

`note_index` is assigned by **position** in the orchestrator loop and never read from the
model, so "exactly one entry per note in `note_index` order" holds by construction.

### LLM role and compliance

The language model is the **sole originator** of every `directive_interpretation` entry.
The deterministic layer may only normalise, correct a single axis of, or suppress an
LLM-originated directive — it never originates one. The one exception is an explicit
last-resort safe-failure path used only when **every** configured provider is unreachable,
so that the service degrades instead of crashing; this is logged when it happens.

`plan_summary` is generated deterministically and deliberately makes **no** model call.

---

## Quickstart (from a clean machine)

```bash
git clone <this-repo> && cd <this-repo>
cp .env.example .env          # then edit .env and add your key(s)
set -a && source .env && set +a
go mod download
go run ./cmd/server           # listens on :8000
```

Verify:

```bash
curl -s localhost:8000/health
# {"status":"ok"}
```

Run one public sample case end to end:

```bash
curl -s -X POST localhost:8000/optimize-energy \
  -H 'Content-Type: application/json' \
  -d '{
    "scenario_id": "SAMPLE-01",
    "operator_notes": [
      "Facilities will wash the rooftop solar panels from noon until 2 PM. During cleaning, usable solar should be treated as roughly 25% of the forecast.",
      "The sports office moved next month'"'"'s registration deadline."
    ],
    "hours": [{"hour":0,"demand_kwh":90,"solar_kwh":0,"tariff_bdt_per_kwh":6}],
    "battery": {"capacity_kwh":220,"initial_energy_kwh":110,"minimum_energy_kwh":40,
                "max_charge_kwh_per_hour":50,"max_discharge_kwh_per_hour":50}
  }'
```

(The real request needs all 24 hour entries — see the public sample pack.)

Expected for SAMPLE-01: note 0 → `solar_reduction`, `hours [12,13]`, `factor 0.25`;
note 1 → `no_op` with `applies:false`; `total_cost_bdt` **38365.00**.

### Full regression run

With the service running, replay all 10 public cases:

```bash
python3 tests/harness.py http://localhost:8000
```

It checks interpretation against the reference, replays the returned schedule against
every GridWise rule, and compares cost to the reference optimum.

Paraphrase robustness and adversarial/edge behaviour:

```bash
python3 tests/paraphrase_test.py http://localhost:8000
python3 tests/edge_test.py http://localhost:8000
```

Go unit tests (optimiser against the reference costs, plus guardrail rules):

```bash
go test ./...
```

---

## Configuration

Environment variables — **names only, never commit values**. See `.env.example`.

| Variable | Purpose | Default |
|---|---|---|
| `GROQ_API_KEY` | Primary provider ([console.groq.com](https://console.groq.com)) | — |
| `GROQ_BASE_URL` | OpenAI-compatible endpoint | `https://api.groq.com/openai/v1` |
| `GROQ_MODEL` | Model id | `llama-3.3-70b-versatile` |
| `GEMINI_API_KEY` | Backup provider ([aistudio.google.com](https://aistudio.google.com/apikey)) | — |
| `GEMINI_BASE_URL` | OpenAI-compatible endpoint | `.../v1beta/openai/` |
| `GEMINI_MODEL` | Model id | `gemini-2.0-flash` |
| `XAI_API_KEY` | Optional third provider (xAI Grok) | — |
| `PORT` | Listen port | `8000` |
| `LLM_TIMEOUT_SECONDS` | Per-call timeout | `6` |

Providers are tried in order **Groq → Gemini → xAI**, each with one JSON-repair retry.
At least one key must be set for the service to satisfy the challenge's LLM requirement.

---

## Docker

```bash
docker build -t gridwise:local .
docker run -d --restart=always -p 80:8000 \
  -e GROQ_API_KEY=... -e GEMINI_API_KEY=... \
  --name gridwise gridwise:local
curl -s localhost/health
```

The image is a 30 MB static binary on Alpine, runs as a non-root user, binds `0.0.0.0`,
and contains **no baked-in credentials**.

---

## Behaviour notes

**Time windows** are start-inclusive, end-exclusive: "noon until 2 PM" → `[12,13]`;
"between 11 AM and 2 PM" → `[11,12,13]`. A window ending at midnight uses 24. A window
that wraps past midnight ("11 PM to 2 AM") yields `[0,1,23]`. A degenerate window yields
the single start hour, never an empty array.

**Solar** is always used to the maximum available, which is weakly optimal (grid price is
non-negative) and weakly more feasible. `grid_kwh` is then the remainder, so the energy
balance holds exactly by construction rather than by rounding luck.

**Totals** (`total_grid_kwh`, `total_cost_bdt`, `peak_grid_kwh`) are computed from the
**rounded, published** `hourly_plan` values, so they can never disagree with the plan the
judge replays.

**Never 5xx on a valid request.** If the LP is infeasible the service relaxes in order —
grid caps → end-of-day neutrality → directive reserves → a closed-form idle baseline that
satisfies every base rule unconditionally — and still returns a well-formed `200`.
`directive_interpretation` is **never** edited when relaxing, because interpretation is
scored separately from application.

**Errors:** malformed JSON or a structurally invalid request returns `400`. Provider
response bodies are never echoed, and explanations are scrubbed for key-like tokens and
stack traces before being returned.

---

## Known limitations

* A note that expresses two directives at once yields one entry (the spec defines one
  directive per note); the explicit operational instruction wins.
* "Peak hours" with no clock time is not inferred from the tariff curve.
* The emergency fallback extractor cannot read fraction words ("about half"); it is
  deliberately conservative and returns `no_op` rather than guess. It only runs when every
  provider is unreachable.

## Dependencies

* [gonum](https://gonum.org) — `optimize/convex/lp` (Simplex) and `mat`. BSD-3-Clause.
* Go standard library for HTTP, JSON and concurrency — no web framework.
* `harness.py` uses only the Python standard library.
