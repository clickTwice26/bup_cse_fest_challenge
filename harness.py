#!/usr/bin/env python3
"""End-to-end judge harness: runs the 10 public cases against a live service.

Checks, per case:
  1. directive_interpretation matches the reference on applies / directive_type /
     hours / numerics within 0.01 (explanation text is NOT compared - the spec
     says free text is not matched byte-for-byte)
  2. the returned hourly_plan passes a full independent replay
  3. total_cost_bdt equals the reference optimal cost within 0.01
"""
import json, sys, time, urllib.request

BASE = sys.argv[1] if len(sys.argv) > 1 else "http://127.0.0.1:8099"
TOL = 0.01
CASES = json.load(open("BUP_CSE_FEST_2026_Preli_Public_Sample_Cases.json"))["cases"]


def post(payload):
    req = urllib.request.Request(BASE + "/optimize-energy",
        data=json.dumps(payload).encode(), headers={"Content-Type": "application/json"})
    t0 = time.time()
    with urllib.request.urlopen(req, timeout=40) as r:
        return json.loads(r.read()), time.time() - t0


def interp_matches(got, want):
    if len(got) != len(want):
        return f"entry count {len(got)} != {len(want)}"
    for i, (g, w) in enumerate(zip(got, want)):
        if g.get("note_index") != i:
            return f"note {i}: note_index={g.get('note_index')}"
        if g.get("applies") != w["applies"]:
            return f"note {i}: applies {g.get('applies')} != {w['applies']}"
        if g.get("directive_type") != w["directive_type"]:
            return f"note {i}: type {g.get('directive_type')} != {w['directive_type']}"
        ga, wa = g.get("structured_adjustment"), w["structured_adjustment"]
        if (ga is None) != (wa is None):
            return f"note {i}: adjustment null mismatch"
        if wa is None:
            continue
        if sorted(ga.get("hours", [])) != sorted(wa.get("hours", [])):
            return f"note {i}: hours {ga.get('hours')} != {wa.get('hours')}"
        for k in ("factor", "minimum_energy_kwh", "max_grid_kwh"):
            if k in wa and abs(float(ga.get(k, 1e9)) - float(wa[k])) > TOL:
                return f"note {i}: {k} {ga.get(k)} != {wa[k]}"
    return None


def replay(plan, inp, interp):
    hrs = {h["hour"]: h for h in inp["hours"]}
    b = inp["battery"]
    eff = {h: hrs[h]["solar_kwh"] for h in range(24)}
    res = {h: b["minimum_energy_kwh"] for h in range(24)}
    nc, nd, gc = set(), set(), {}
    for e in interp:
        a = e.get("structured_adjustment")
        if not e.get("applies") or not a:
            continue
        t = e["directive_type"]
        for h in a.get("hours", []):
            if t == "solar_reduction":   eff[h] = hrs[h]["solar_kwh"] * a["factor"]
            elif t == "minimum_battery_reserve": res[h] = max(res[h], a["minimum_energy_kwh"])
            elif t == "no_charge_window":    nc.add(h)
            elif t == "no_discharge_window": nd.add(h)
            elif t == "max_grid_window":     gc[h] = min(gc.get(h, 1e18), a["max_grid_kwh"])
    if len(plan) != 24 or sorted(p["hour"] for p in plan) != list(range(24)):
        return ["plan must contain 24 unique hours"]
    E, errs = b["initial_energy_kwh"], []
    tg = tc = pk = 0
    for p in sorted(plan, key=lambda x: x["hour"]):
        h, g, s, act, mag = p["hour"], p["grid_kwh"], p["solar_used_kwh"], p["battery_action"], p["battery_kwh"]
        chg = mag if act == "charge" else 0
        dis = mag if act == "discharge" else 0
        if act == "idle" and abs(mag) > TOL: errs.append(f"h{h} idle nonzero")
        if abs(g + s + dis - (hrs[h]["demand_kwh"] + chg)) > TOL: errs.append(f"h{h} balance")
        if s > eff[h] + TOL: errs.append(f"h{h} solar>{eff[h]:.2f}")
        if chg > b["max_charge_kwh_per_hour"] + TOL: errs.append(f"h{h} chg rate")
        if dis > b["max_discharge_kwh_per_hour"] + TOL: errs.append(f"h{h} dis rate")
        if h in nc and chg > TOL: errs.append(f"h{h} no_charge")
        if h in nd and dis > TOL: errs.append(f"h{h} no_discharge")
        if h in gc and g > gc[h] + TOL: errs.append(f"h{h} grid cap")
        E += chg - dis
        if abs(E - p["battery_energy_after_kwh"]) > TOL: errs.append(f"h{h} E mismatch")
        if E < res[h] - TOL: errs.append(f"h{h} below reserve")
        if E > b["capacity_kwh"] + TOL: errs.append(f"h{h} over capacity")
        tg += g; tc += g * hrs[h]["tariff_bdt_per_kwh"]; pk = max(pk, g)
    if abs(E - b["initial_energy_kwh"]) > TOL: errs.append("end != initial battery")
    return errs, tg, tc, pk


def main():
    interp_ok = plan_ok = cost_ok = 0
    lat = []
    for c in CASES:
        try:
            r, dt = post(c["input"])
        except Exception as e:
            print(f"{c['id']:<10} REQUEST FAILED: {e}"); continue
        lat.append(dt)
        exp = c["expected_output"]
        im = interp_matches(r.get("directive_interpretation", []), exp["directive_interpretation"])
        errs, tg, tc, pk = replay(r["hourly_plan"], c["input"], r["directive_interpretation"])
        tot_ok = (abs(tg - r["total_grid_kwh"]) <= TOL and abs(tc - r["total_cost_bdt"]) <= TOL
                  and abs(pk - r["peak_grid_kwh"]) <= TOL)
        cost_delta = r["total_cost_bdt"] - exp["total_cost_bdt"]
        if im is None: interp_ok += 1
        if not errs and tot_ok: plan_ok += 1
        if abs(cost_delta) <= TOL: cost_ok += 1
        print(f"{c['id']:<10} interp={'OK ' if im is None else 'XX'} "
              f"plan={'OK ' if not errs and tot_ok else 'XX'} "
              f"cost={r['total_cost_bdt']:>9.2f} ref={exp['total_cost_bdt']:>8} "
              f"d={cost_delta:+8.2f}  {dt*1000:5.0f}ms"
              + (f"\n           interp: {im}" if im else "")
              + (f"\n           plan: {errs[:3]}" if errs else ""))
    n = len(CASES)
    lat.sort()
    p95 = lat[int(len(lat) * 0.95) - 1] if lat else 0
    print(f"\ninterpretation {interp_ok}/{n}   plan valid {plan_ok}/{n}   optimal cost {cost_ok}/{n}")
    print(f"latency p50={lat[len(lat)//2]*1000:.0f}ms p95={p95*1000:.0f}ms" if lat else "")
    return 0 if (interp_ok == n and plan_ok == n and cost_ok == n) else 1


if __name__ == "__main__":
    sys.exit(main())
