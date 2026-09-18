package main

// Request/response types matching the GridWise Problem Statement exactly.

type HourEntry struct {
	Hour        int     `json:"hour"`
	DemandKwh   float64 `json:"demand_kwh"`
	SolarKwh    float64 `json:"solar_kwh"`
	TariffPerKwh float64 `json:"tariff_bdt_per_kwh"`
}

type Battery struct {
	CapacityKwh         float64 `json:"capacity_kwh"`
	InitialEnergyKwh    float64 `json:"initial_energy_kwh"`
	MinimumEnergyKwh    float64 `json:"minimum_energy_kwh"`
	MaxChargeKwhPerHour float64 `json:"max_charge_kwh_per_hour"`
	MaxDischargeKwhPerHour float64 `json:"max_discharge_kwh_per_hour"`
}

type OptimizeRequest struct {
	ScenarioID    string      `json:"scenario_id"`
	OperatorNotes []string    `json:"operator_notes"`
	Hours         []HourEntry `json:"hours"`
	Battery       Battery     `json:"battery"`
}

// DirectiveInterpretation carries exactly the five spec fields. Adding any
// extra key risks the API Contract & Schema points.
type DirectiveInterpretation struct {
	NoteIndex            int            `json:"note_index"`
	Applies              bool           `json:"applies"`
	DirectiveType        string         `json:"directive_type"`
	StructuredAdjustment map[string]any `json:"structured_adjustment"`
	Explanation          string         `json:"explanation"`
}

type HourPlan struct {
	Hour                  int     `json:"hour"`
	GridKwh               float64 `json:"grid_kwh"`
	SolarUsedKwh          float64 `json:"solar_used_kwh"`
	BatteryAction         string  `json:"battery_action"`
	BatteryKwh            float64 `json:"battery_kwh"`
	BatteryEnergyAfterKwh float64 `json:"battery_energy_after_kwh"`
}

type OptimizeResponse struct {
	ScenarioID              string                    `json:"scenario_id"`
	DirectiveInterpretation []DirectiveInterpretation `json:"directive_interpretation"`
	HourlyPlan              []HourPlan                `json:"hourly_plan"`
	TotalGridKwh            float64                   `json:"total_grid_kwh"`
	TotalCostBdt            float64                   `json:"total_cost_bdt"`
	PeakGridKwh             float64                   `json:"peak_grid_kwh"`
	PlanSummary             string                    `json:"plan_summary"`
}

const (
	TypeSolarReduction  = "solar_reduction"
	TypeMinBatteryRes   = "minimum_battery_reserve"
	TypeNoChargeWindow  = "no_charge_window"
	TypeNoDischargeWin  = "no_discharge_window"
	TypeMaxGridWindow   = "max_grid_window"
	TypeNoOp            = "no_op"
)

const NHours = 24
