"""Independent replay validator - mirrors exactly what the judge re-checks.

Never ship a plan that fails this. On failure the caller walks the relaxation
ladder; directive_interpretation is NEVER edited (it is scored separately).
"""
from __future__ import annotations

import math

TOL = 0.01


def validate(plan: dict, cx) -> list[str]:
    errs: list[str] = []
    rows = plan["hourly_plan"]
    b = cx.battery

    if len(rows) != 24 or sorted(r["hour"] for r in rows) != list(range(24)):
        errs.append("hourly_plan must contain exactly 24 unique hours 0-23")
        return errs

    E = float(b.initial_energy_kwh)
    for r in rows:
        h = r["hour"]
        g, s, mag = r["grid_kwh"], r["solar_used_kwh"], r["battery_kwh"]
        act = r["battery_action"]

        for name, v in (("grid_kwh", g), ("solar_used_kwh", s), ("battery_kwh", mag)):
            if not math.isfinite(v) or v < -TOL:
                errs.append(f"h{h}: {name} must be finite and non-negative")
        if act not in ("charge", "discharge", "idle"):
            errs.append(f"h{h}: bad battery_action {act!r}")
            continue
        if act == "idle" and abs(mag) > TOL:
            errs.append(f"h{h}: battery_kwh must be 0 when idle")

        chg = mag if act == "charge" else 0.0
        dis = mag if act == "discharge" else 0.0

        if abs(g + s + dis - (cx.demand[h] + chg)) > TOL:
            errs.append(f"h{h}: energy balance violated")
        if s > cx.eff_solar[h] + TOL:
            errs.append(f"h{h}: solar_used {s} exceeds effective solar {cx.eff_solar[h]}")
        if chg > b.max_charge_kwh_per_hour + TOL:
            errs.append(f"h{h}: charge rate exceeded")
        if dis > b.max_discharge_kwh_per_hour + TOL:
            errs.append(f"h{h}: discharge rate exceeded")
        if h in cx.no_charge and chg > TOL:
            errs.append(f"h{h}: no_charge_window violated")
        if h in cx.no_discharge and dis > TOL:
            errs.append(f"h{h}: no_discharge_window violated")
        if h in cx.grid_cap and g > cx.grid_cap[h] + TOL:
            errs.append(f"h{h}: max_grid_window violated ({g} > {cx.grid_cap[h]})")

        E = E + chg - dis
        if abs(E - r["battery_energy_after_kwh"]) > TOL:
            errs.append(f"h{h}: battery_energy_after_kwh inconsistent")
        if E < cx.reserve[h] - TOL:
            errs.append(f"h{h}: battery below reserve ({E} < {cx.reserve[h]})")
        if E > b.capacity_kwh + TOL:
            errs.append(f"h{h}: battery above capacity")

    if abs(E - b.initial_energy_kwh) > TOL:
        errs.append(f"end-of-day battery {E} != initial {b.initial_energy_kwh}")

    tg = sum(r["grid_kwh"] for r in rows)
    tc = sum(r["grid_kwh"] * cx.tariff[r["hour"]] for r in rows)
    pk = max(r["grid_kwh"] for r in rows)
    if abs(tg - plan["total_grid_kwh"]) > TOL:
        errs.append("total_grid_kwh disagrees with hourly_plan")
    if abs(tc - plan["total_cost_bdt"]) > TOL:
        errs.append("total_cost_bdt disagrees with hourly_plan")
    if abs(pk - plan["peak_grid_kwh"]) > TOL:
        errs.append("peak_grid_kwh disagrees with hourly_plan")
    return errs
