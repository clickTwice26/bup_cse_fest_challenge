#!/usr/bin/env python3
"""Multi-note interaction suite.

Hidden scenarios carry 1-3 notes whose directives interact. This checks both
that each note is interpreted correctly AND that the single returned schedule
honours every directive simultaneously.
"""
import json, os, sys, urllib.request

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BASE = sys.argv[1] if len(sys.argv) > 1 else "http://127.0.0.1:8099"
TOL = 0.01
CAP = 240.0
BAT = {"capacity_kwh": CAP, "initial_energy_kwh": 120, "minimum_energy_kwh": 40,
       "max_charge_kwh_per_hour": 60, "max_discharge_kwh_per_hour": 60}
HOURS = [{"hour": h, "demand_kwh": 90 + (h % 6) * 18,
          "solar_kwh": max(0, 140 - abs(h - 12) * 22),
          "tariff_bdt_per_kwh": 13 if 18 <= h <= 21 else (5 if h < 6 else 9)}
         for h in range(24)]

CASES = [
  {"name": "solar reduction + distractor",
   "notes": ["Panel washing from 11 AM until 1 PM leaves about a quarter of normal output.",
             "The cafeteria menu changes tomorrow."],
   "want": [("solar_reduction", [11,12], {"factor": 0.25}), ("no_op", None, None)]},
  {"name": "reserve + grid cap overlapping",
   "notes": ["Keep at least 50% of battery capacity from 6 PM until 10 PM.",
             "Grid import must not exceed 170 kWh from 7 PM until 9 PM."],
   "want": [("minimum_battery_reserve", [18,19,20,21], {"minimum_energy_kwh": 120}),
            ("max_grid_window", [19,20], {"max_grid_kwh": 170})]},
  {"name": "no_charge + no_discharge + distractor",
   "notes": ["The charger is isolated from 10 AM until 1 PM.",
             "Relay testing means no battery discharge from 6 PM until 8 PM.",
             "Library hours extend next week."],
   "want": [("no_charge_window", [10,11,12], {}), ("no_discharge_window", [18,19], {}),
            ("no_op", None, None)]},
  {"name": "three real directives",
   "notes": ["Expect a 60% reduction in solar between 10 AM and 1 PM for inverter work.",
             "Hold at least 100 kWh in the battery from 7 PM until 10 PM.",
             "Cap grid import at 200 kWh per hour from 6 PM to 9 PM."],
   "want": [("solar_reduction", [10,11,12], {"factor": 0.4}),
            ("minimum_battery_reserve", [19,20,21], {"minimum_energy_kwh": 100}),
            ("max_grid_window", [18,19,20], {"max_grid_kwh": 200})]},
  {"name": "two notes same type, overlapping windows",
   "notes": ["Keep at least 70 kWh in the battery from 5 PM until 9 PM.",
             "Keep at least 130 kWh in the battery from 7 PM until 11 PM."],
   "want": [("minimum_battery_reserve", [17,18,19,20], {"minimum_energy_kwh": 70}),
            ("minimum_battery_reserve", [19,20,21,22], {"minimum_energy_kwh": 130})]},
  {"name": "all three distractors",
   "notes": ["Tomorrow the charger goes offline from 2 AM to 5 AM.",
             "Evening demand will be 10% higher today.",
             "A seminar room booking moved to next week."],
   "want": [("no_op", None, None), ("no_op", None, None), ("no_op", None, None)]},
]


def replay(r, notes):
    hrs = {h["hour"]: h for h in HOURS}
    eff = {h: hrs[h]["solar_kwh"] for h in range(24)}
    res = {h: BAT["minimum_energy_kwh"] for h in range(24)}
    nc, nd, gc = set(), set(), {}
    for e in r["directive_interpretation"]:
        a = e.get("structured_adjustment")
        if not e.get("applies") or not a: continue
        t = e["directive_type"]
        for h in a.get("hours", []):
            if t == "solar_reduction": eff[h] = hrs[h]["solar_kwh"] * a["factor"]
            elif t == "minimum_battery_reserve": res[h] = max(res[h], a["minimum_energy_kwh"])
            elif t == "no_charge_window": nc.add(h)
            elif t == "no_discharge_window": nd.add(h)
            elif t == "max_grid_window": gc[h] = min(gc.get(h, 1e18), a["max_grid_kwh"])
    E, errs = BAT["initial_energy_kwh"], []
    for p in sorted(r["hourly_plan"], key=lambda x: x["hour"]):
        h, g, s, act, m = p["hour"], p["grid_kwh"], p["solar_used_kwh"], p["battery_action"], p["battery_kwh"]
        chg = m if act == "charge" else 0; dis = m if act == "discharge" else 0
        if abs(g + s + dis - (hrs[h]["demand_kwh"] + chg)) > TOL: errs.append(f"h{h} balance")
        if s > eff[h] + TOL: errs.append(f"h{h} solar>{eff[h]:.1f}")
        if h in nc and chg > TOL: errs.append(f"h{h} charged in no_charge window")
        if h in nd and dis > TOL: errs.append(f"h{h} discharged in no_discharge window")
        if h in gc and g > gc[h] + TOL: errs.append(f"h{h} grid {g:.1f} > cap {gc[h]}")
        E += chg - dis
        if E < res[h] - TOL: errs.append(f"h{h} battery {E:.1f} < reserve {res[h]}")
        if E > BAT["capacity_kwh"] + TOL: errs.append(f"h{h} over capacity")
    if abs(E - BAT["initial_energy_kwh"]) > TOL: errs.append("end != initial")
    return errs


def main():
    bad = 0
    for c in CASES:
        payload = {"scenario_id": "MULTI", "operator_notes": c["notes"],
                   "hours": HOURS, "battery": BAT}
        req = urllib.request.Request(BASE + "/optimize-energy",
            data=json.dumps(payload).encode(), headers={"Content-Type": "application/json"})
        with urllib.request.urlopen(req, timeout=60) as resp:
            r = json.loads(resp.read())
        problems = []
        di = r["directive_interpretation"]
        if len(di) != len(c["notes"]):
            problems.append(f"{len(di)} entries for {len(c['notes'])} notes")
        else:
            for i, (wt, wh, wv) in enumerate(c["want"]):
                e = di[i]
                if e["note_index"] != i: problems.append(f"note {i}: index {e['note_index']}")
                if e["directive_type"] != wt:
                    problems.append(f"note {i}: {e['directive_type']} want {wt}"); continue
                if wt == "no_op":
                    if e["applies"] or e["structured_adjustment"] is not None:
                        problems.append(f"note {i}: bad no_op shape")
                    continue
                a = e["structured_adjustment"] or {}
                if sorted(a.get("hours", [])) != wh:
                    problems.append(f"note {i}: hours {a.get('hours')} want {wh}")
                for k, v in (wv or {}).items():
                    if abs(float(a.get(k, -1e9)) - v) > 0.51:
                        problems.append(f"note {i}: {k}={a.get(k)} want {v}")
        problems += replay(r, c["notes"])
        if problems:
            bad += 1
            print(f"FAIL  {c['name']}")
            for p in problems[:5]: print(f"        {p}")
        else:
            print(f"ok    {c['name']}  (cost {r['total_cost_bdt']:.0f})")
    print(f"\n{len(CASES)-bad}/{len(CASES)} multi-note scenarios passed")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
