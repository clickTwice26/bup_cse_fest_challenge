"""Deterministic guardrail + repair layer.

Every function here is TOTAL: it returns a value for any input and never raises.
LLM output is untrusted structured data until it passes through this module.
The model names meaning; this module does all arithmetic and enumeration.
"""
from __future__ import annotations

import math
import re
import unicodedata

ALLOWED_TYPES = {
    "solar_reduction", "minimum_battery_reserve", "no_charge_window",
    "no_discharge_window", "max_grid_window", "no_op",
}

TYPE_ALIASES = {
    "solar": "solar_reduction", "solar_reduce": "solar_reduction",
    "pv_reduction": "solar_reduction", "reduce_solar": "solar_reduction",
    "solarreduction": "solar_reduction", "solar_reduction_window": "solar_reduction",
    "min_battery_reserve": "minimum_battery_reserve",
    "battery_reserve": "minimum_battery_reserve", "reserve": "minimum_battery_reserve",
    "minimum_reserve": "minimum_battery_reserve", "min_reserve": "minimum_battery_reserve",
    "no_charge": "no_charge_window", "charge_block": "no_charge_window",
    "no_charging": "no_charge_window", "nocharge": "no_charge_window",
    "no_discharge": "no_discharge_window", "discharge_block": "no_discharge_window",
    "no_discharging": "no_discharge_window", "nodischarge": "no_discharge_window",
    "grid_cap": "max_grid_window", "max_grid": "max_grid_window",
    "grid_limit": "max_grid_window", "maxgrid": "max_grid_window",
    "none": "no_op", "null": "no_op", "na": "no_op", "irrelevant": "no_op",
    "not_applicable": "no_op", "noop": "no_op",
}

VALUE_SEMANTICS = {
    "solar_remaining_percent", "solar_reduction_percent", "reserve_absolute_kwh",
    "reserve_percent_of_capacity", "reserve_above_base_kwh",
    "grid_cap_kwh_per_hour", "grid_cap_total_over_window_kwh", "none",
}

_DIGIT_MAP = {ord(c): str(i % 10) for i, c in enumerate(
    "০১২৩৪৫৬৭৮৯٠١٢٣٤٥٦٧٨٩")}

DEFAULT_EXPLANATION = {
    "solar_reduction": "Usable solar is reduced during the stated hours.",
    "minimum_battery_reserve": "Battery energy is held at or above the stated reserve.",
    "no_charge_window": "Battery charging is unavailable during the stated hours.",
    "no_discharge_window": "Battery discharging is unavailable during the stated hours.",
    "max_grid_window": "Grid import is capped during the stated hours.",
    "no_op": "This note does not affect today's energy schedule.",
}

_SECRET_RE = re.compile(r"(sk-[A-Za-z0-9_\-]{8,}|Bearer\s+\S+|gsk_[A-Za-z0-9]{8,}|Traceback)")


# ---------------------------------------------------------------- coercion

def normalize_note(s) -> str:
    if not isinstance(s, str):
        s = "" if s is None else str(s)
    s = unicodedata.normalize("NFKC", s).translate(_DIGIT_MAP)
    s = "".join(ch for ch in s if ch == "\n" or ord(ch) >= 32)
    return re.sub(r"\s+", " ", s).strip()[:1200]


def coerce_type(v) -> str:
    k = re.sub(r"[\s\-]+", "_", str(v or "").strip().lower())
    if k in ALLOWED_TYPES:
        return k
    return TYPE_ALIASES.get(k, "no_op")     # unknown type -> no_op, never invented


def coerce_num(v):
    if isinstance(v, bool):
        return None
    if isinstance(v, (int, float)):
        return float(v) if math.isfinite(v) else None
    m = re.search(r"-?\d+(?:\.\d+)?", str(v or "").replace(",", ""))
    return float(m.group()) if m else None


def coerce_bool(v) -> bool:
    if isinstance(v, bool):
        return v
    return str(v).strip().lower() in ("true", "1", "yes", "y")


def _clamp(v, lo, hi):
    return max(lo, min(hi, v))


# ------------------------------------------------------------------- hours

def expand_window(start, end):
    """Start-inclusive, end-exclusive. Handles midnight end and midnight wrap."""
    s, e = coerce_num(start), coerce_num(end)
    if s is None or e is None:
        return None
    s, e = int(s) % 24, int(e)
    if e in (0, 24):
        e = 24
    elif not 1 <= e <= 24:
        e = (e % 24) or 24
    if e > s:
        return list(range(s, e))
    if e == s:
        return [s]                                    # never return []
    return sorted(set(range(s, 24)) | set(range(0, e)))   # wraps past midnight


def clean_hours(xs) -> list[int]:
    out = set()
    for x in (xs or []):
        n = coerce_num(x.split(":")[0] if isinstance(x, str) else x)
        if n is not None and float(n).is_integer() and 0 <= n <= 23:
            out.add(int(n))
    return sorted(out)


def canon_hours(raw_hours, start, end, non_contiguous):
    """Dual-channel. The window expansion is authoritative; disagreement is signal."""
    win = expand_window(start, end)
    lst = clean_hours(raw_hours)
    if non_contiguous and lst:
        return lst, False
    if win is not None and lst:
        return win, set(win) != set(lst)
    return (win or lst or []), False


# ------------------------------------------------------- value resolution

DOWNGRADE = object()


def resolve_adjustment(dtype, semantics, number, hours, battery):
    """All arithmetic lives here. Returns dict, None (no_op), or DOWNGRADE."""
    if dtype == "no_op":
        return None
    if dtype in ("no_charge_window", "no_discharge_window"):
        return {"hours": hours}
    if number is None:
        return DOWNGRADE                     # never invent a value

    cap = float(battery.capacity_kwh)
    base = float(battery.minimum_energy_kwh)

    if dtype == "solar_reduction":
        f = (1.0 - number / 100.0) if semantics == "solar_reduction_percent" else number / 100.0
        f = _clamp(f, 0.0, 1.0)
        if f >= 0.999:
            return DOWNGRADE                 # an increase/no-change is not a reduction
        return {"hours": hours, "factor": round(f, 4)}

    if dtype == "minimum_battery_reserve":
        if semantics == "reserve_percent_of_capacity":
            v = number / 100.0 * cap
        elif semantics == "reserve_above_base_kwh":
            v = base + number
        else:
            v = number
        return {"hours": hours, "minimum_energy_kwh": round(_clamp(v, 0.0, cap), 4)}

    if dtype == "max_grid_window":
        v = number / max(1, len(hours)) if semantics == "grid_cap_total_over_window_kwh" else number
        return {"hours": hours, "max_grid_kwh": round(max(0.0, v), 4)}

    return DOWNGRADE


# ------------------------------------------------------------- assembly

def no_op_entry(index: int, explanation: str | None = None) -> dict:
    return {
        "note_index": index, "applies": False, "directive_type": "no_op",
        "structured_adjustment": None,
        "explanation": explanation or DEFAULT_EXPLANATION["no_op"],
    }


def clean_explanation(text, dtype) -> str:
    s = re.sub(r"\s+", " ", str(text or "")).strip()
    s = _SECRET_RE.sub("[redacted]", s)
    if not s:
        s = DEFAULT_EXPLANATION.get(dtype, "")
    return s[:200]


def build_entry(index: int, raw: dict | None, battery) -> tuple[dict, list[str]]:
    """Turn one raw LLM object into a spec-shaped directive_interpretation entry.

    note_index is assigned by POSITION, never read from the model, so
    completeness and ordering are structurally guaranteed.
    """
    flags: list[str] = []
    if not isinstance(raw, dict):
        return no_op_entry(index), ["no_object"]

    dtype = coerce_type(raw.get("directive_type"))
    if not coerce_bool(raw.get("applies_today", True)):
        dtype = "no_op"
    if dtype == "no_op":
        return no_op_entry(index, clean_explanation(raw.get("explanation"), "no_op")), flags

    hours, disagreed = canon_hours(
        raw.get("hours"), raw.get("window_start_hour"),
        raw.get("window_end_hour_exclusive"), coerce_bool(raw.get("non_contiguous")))
    if disagreed:
        flags.append("hour_channel_disagreement")
    if not hours:
        return no_op_entry(index), flags + ["empty_hours"]

    semantics = str(raw.get("value_semantics") or "none").strip().lower()
    if semantics not in VALUE_SEMANTICS:
        semantics = "none"
    adj = resolve_adjustment(dtype, semantics, coerce_num(raw.get("value_number")),
                             hours, battery)
    if adj is DOWNGRADE:
        return no_op_entry(index), flags + ["unresolvable_value"]

    return {
        "note_index": index, "applies": True, "directive_type": dtype,
        "structured_adjustment": adj,
        "explanation": clean_explanation(raw.get("explanation"), dtype),
    }, flags
