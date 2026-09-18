# GridWise LLM — API Reference

Base URL: `https://<app-name>.cubicle.shagato.space`
All requests and responses are `application/json`. No authentication.

---

## `GET /health`

Readiness probe. Returns immediately and touches no external service, so it stays
green even if a model provider is degraded.

**200**
```json
{ "status": "ok" }
```

---

## `POST /optimize-energy`

Interprets the operator notes and returns them alongside a cost-minimal 24-hour
schedule.

### Request

| Field | Type | Required | Notes |
|---|---|---|---|
| `scenario_id` | string | yes | Echoed back unchanged |
| `operator_notes` | string[] | yes | 1–3 free-form notes; an empty array is accepted |
| `hours` | object[24] | yes | Exactly 24 entries, hours 0–23, unique, any order |
| `battery` | object | yes | See below |

**`hours[]` entry**

| Field | Type | Notes |
|---|---|---|
| `hour` | integer | 0–23, unique across the array |
| `demand_kwh` | number | Non-negative; must be met every hour |
| `solar_kwh` | number | Non-negative; forecast *before* any directive |
| `tariff_bdt_per_kwh` | number | Non-negative grid price for that hour |

**`battery`**

| Field | Type | Notes |
|---|---|---|
| `capacity_kwh` | number | Greater than zero |
| `initial_energy_kwh` | number | Must not exceed `capacity_kwh` |
| `minimum_energy_kwh` | number | Base floor, before any directive raises it |
| `max_charge_kwh_per_hour` | number | Per-hour charge limit |
| `max_discharge_kwh_per_hour` | number | Per-hour discharge limit |

### Response — 200

| Field | Type | Meaning |
|---|---|---|
| `scenario_id` | string | Echo of the request |
| `directive_interpretation` | object[] | Exactly one entry per note, in `note_index` order |
| `hourly_plan` | object[24] | The schedule, hours 0–23 |
| `total_grid_kwh` | number | Sum of `grid_kwh`, recomputed from `hourly_plan` |
| `total_cost_bdt` | number | Sum of `grid_kwh × tariff`, recomputed from `hourly_plan` |
| `peak_grid_kwh` | number | Maximum hourly `grid_kwh` |
| `plan_summary` | string | Short human-readable description of the strategy |

**`directive_interpretation[]` entry** — exactly these five fields, no others.

| Field | Type | Meaning |
|---|---|---|
| `note_index` | integer | Zero-based index of the note, always in order |
| `applies` | boolean | `true` for a real directive; `false` **only** for `no_op` |
| `directive_type` | string | One of the six types below |
| `structured_adjustment` | object \| null | Shape depends on the type; `null` **only** for `no_op` |
| `explanation` | string | Short prose; not compared byte-for-byte |

**Directive types and their adjustment shapes**

| `directive_type` | `structured_adjustment` | Meaning |
|---|---|---|
| `solar_reduction` | `{"hours":[…], "factor": 0.0–1.0}` | `factor` is the fraction of solar **remaining** |
| `minimum_battery_reserve` | `{"hours":[…], "minimum_energy_kwh": n}` | Battery must stay at or above `n` in those hours |
| `no_charge_window` | `{"hours":[…]}` | Charging is blocked in those hours |
| `no_discharge_window` | `{"hours":[…]}` | Discharging is blocked in those hours |
| `max_grid_window` | `{"hours":[…], "max_grid_kwh": n}` | Grid import capped at `n` in **each** listed hour |
| `no_op` | `null` | The note does not affect today's schedule |

`hours` is always unique integers 0–23 in ascending order.

**`hourly_plan[]` entry**

| Field | Type | Meaning |
|---|---|---|
| `hour` | integer | 0–23 |
| `grid_kwh` | number | Grid energy purchased, non-negative |
| `solar_used_kwh` | number | Solar consumed; never exceeds effective solar |
| `battery_action` | string | `charge`, `discharge`, or `idle` |
| `battery_kwh` | number | Magnitude of the action; exactly `0` when `idle` |
| `battery_energy_after_kwh` | number | Battery level at the end of the hour |

### Invariants the response always satisfies

For every hour:

```
grid_kwh + solar_used_kwh + discharge = demand_kwh + charge
solar_used_kwh <= solar_kwh × factor           (if a solar_reduction applies)
reserve[h] <= battery_energy_after_kwh <= capacity_kwh
charge <= max_charge_kwh_per_hour
discharge <= max_discharge_kwh_per_hour
```

And across the day, `battery_energy_after_kwh` at hour 23 equals
`initial_energy_kwh` — the starting charge cannot be spent as free energy.

### Status codes

| Code | When |
|---|---|
| `200` | Success. Returned even when a provider fails, via the fallback path |
| `400` | Malformed JSON, or structurally invalid: wrong hour count, duplicate hours, negative values, `initial_energy_kwh > capacity_kwh`, missing `scenario_id` |
| `500` | Unrecoverable internal error. No stack trace or credential is ever exposed |

---

## Worked example

```bash
curl -s -X POST "$BASE/optimize-energy" \
  -H 'Content-Type: application/json' \
  -d @testdata/example_request.json
```

Notes:

```
[0] "Facilities will wash the rooftop solar panels from noon until 2 PM.
     During cleaning, usable solar should be treated as roughly 25% of the forecast."
[1] "The sports office moved next month's registration deadline."
```

Interpretation:

```json
[
  { "note_index": 0, "applies": true, "directive_type": "solar_reduction",
    "structured_adjustment": { "hours": [12, 13], "factor": 0.25 },
    "explanation": "Solar availability is reduced during panel cleaning." },
  { "note_index": 1, "applies": false, "directive_type": "no_op",
    "structured_adjustment": null,
    "explanation": "This note does not affect today's energy schedule." }
]
```

Note 0 maps `"noon until 2 PM"` to `[12, 13]` — the end hour is **excluded** — and
`"roughly 25% of the forecast"` to `factor 0.25`, the fraction that remains.
Note 1 mentions a date but changes nothing about today's electricity, so it is a
`no_op` with `applies: false`.

For the same scenario the service returns `total_cost_bdt: 38365.00`, matching the
organiser's reference optimum exactly.

---

## Interpretation conventions

These determine the numbers in `structured_adjustment`.

**Time** — start inclusive, end exclusive.

| Wording | `hours` |
|---|---|
| "from noon until 2 PM" | `[12, 13]` |
| "between 11 AM and 2 PM" | `[11, 12, 13]` |
| "from 6 PM until 9 PM" | `[18, 19, 20]` |
| "from 8 PM to midnight" | `[20, 21, 22, 23]` |
| "at 3 PM" / "during the 3 PM hour" | `[15]` |
| "for three hours starting at 10 AM" | `[10, 11, 12]` |
| "from 11 PM to 2 AM" | `[0, 1, 23]` |

**Solar** — `factor` is always the fraction that **remains**.

| Wording | `factor` |
|---|---|
| "drops to about 20%" | `0.2` |
| "an 80% reduction" | `0.2` |
| "reduced by 60%" | `0.4` |
| "about half the forecast" | `0.5` |
| "one-fifth of normal output" | `0.2` |
| "knock 20% off" | `0.8` |

**Reserves** — percentages resolve against that request's `capacity_kwh`.

| Wording | With `capacity_kwh: 200` |
|---|---|
| "at least 90 kWh" | `90` |
| "50% of battery capacity" | `100` |
| "a quarter of the pack" | `50` |

**Grid caps** — kW and kWh coincide over a one-hour interval, so
`"130 kW"` becomes `130`. A cap stated as a total across a window is divided
evenly across that window's hours.

**`no_op`** covers notes about another day ("tomorrow", "next week"),
non-energy campus admin, hypotheticals ("if the weather worsens"), and anything
the model cannot represent — changes to demand, tariff, battery parameters, grid
export, or a diesel generator.
