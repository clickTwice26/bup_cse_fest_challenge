package main

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Deterministic guardrail + repair layer. Every function here is TOTAL: it
// returns a value for any input and never panics. LLM output is untrusted
// structured data until it passes through here. The model names meaning; this
// file does all arithmetic and all hour enumeration.

var allowedTypes = map[string]bool{
	TypeSolarReduction: true, TypeMinBatteryRes: true, TypeNoChargeWindow: true,
	TypeNoDischargeWin: true, TypeMaxGridWindow: true, TypeNoOp: true,
}

var typeAliases = map[string]string{
	"solar": TypeSolarReduction, "solar_reduce": TypeSolarReduction,
	"pv_reduction": TypeSolarReduction, "reduce_solar": TypeSolarReduction,
	"solarreduction": TypeSolarReduction, "solar_reduction_window": TypeSolarReduction,
	"min_battery_reserve": TypeMinBatteryRes, "battery_reserve": TypeMinBatteryRes,
	"reserve": TypeMinBatteryRes, "minimum_reserve": TypeMinBatteryRes,
	"min_reserve": TypeMinBatteryRes, "minimum_battery": TypeMinBatteryRes,
	"no_charge": TypeNoChargeWindow, "charge_block": TypeNoChargeWindow,
	"no_charging": TypeNoChargeWindow, "nocharge": TypeNoChargeWindow,
	"no_discharge": TypeNoDischargeWin, "discharge_block": TypeNoDischargeWin,
	"no_discharging": TypeNoDischargeWin, "nodischarge": TypeNoDischargeWin,
	"grid_cap": TypeMaxGridWindow, "max_grid": TypeMaxGridWindow,
	"grid_limit": TypeMaxGridWindow, "maxgrid": TypeMaxGridWindow,
	"none": TypeNoOp, "null": TypeNoOp, "na": TypeNoOp, "irrelevant": TypeNoOp,
	"not_applicable": TypeNoOp, "noop": TypeNoOp, "": TypeNoOp,
}

var validSemantics = map[string]bool{
	"solar_remaining_percent": true, "solar_reduction_percent": true,
	"reserve_absolute_kwh": true, "reserve_percent_of_capacity": true,
	"reserve_above_base_kwh": true, "grid_cap_kwh_per_hour": true,
	"grid_cap_total_over_window_kwh": true, "none": true,
}

var defaultExplanation = map[string]string{
	TypeSolarReduction: "Usable solar is reduced during the stated hours.",
	TypeMinBatteryRes:  "Battery energy is held at or above the stated reserve.",
	TypeNoChargeWindow: "Battery charging is unavailable during the stated hours.",
	TypeNoDischargeWin: "Battery discharging is unavailable during the stated hours.",
	TypeMaxGridWindow:  "Grid import is capped during the stated hours.",
	TypeNoOp:           "This note does not affect today's energy schedule.",
}

var (
	secretRe = regexp.MustCompile(`(?i)(sk-[A-Za-z0-9_\-]{8,}|gsk_[A-Za-z0-9]{8,}|Bearer\s+\S+|Traceback)`)
	wsRe     = regexp.MustCompile(`\s+`)
	numRe    = regexp.MustCompile(`-?\d+(?:\.\d+)?`)
	nonWord  = regexp.MustCompile(`[\s\-]+`)
)

// NormalizeNote folds Bengali/Arabic digits to ASCII and collapses whitespace.
func NormalizeNote(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '০' && r <= '৯': // Bengali 0-9
			b.WriteRune('0' + (r - '০'))
		case r >= '٠' && r <= '٩': // Arabic-Indic 0-9
			b.WriteRune('0' + (r - '٠'))
		case r == '\n' || unicode.IsPrint(r):
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(wsRe.ReplaceAllString(b.String(), " "))
	if len(out) > 1200 {
		out = out[:1200]
	}
	return out
}

// CoerceType maps model output onto a supported type. Unknown -> no_op, never invented.
func CoerceType(v any) string {
	s := strings.ToLower(strings.TrimSpace(toStr(v)))
	s = nonWord.ReplaceAllString(s, "_")
	if allowedTypes[s] {
		return s
	}
	if t, ok := typeAliases[s]; ok {
		return t
	}
	return TypeNoOp
}

func toStr(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	return ""
}

// CoerceNum extracts a finite number from a number or a string like "150 kWh".
func CoerceNum(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0, false
		}
		return x, true
	case int:
		return float64(x), true
	case string:
		m := numRe.FindString(strings.ReplaceAll(x, ",", ""))
		if m == "" {
			return 0, false
		}
		f, err := strconv.ParseFloat(m, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return f, true
	}
	return 0, false
}

func CoerceBool(v any, def bool) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		s := strings.ToLower(strings.TrimSpace(x))
		return s == "true" || s == "1" || s == "yes" || s == "y"
	case nil:
		return def
	}
	return def
}

// ExpandWindow is start-inclusive, end-exclusive. Handles a midnight end and a
// window that wraps past midnight. Never returns an empty slice for valid input.
func ExpandWindow(start, end any) []int {
	sf, ok1 := CoerceNum(start)
	ef, ok2 := CoerceNum(end)
	if !ok1 || !ok2 {
		return nil
	}
	s := ((int(sf) % 24) + 24) % 24
	e := int(ef)
	if e == 0 || e == 24 {
		e = 24
	} else if e < 1 || e > 24 {
		e = ((e % 24) + 24) % 24
		if e == 0 {
			e = 24
		}
	}
	switch {
	case e > s:
		out := make([]int, 0, e-s)
		for h := s; h < e; h++ {
			out = append(out, h)
		}
		return out
	case e == s:
		return []int{s} // degenerate window is one hour, never empty
	default: // wraps past midnight
		set := map[int]bool{}
		for h := s; h < 24; h++ {
			set[h] = true
		}
		for h := 0; h < e; h++ {
			set[h] = true
		}
		return sortedKeys(set)
	}
}

func sortedKeys(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

func CleanHours(raw any) []int {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	set := map[int]bool{}
	for _, v := range list {
		if s, isStr := v.(string); isStr {
			v = strings.Split(s, ":")[0]
		}
		if f, ok := CoerceNum(v); ok && f == math.Trunc(f) && f >= 0 && f <= 23 {
			set[int(f)] = true
		}
	}
	return sortedKeys(set)
}

// CanonHours resolves the dual channel. The window expansion is authoritative;
// disagreement with the model's own hours list is a free error signal.
func CanonHours(rawHours, start, end any, nonContiguous bool) ([]int, bool) {
	win := ExpandWindow(start, end)
	list := CleanHours(rawHours)
	if nonContiguous && len(list) > 0 {
		return list, false
	}
	if win != nil && len(list) > 0 {
		return win, !sameSet(win, list)
	}
	if win != nil {
		return win, false
	}
	return list, false
}

func sameSet(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[int]bool{}
	for _, v := range a {
		m[v] = true
	}
	for _, v := range b {
		if !m[v] {
			return false
		}
	}
	return true
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

func toAnyHours(hrs []int) []any {
	out := make([]any, len(hrs))
	for i, h := range hrs {
		out[i] = float64(h)
	}
	return out
}

// ResolveAdjustment performs every unit conversion. Returns (adj, ok).
// ok=false means downgrade to no_op - we never invent a missing value.
func ResolveAdjustment(dtype, semantics string, number float64, hasNumber bool,
	hours []int, bat Battery) (map[string]any, bool) {

	switch dtype {
	case TypeNoOp:
		return nil, false
	case TypeNoChargeWindow, TypeNoDischargeWin:
		return map[string]any{"hours": toAnyHours(hours)}, true
	}
	if !hasNumber {
		return nil, false
	}

	switch dtype {
	case TypeSolarReduction:
		f := number / 100.0
		if semantics == "solar_reduction_percent" {
			f = 1.0 - number/100.0
		}
		f = clamp(f, 0.0, 1.0)
		if f >= 0.999 { // an increase or no change is not a reduction
			return nil, false
		}
		return map[string]any{"hours": toAnyHours(hours), "factor": round(f, 4)}, true

	case TypeMinBatteryRes:
		v := number
		switch semantics {
		case "reserve_percent_of_capacity":
			v = number / 100.0 * bat.CapacityKwh
		case "reserve_above_base_kwh":
			v = bat.MinimumEnergyKwh + number
		}
		return map[string]any{"hours": toAnyHours(hours),
			"minimum_energy_kwh": round(clamp(v, 0, bat.CapacityKwh), 4)}, true

	case TypeMaxGridWindow:
		v := number
		if semantics == "grid_cap_total_over_window_kwh" && len(hours) > 0 {
			v = number / float64(len(hours))
		}
		return map[string]any{"hours": toAnyHours(hours),
			"max_grid_kwh": round(math.Max(0, v), 4)}, true
	}
	return nil, false
}

func NoOpEntry(index int, explanation string) DirectiveInterpretation {
	if explanation == "" {
		explanation = defaultExplanation[TypeNoOp]
	}
	return DirectiveInterpretation{NoteIndex: index, Applies: false,
		DirectiveType: TypeNoOp, StructuredAdjustment: nil, Explanation: explanation}
}

func CleanExplanation(text any, dtype string) string {
	s := strings.TrimSpace(wsRe.ReplaceAllString(toStr(text), " "))
	s = secretRe.ReplaceAllString(s, "[redacted]")
	if s == "" {
		s = defaultExplanation[dtype]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// BuildEntry turns one raw LLM object into a spec-shaped interpretation entry.
// note_index is assigned by POSITION and never read from the model, so
// completeness and ordering are structurally guaranteed.
func BuildEntry(index int, raw map[string]any, bat Battery) (DirectiveInterpretation, []string) {
	var flags []string
	if raw == nil {
		return NoOpEntry(index, ""), []string{"no_object"}
	}
	dtype := CoerceType(raw["directive_type"])
	if !CoerceBool(raw["applies_today"], true) {
		dtype = TypeNoOp
	}
	if dtype == TypeNoOp {
		return NoOpEntry(index, CleanExplanation(raw["explanation"], TypeNoOp)), flags
	}

	hours, disagreed := CanonHours(raw["hours"], raw["window_start_hour"],
		raw["window_end_hour_exclusive"], CoerceBool(raw["non_contiguous"], false))
	if disagreed {
		flags = append(flags, "hour_channel_disagreement")
	}
	if len(hours) == 0 {
		return NoOpEntry(index, ""), append(flags, "empty_hours")
	}

	semantics := strings.ToLower(strings.TrimSpace(toStr(raw["value_semantics"])))
	if !validSemantics[semantics] {
		semantics = "none"
	}
	num, hasNum := CoerceNum(raw["value_number"])
	adj, ok := ResolveAdjustment(dtype, semantics, num, hasNum, hours, bat)
	if !ok {
		return NoOpEntry(index, ""), append(flags, "unresolvable_value")
	}
	return DirectiveInterpretation{NoteIndex: index, Applies: true, DirectiveType: dtype,
		StructuredAdjustment: adj, Explanation: CleanExplanation(raw["explanation"], dtype)}, flags
}
