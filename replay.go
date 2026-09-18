package main

import "math"

const Tol = 0.01

// Validate re-checks a finished plan independently of the optimizer, mirroring
// what the judge replays. Never ship a plan that fails this.
func Validate(plan []HourPlan, totalGrid, totalCost, peak float64, cx *Constraints) []string {
	var errs []string
	if len(plan) != NHours {
		return []string{"hourly_plan must contain exactly 24 entries"}
	}
	seen := map[int]bool{}
	for _, p := range plan {
		if p.Hour < 0 || p.Hour >= NHours || seen[p.Hour] {
			return []string{"hourly_plan must contain unique hours 0-23"}
		}
		seen[p.Hour] = true
	}

	b := cx.Bat
	E := b.InitialEnergyKwh
	gt, ct, pk := 0.0, 0.0, 0.0
	for _, p := range plan {
		h := p.Hour
		for _, v := range []float64{p.GridKwh, p.SolarUsedKwh, p.BatteryKwh} {
			if math.IsNaN(v) || math.IsInf(v, 0) || v < -Tol {
				errs = append(errs, "h"+itoa(h)+": values must be finite and non-negative")
			}
		}
		chg, dis := 0.0, 0.0
		switch p.BatteryAction {
		case "charge":
			chg = p.BatteryKwh
		case "discharge":
			dis = p.BatteryKwh
		case "idle":
			if math.Abs(p.BatteryKwh) > Tol {
				errs = append(errs, "h"+itoa(h)+": battery_kwh must be 0 when idle")
			}
		default:
			errs = append(errs, "h"+itoa(h)+": invalid battery_action")
			continue
		}
		if math.Abs(p.GridKwh+p.SolarUsedKwh+dis-(cx.Demand[h]+chg)) > Tol {
			errs = append(errs, "h"+itoa(h)+": energy balance violated")
		}
		if p.SolarUsedKwh > cx.EffSolar[h]+Tol {
			errs = append(errs, "h"+itoa(h)+": solar_used exceeds effective solar")
		}
		if chg > b.MaxChargeKwhPerHour+Tol {
			errs = append(errs, "h"+itoa(h)+": charge rate exceeded")
		}
		if dis > b.MaxDischargeKwhPerHour+Tol {
			errs = append(errs, "h"+itoa(h)+": discharge rate exceeded")
		}
		if cx.NoCharge[h] && chg > Tol {
			errs = append(errs, "h"+itoa(h)+": no_charge_window violated")
		}
		if cx.NoDischg[h] && dis > Tol {
			errs = append(errs, "h"+itoa(h)+": no_discharge_window violated")
		}
		if cap, ok := cx.GridCap[h]; ok && p.GridKwh > cap+Tol {
			errs = append(errs, "h"+itoa(h)+": max_grid_window violated")
		}
		E = E + chg - dis
		if math.Abs(E-p.BatteryEnergyAfterKwh) > Tol {
			errs = append(errs, "h"+itoa(h)+": battery_energy_after_kwh inconsistent")
		}
		if E < cx.Reserve[h]-Tol {
			errs = append(errs, "h"+itoa(h)+": battery below reserve")
		}
		if E > b.CapacityKwh+Tol {
			errs = append(errs, "h"+itoa(h)+": battery above capacity")
		}
		gt += p.GridKwh
		ct += p.GridKwh * cx.Tariff[h]
		if p.GridKwh > pk {
			pk = p.GridKwh
		}
	}
	if math.Abs(E-b.InitialEnergyKwh) > Tol {
		errs = append(errs, "end-of-day battery does not return to initial level")
	}
	if math.Abs(gt-totalGrid) > Tol {
		errs = append(errs, "total_grid_kwh disagrees with hourly_plan")
	}
	if math.Abs(ct-totalCost) > Tol {
		errs = append(errs, "total_cost_bdt disagrees with hourly_plan")
	}
	if math.Abs(pk-peak) > Tol {
		errs = append(errs, "peak_grid_kwh disagrees with hourly_plan")
	}
	return errs
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [4]byte
	n := 0
	for i > 0 {
		b[n] = byte('0' + i%10)
		i /= 10
		n++
	}
	for l, r := 0, n-1; l < r; l, r = l+1, r-1 {
		b[l], b[r] = b[r], b[l]
	}
	return string(b[:n])
}
