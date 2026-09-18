package energy

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

type sampleCase struct {
	ID       string          `json:"id"`
	Input    OptimizeRequest `json:"input"`
	Expected struct {
		DirectiveInterpretation []DirectiveInterpretation `json:"directive_interpretation"`
		TotalCostBdt            float64                   `json:"total_cost_bdt"`
	} `json:"expected_output"`
}

func loadCases(t *testing.T) []sampleCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "public_cases.json"))
	if err != nil {
		t.Fatalf("read public cases: %v", err)
	}
	var doc struct {
		Cases []sampleCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse public cases: %v", err)
	}
	return doc.Cases
}

// TestOptimizerMatchesReferenceCost isolates the optimiser: it is fed the
// organiser's own reference interpretations, so any cost difference is the
// solver's fault and not the language model's.
func TestOptimizerMatchesReferenceCost(t *testing.T) {
	for _, c := range loadCases(t) {
		c := c
		t.Run(c.ID, func(t *testing.T) {
			cx := NewConstraints(c.Input.Hours, c.Input.Battery, c.Expected.DirectiveInterpretation)
			net, ok := cx.SolveLP(SolveOpts{EnforceReserve: true, EnforceNeutral: true, EnforceCap: true})
			if !ok {
				t.Fatalf("LP reported infeasible")
			}
			plan, tg, tc, pk := cx.BuildPlan(net)
			if errs := Validate(plan, tg, tc, pk, cx); len(errs) > 0 {
				t.Fatalf("replay failed: %v", errs)
			}
			if d := math.Abs(tc - c.Expected.TotalCostBdt); d > 0.01 {
				t.Errorf("cost %.4f, want %.4f (delta %+.4f)", tc, c.Expected.TotalCostBdt, tc-c.Expected.TotalCostBdt)
			}
		})
	}
}
