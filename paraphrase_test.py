#!/usr/bin/env python3
"""Paraphrase robustness probe - the closest proxy we have to the hidden set.

Each note is reworded away from the public pack's phrasing. Ground truth is
hand-written. Battery capacity is fixed at 200 kWh so percent-of-capacity
reserves resolve to round numbers.
"""
import json, sys, urllib.request, concurrent.futures as cf

BASE = sys.argv[1] if len(sys.argv) > 1 else "http://127.0.0.1:8099"
CAP = 200.0
BATTERY = {"capacity_kwh": CAP, "initial_energy_kwh": 100, "minimum_energy_kwh": 30,
           "max_charge_kwh_per_hour": 50, "max_discharge_kwh_per_hour": 50}
HOURS = [{"hour": h, "demand_kwh": 100 + (h % 5) * 10,
          "solar_kwh": max(0, 120 - abs(h - 12) * 20),
          "tariff_bdt_per_kwh": 12 if 18 <= h <= 21 else (6 if h < 6 else 9)}
         for h in range(24)]


def probe(case):
    payload = {"scenario_id": "PARA", "operator_notes": [case["note"]],
               "hours": HOURS, "battery": BATTERY}
    req = urllib.request.Request(BASE + "/optimize-energy",
        data=json.dumps(payload).encode(), headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=40) as r:
        e = json.loads(r.read())["directive_interpretation"][0]
    problems = []
    if e["directive_type"] != case["type"]:
        problems.append(f"type={e['directive_type']} want {case['type']}")
    if case["type"] == "no_op":
        if e["applies"] or e["structured_adjustment"] is not None:
            problems.append("no_op must have applies=false and null adjustment")
        return case, problems
    if not e["applies"]:
        problems.append("applies=false on a real directive")
    a = e.get("structured_adjustment") or {}
    if sorted(a.get("hours", [])) != case["hours"]:
        problems.append(f"hours={a.get('hours')} want {case['hours']}")
    for k in ("factor", "minimum_energy_kwh", "max_grid_kwh"):
        if k in case and abs(float(a.get(k, -1e9)) - case[k]) > 0.51:
            problems.append(f"{k}={a.get(k)} want {case[k]}")
    return case, problems


def main():
    cases = json.load(open("paraphrases.json"))
    with cf.ThreadPoolExecutor(max_workers=6) as ex:
        results = list(ex.map(probe, cases))
    bad = 0
    by_type = {}
    for case, problems in results:
        t = case["type"]
        by_type.setdefault(t, [0, 0])
        by_type[t][1] += 1
        if problems:
            bad += 1
            print(f"FAIL  {case['note'][:70]}")
            for p in problems:
                print(f"        {p}")
        else:
            by_type[t][0] += 1
    print(f"\n{len(cases)-bad}/{len(cases)} paraphrases correct")
    for t, (ok, n) in sorted(by_type.items()):
        print(f"   {t:<26} {ok}/{n}")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
