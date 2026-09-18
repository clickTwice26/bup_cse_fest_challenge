"""Optimizer isolation test: feed the REFERENCE interpretations, check cost parity."""
import json, sys, types
sys.path.insert(0, ".")
from app.optimizer import Constraints, solve_lp, build_plan
from app.replay import validate
from app.schemas import OptimizeRequest

CASES = json.load(open("BUP_CSE_FEST_2026_Preli_Public_Sample_Cases.json"))["cases"]

def run():
    fails = 0
    for c in CASES:
        req = OptimizeRequest(**c["input"])
        exp = c["expected_output"]
        directives = [types.SimpleNamespace(
            directive_type=e["directive_type"],
            structured_adjustment=e["structured_adjustment"]) for e in exp["directive_interpretation"]]
        cx = Constraints(req.hours, req.battery, directives)
        net = solve_lp(cx)
        assert net is not None, f"{c['id']} LP infeasible"
        plan = build_plan(cx, net)
        errs = validate(plan, cx)
        ref = exp["total_cost_bdt"]
        got = plan["total_cost_bdt"]
        delta = got - ref
        ok = not errs and abs(delta) <= 0.01
        if not ok: fails += 1
        print(f"{c['id']}  ref={ref:>8}  got={got:>10.2f}  delta={delta:+.4f}  "
              f"{'PASS' if ok else 'FAIL ' + str(errs[:2])}")
    print(f"\n{len(CASES)-fails}/{len(CASES)} passed")
    return fails

if __name__ == "__main__":
    sys.exit(1 if run() else 0)
