package main

import (
	"regexp"
	"strconv"
	"strings"
)

// A deterministic shadow reader used ONLY as a cross-check oracle. It may
// correct one axis of, or suppress, a directive the LLM produced. It must never
// originate a directive - except in fallbackExtract, the explicit safe-failure
// path used only when every provider is unreachable.

var (
	otherDayRe = regexp.MustCompile(`(?i)\b(tomorrow|next\s+(week|month|year|term|semester|billing)|yesterday|last\s+(night|week|month)|after\s+eid|coming\s+(week|month))\b`)
	todayRe    = regexp.MustCompile(`(?i)\b(today|tonight|this\s+(morning|afternoon|evening|hour)|right\s+now|currently)\b`)

	// High-precision polarity cues around a stated percentage.
	reductionRe = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?\s*(?:%|percent)\s*(?:reduction|drop|decrease|cut|loss)|(?:reduc\w*|decreas\w*|drop\w*|cut|down|derate\w*|los\w*|knock\w*)\s+(?:\w+\s+){0,3}?by\s+\w*\s*\d+(?:\.\d+)?\s*(?:%|percent)|drops?\s+\d+(?:\.\d+)?\s*(?:%|percent))`)
	remainingRe = regexp.MustCompile(`(?i)((?:to|at|as|only|just)\s+(?:about\s+|around\s+|roughly\s+|approximately\s+)?\d+(?:\.\d+)?\s*(?:%|percent)|\d+(?:\.\d+)?\s*(?:%|percent)\s+of\b|(?:leav\w*|remain\w*|limited)\s+(?:\w+\s+){0,3}?\d+(?:\.\d+)?\s*(?:%|percent))`)

	clockRe = regexp.MustCompile(`(?i)\b(\d{1,2})(?::(\d{2}))?\s*(am|pm|noon|midnight)?\b`)
)

// crossCheck applies high-precision single-axis corrections to raw LLM output.
func crossCheck(raw map[string]any, note string) map[string]any {
	// d4: the note is anchored to another day, with no counter-signal for today.
	if otherDayRe.MatchString(note) && !todayRe.MatchString(note) {
		if CoerceBool(raw["applies_today"], true) {
			raw["applies_today"] = false
			raw["directive_type"] = TypeNoOp
		}
	}
	// d5: BY/TO polarity flip on a solar reduction. Regex polarity is more
	// reliable here than a model performing the inversion implicitly.
	if CoerceType(raw["directive_type"]) == TypeSolarReduction {
		red, rem := reductionRe.MatchString(note), remainingRe.MatchString(note)
		if red != rem { // exactly one cue fired
			want := "solar_remaining_percent"
			if red {
				want = "solar_reduction_percent"
			}
			if got := strings.ToLower(toStr(raw["value_semantics"])); got != want &&
				(got == "solar_remaining_percent" || got == "solar_reduction_percent") {
				raw["value_semantics"] = want
			}
		}
	}
	return raw
}

// ---------------------------------------------------------------- fallback

var typeCues = []struct {
	t    string
	re   *regexp.Regexp
}{
	{TypeNoChargeWindow, regexp.MustCompile(`(?i)(charg\w*\s+(circuit|contactor|controller)|charger|rectifier|charge\s+inhibit|not?\s+charg\w*|no\s+charging|avoid\s+charging|cannot\s+charge|don'?t\s+charge|top\s+up)`)},
	{TypeNoDischargeWin, regexp.MustCompile(`(?i)(not?\s+discharg\w*|no\s+discharging|cannot\s+discharge|don'?t\s+discharge|relay\s+test|protection\s+test|islanding|megger|charge-only|discharge\s+contactor)`)},
	{TypeMaxGridWindow, regexp.MustCompile(`(?i)(grid\s+(import|intake|draw|consumption)|feeder|transformer|substation|incomer|switchgear|mains|sanctioned\s+load|utility|must\s+not\s+exceed|cap\w*\s+at)`)},
	{TypeMinBatteryRes, regexp.MustCompile(`(?i)(keep\s+at\s+least|maintain|reserve|must\s+not\s+fall\s+below|remain\s+in\s+the\s+battery|stay\s+(at\s+or\s+)?above|state\s+of\s+charge|\bsoc\b|backup)`)},
	{TypeSolarReduction, regexp.MustCompile(`(?i)(solar|pv\b|rooftop|panel|array|inverter|cloud|haze|shading|generation)`)},
}

func hour24(v int, mer string) int {
	switch strings.ToLower(mer) {
	case "pm":
		if v != 12 {
			v += 12
		}
	case "am":
		if v == 12 {
			v = 0
		}
	case "noon":
		v = 12
	case "midnight":
		v = 0
	}
	return v % 25
}

// fallbackExtract is the last-resort safe-failure path (all providers down).
// It is deliberately conservative: it returns no_op unless it is confident.
func fallbackExtract(idx int, note string, bat Battery) DirectiveInterpretation {
	dtype := TypeNoOp
	for _, c := range typeCues {
		if c.re.MatchString(note) {
			dtype = c.t
			break
		}
	}
	if dtype == TypeNoOp || (otherDayRe.MatchString(note) && !todayRe.MatchString(note)) {
		return NoOpEntry(idx, "")
	}

	// Bare day-words carry no digits; rewrite them so the clock regex sees them.
	norm := strings.NewReplacer(
		"noon", "12pm", "Noon", "12pm", "NOON", "12pm",
		"midnight", "12am", "Midnight", "12am", "MIDNIGHT", "12am",
	).Replace(note)
	// Strip quantity tokens so "25%" or "155 kWh" is never read as a clock hour.
	norm = regexp.MustCompile(`(?i)\d+(?:\.\d+)?\s*(?:%|percent|kwh|kw\b|mwh|units?)`).ReplaceAllString(norm, " ")

	var hs []int
	ms := clockRe.FindAllStringSubmatch(norm, -1)
	for _, m := range ms {
		if m[3] == "" && m[1] == "" {
			continue
		}
		if m[1] == "" { // bare noon/midnight
			hs = append(hs, hour24(0, m[3]))
			continue
		}
		v, err := strconv.Atoi(m[1])
		if err != nil || v > 24 {
			continue
		}
		if m[3] == "" && v > 23 {
			continue
		}
		hs = append(hs, hour24(v, m[3]))
	}
	if len(hs) < 2 {
		return NoOpEntry(idx, "")
	}
	hours := ExpandWindow(hs[0], hs[1])
	if len(hours) == 0 {
		return NoOpEntry(idx, "")
	}

	sem, num, has := "none", 0.0, false
	if pm := regexp.MustCompile(`(\d+(?:\.\d+)?)\s*(?:%|percent)`).FindStringSubmatch(note); pm != nil {
		num, _ = strconv.ParseFloat(pm[1], 64)
		has = true
		switch dtype {
		case TypeSolarReduction:
			sem = "solar_remaining_percent"
			if reductionRe.MatchString(note) {
				sem = "solar_reduction_percent"
			}
		case TypeMinBatteryRes:
			sem = "reserve_percent_of_capacity"
		}
	} else if km := regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(?:kwh|kw\b|units?)`).FindStringSubmatch(note); km != nil {
		num, _ = strconv.ParseFloat(km[1], 64)
		has = true
		switch dtype {
		case TypeMinBatteryRes:
			sem = "reserve_absolute_kwh"
		case TypeMaxGridWindow:
			sem = "grid_cap_kwh_per_hour"
		}
	}

	adj, ok := ResolveAdjustment(dtype, sem, num, has, hours, bat)
	if !ok {
		return NoOpEntry(idx, "")
	}
	return DirectiveInterpretation{NoteIndex: idx, Applies: true, DirectiveType: dtype,
		StructuredAdjustment: adj, Explanation: defaultExplanation[dtype]}
}
