package energy

import (
	"math"
	"sort"

	"gonum.org/v1/gonum/mat"
	"gonum.org/v1/gonum/optimize/convex/lp"
)

// Constraints is the deterministic projection of accepted directives onto the
// math model. Nothing here reads natural language.
type Constraints struct {
	Demand   []float64
	Tariff   []float64
	EffSolar []float64
	Reserve  []float64
	NoCharge map[int]bool
	NoDischg map[int]bool
	GridCap  map[int]float64
	Bat      Battery
}

func NewConstraints(hours []HourEntry, bat Battery, dirs []DirectiveInterpretation) *Constraints {
	cx := &Constraints{
		Demand: make([]float64, NHours), Tariff: make([]float64, NHours),
		EffSolar: make([]float64, NHours), Reserve: make([]float64, NHours),
		NoCharge: map[int]bool{}, NoDischg: map[int]bool{},
		GridCap: map[int]float64{}, Bat: bat,
	}
	for _, h := range hours {
		if h.Hour < 0 || h.Hour >= NHours {
			continue
		}
		cx.Demand[h.Hour] = h.DemandKwh
		cx.Tariff[h.Hour] = h.TariffPerKwh
		cx.EffSolar[h.Hour] = h.SolarKwh
	}
	for i := range cx.Reserve {
		cx.Reserve[i] = bat.MinimumEnergyKwh
	}
	for _, d := range dirs {
		cx.Apply(d)
	}
	return cx
}

func AdjHours(adj map[string]any) []int {
	raw, _ := adj["hours"].([]any)
	out := []int{}
	for _, v := range raw {
		if f, ok := v.(float64); ok && f >= 0 && f < NHours {
			out = append(out, int(f))
		}
	}
	if len(out) == 0 {
		if ints, ok := adj["hours"].([]int); ok {
			for _, v := range ints {
				if v >= 0 && v < NHours {
					out = append(out, v)
				}
			}
		}
	}
	return out
}

func AdjNum(adj map[string]any, key string) (float64, bool) {
	switch v := adj[key].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	}
	return 0, false
}

func (cx *Constraints) Apply(d DirectiveInterpretation) {
	if !d.Applies || d.StructuredAdjustment == nil {
		return
	}
	hrs := AdjHours(d.StructuredAdjustment)
	switch d.DirectiveType {
	case TypeSolarReduction:
		if f, ok := AdjNum(d.StructuredAdjustment, "factor"); ok {
			for _, h := range hrs {
				cx.EffSolar[h] *= f
			}
		}
	case TypeMinBatteryRes:
		if v, ok := AdjNum(d.StructuredAdjustment, "minimum_energy_kwh"); ok {
			for _, h := range hrs {
				if v > cx.Reserve[h] {
					cx.Reserve[h] = v
				}
			}
		}
	case TypeNoChargeWindow:
		for _, h := range hrs {
			cx.NoCharge[h] = true
		}
	case TypeNoDischargeWin:
		for _, h := range hrs {
			cx.NoDischg[h] = true
		}
	case TypeMaxGridWindow:
		if v, ok := AdjNum(d.StructuredAdjustment, "max_grid_kwh"); ok {
			for _, h := range hrs {
				if cur, seen := cx.GridCap[h]; !seen || v < cur {
					cx.GridCap[h] = v
				}
			}
		}
	}
}

type SolveOpts struct {
	EnforceReserve bool
	EnforceNeutral bool
	EnforceCap     bool
	// soft turns the directive grid caps and reserves into penalised soft
	// constraints instead of hard ones. The LP then satisfies them wherever it
	// can and violates them minimally where it cannot, which is strictly better
	// than abandoning the directive outright.
	SoftCap     bool
	SoftReserve bool
}

// bigM must dominate any real tariff cost so the LP only ever pays it as a
// last resort, but stay small enough to avoid numerical trouble.
const bigM = 1e6

// SolveLP returns net battery flow per hour (charge positive), or ok=false.
// Layout: g[h]=h, s[h]=24+h, c[h]=48+h, d[h]=72+h.
func (cx *Constraints) SolveLP(o SolveOpts) ([]float64, bool) {
	capHours := make([]int, 0, len(cx.GridCap))
	for h := range cx.GridCap {
		capHours = append(capHours, h)
	}
	sort.Ints(capHours)

	nExtra := 0
	capSlack := map[int]int{} // hour -> variable index
	resSlack := map[int]int{}
	if o.EnforceCap && o.SoftCap {
		for _, h := range capHours {
			capSlack[h] = 96 + nExtra
			nExtra++
		}
	}
	if o.EnforceReserve && o.SoftReserve {
		for h := 0; h < NHours; h++ {
			resSlack[h] = 96 + nExtra
			nExtra++
		}
	}

	b := newLP(96 + nExtra)
	E0, cap := cx.Bat.InitialEnergyKwh, cx.Bat.CapacityKwh

	for h := 0; h < NHours; h++ {
		b.add(map[int]float64{h: 1, 24 + h: 1, 72 + h: 1, 48 + h: -1}, eq, cx.Demand[h])
		b.setUB(24+h, cx.EffSolar[h])
		if cx.NoCharge[h] {
			b.setUB(48+h, 0)
		} else {
			b.setUB(48+h, cx.Bat.MaxChargeKwhPerHour)
		}
		if cx.NoDischg[h] {
			b.setUB(72+h, 0)
		} else {
			b.setUB(72+h, cx.Bat.MaxDischargeKwhPerHour)
		}
		if o.EnforceCap {
			if v, ok := cx.GridCap[h]; ok {
				if si, soft := capSlack[h]; soft {
					b.add(map[int]float64{h: 1, si: -1}, le, v) // g[h] - slack <= cap
				} else {
					b.setUB(h, v)
				}
			}
		}
		up := map[int]float64{}
		lo := map[int]float64{}
		for k := 0; k <= h; k++ {
			up[48+k] = 1
			up[72+k] = -1
			lo[48+k] = 1
			lo[72+k] = -1
		}
		b.add(up, le, cap-E0)
		res := cx.Bat.MinimumEnergyKwh
		if o.EnforceReserve {
			res = cx.Reserve[h]
		}
		if si, soft := resSlack[h]; soft {
			lo[si] = 1 // sum(net) + slack >= reserve - E0
		}
		b.add(lo, ge, res-E0)
	}
	if o.EnforceNeutral {
		neu := map[int]float64{}
		for k := 0; k < NHours; k++ {
			neu[48+k] = 1
			neu[72+k] = -1
		}
		b.add(neu, eq, 0)
	}

	Adata, rhs, total, nRow := b.compile()
	c := make([]float64, total)
	for h := 0; h < NHours; h++ {
		c[h] = cx.Tariff[h]
	}
	for _, si := range capSlack {
		c[si] = bigM
	}
	for _, si := range resSlack {
		c[si] = bigM
	}
	A := mat.NewDense(nRow, total, Adata)
	_, x, err := lp.Simplex(c, A, rhs, 1e-10, nil)
	if err != nil || x == nil {
		return nil, false
	}
	net := make([]float64, NHours)
	for h := 0; h < NHours; h++ {
		net[h] = x[48+h] - x[72+h]
	}
	return net, true
}

func Round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

// BuildPlan turns net battery flow into the published schedule.
// Solar is always taken to the maximum (grid price >= 0 makes that weakly
// optimal and weakly more feasible), so the balance holds exactly by
// construction. Totals are computed from the ROUNDED published values so they
// can never disagree with hourly_plan.
func (cx *Constraints) BuildPlan(net []float64) ([]HourPlan, float64, float64, float64) {
	b := cx.Bat
	n := make([]float64, NHours)
	for h := range n {
		n[h] = Round(net[h], 6)
	}
	resid := 0.0
	for _, v := range n {
		resid += v
	}
	if math.Abs(resid) > 1e-9 { // force exact end-of-day neutrality
		idx := make([]int, NHours)
		for i := range idx {
			idx[i] = i
		}
		sort.Slice(idx, func(a, c int) bool { return math.Abs(n[idx[a]]) > math.Abs(n[idx[c]]) })
		limit := math.Max(b.MaxChargeKwhPerHour, b.MaxDischargeKwhPerHour)
		for _, i := range idx {
			if math.Abs(n[i]-resid) <= limit+1e-9 {
				n[i] -= resid
				break
			}
		}
	}

	plan := make([]HourPlan, NHours)
	E := b.InitialEnergyKwh
	totalGrid, totalCost, peak := 0.0, 0.0, 0.0
	for h := 0; h < NHours; h++ {
		nt := Round(n[h], 4)
		s := math.Min(cx.EffSolar[h], math.Max(0, cx.Demand[h]+nt))
		s = Round(math.Max(0, s), 4)
		g := Round(cx.Demand[h]+nt-s, 4)
		if g < 0 {
			s = Round(s+g, 4)
			g = 0
		}
		E = Round(E+nt, 4)
		action, mag := "idle", 0.0
		if nt > 1e-9 {
			action, mag = "charge", nt
		} else if nt < -1e-9 {
			action, mag = "discharge", -nt
		}
		plan[h] = HourPlan{Hour: h, GridKwh: g, SolarUsedKwh: s,
			BatteryAction: action, BatteryKwh: Round(mag, 4), BatteryEnergyAfterKwh: E}
		totalGrid += g
		totalCost += g * cx.Tariff[h]
		if g > peak {
			peak = g
		}
	}
	return plan, Round(totalGrid, 4), Round(totalCost, 4), Round(peak, 4)
}
