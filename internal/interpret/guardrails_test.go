package interpret

import (
	"reflect"
	"testing"

	"gridwise/internal/energy"
)

// Windows are start-inclusive and end-exclusive. These are the cases that
// decide the interpretation score, so they are pinned here.
func TestExpandWindow(t *testing.T) {
	for _, tc := range []struct {
		name       string
		start, end int
		want       []int
	}{
		{"noon until 2 PM", 12, 14, []int{12, 13}},
		{"between 11 AM and 2 PM", 11, 14, []int{11, 12, 13}},
		{"6 PM until 9 PM", 18, 21, []int{18, 19, 20}},
		{"9 PM until midnight", 21, 24, []int{21, 22, 23}},
		{"single hour", 15, 16, []int{15}},
		{"degenerate is one hour, never empty", 15, 15, []int{15}},
		{"wraps past midnight", 23, 2, []int{0, 1, 23}},
		{"end given as 0 means midnight", 20, 0, []int{20, 21, 22, 23}},
	} {
		if got := ExpandWindow(tc.start, tc.end); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: ExpandWindow(%d,%d) = %v, want %v", tc.name, tc.start, tc.end, got, tc.want)
		}
	}
}

// factor is the fraction of solar REMAINING. Inverting this is the single most
// expensive mistake available in this problem.
func TestResolveSolarFactor(t *testing.T) {
	bat := energy.Battery{CapacityKwh: 200, MinimumEnergyKwh: 40}
	for _, tc := range []struct {
		name      string
		semantics string
		number    float64
		want      float64
		ok        bool
	}{
		{"80% reduction leaves 0.2", "solar_reduction_percent", 80, 0.2, true},
		{"drops to 20% leaves 0.2", "solar_remaining_percent", 20, 0.2, true},
		{"about half", "solar_remaining_percent", 50, 0.5, true},
		{"reduced by 60% leaves 0.4", "solar_reduction_percent", 60, 0.4, true},
		{"completely offline", "solar_remaining_percent", 0, 0.0, true},
		{"an increase is not a reduction", "solar_reduction_percent", -20, 0, false},
	} {
		adj, ok := ResolveAdjustment(energy.TypeSolarReduction, tc.semantics, tc.number, true, []int{11}, bat)
		if ok != tc.ok {
			t.Errorf("%s: ok = %v, want %v", tc.name, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		if got := adj["factor"].(float64); got != tc.want {
			t.Errorf("%s: factor = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestResolveReserve(t *testing.T) {
	bat := energy.Battery{CapacityKwh: 200, MinimumEnergyKwh: 40}
	for _, tc := range []struct {
		name      string
		semantics string
		number    float64
		want      float64
	}{
		{"absolute kWh", "reserve_absolute_kwh", 90, 90},
		{"50% of capacity", "reserve_percent_of_capacity", 50, 100},
		{"above the base minimum", "reserve_above_base_kwh", 50, 90},
		{"clamped to capacity", "reserve_absolute_kwh", 5000, 200},
	} {
		adj, ok := ResolveAdjustment(energy.TypeMinBatteryRes, tc.semantics, tc.number, true, []int{18}, bat)
		if !ok {
			t.Fatalf("%s: unexpectedly rejected", tc.name)
		}
		if got := adj["minimum_energy_kwh"].(float64); got != tc.want {
			t.Errorf("%s: reserve = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// An unsupported or hallucinated directive type must degrade to no_op and never
// invent a constraint.
func TestCoerceTypeNeverInvents(t *testing.T) {
	for in, want := range map[string]string{
		"solar_reduction": energy.TypeSolarReduction,
		"no_charge":       energy.TypeNoChargeWindow,
		"GRID_CAP":        energy.TypeMaxGridWindow,
		"DELETE_ALL":      energy.TypeNoOp,
		"":                energy.TypeNoOp,
		"battery_explode": energy.TypeNoOp,
	} {
		if got := CoerceType(in); got != want {
			t.Errorf("CoerceType(%q) = %q, want %q", in, got, want)
		}
	}
}

// A missing value must downgrade rather than be guessed.
func TestMissingValueDowngrades(t *testing.T) {
	bat := energy.Battery{CapacityKwh: 200, MinimumEnergyKwh: 40}
	for _, dt := range []string{energy.TypeSolarReduction, energy.TypeMinBatteryRes, energy.TypeMaxGridWindow} {
		if _, ok := ResolveAdjustment(dt, "none", 0, false, []int{18}, bat); ok {
			t.Errorf("%s: accepted a missing value", dt)
		}
	}
}
