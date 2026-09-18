package main

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

type sampleCase struct {
	ID    string          `json:"id"`
	Input OptimizeRequest `json:"input"`
	Expected struct {
		DirectiveInterpretation []DirectiveInterpretation `json:"directive_interpretation"`
		TotalCostBdt            float64                   `json:"total_cost_bdt"`
	} `json:"expected_output"`
}

func loadCases(t *testing.T) []sampleCase {
	raw, err := os.ReadFile("BUP_CSE_FEST_2026_Preli_Public_Sample_Cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Cases []sampleCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Cases
}

// Optimizer isolation: feed the REFERENCE interpretations, demand cost parity.
func TestOptimizerMatchesReferenceCost(t *testing.T) {
	for _, c := range loadCases(t) {
		cx := NewConstraints(c.Input.Hours, c.Input.Battery, c.Expected.DirectiveInterpretation)
		net, ok := cx.SolveLP(solveOpts{enforceReserve: true, enforceNeutral: true, enforceCap: true})
		if !ok {
			t.Errorf("%s: LP infeasible", c.ID)
			continue
		}
		plan, tg, tc, pk := cx.BuildPlan(net)
		if errs := Validate(plan, tg, tc, pk, cx); len(errs) > 0 {
			t.Errorf("%s: replay failed: %v", c.ID, errs[:min(3, len(errs))])
			continue
		}
		if d := math.Abs(tc - c.Expected.TotalCostBdt); d > 0.01 {
			t.Errorf("%s: cost %.4f want %.4f (delta %+.4f)", c.ID, tc, c.Expected.TotalCostBdt, tc-c.Expected.TotalCostBdt)
		} else {
			t.Logf("%s  ref=%-9.0f got=%-10.2f delta=%+.4f  PASS", c.ID, c.Expected.TotalCostBdt, tc, tc-c.Expected.TotalCostBdt)
		}
	}
}
