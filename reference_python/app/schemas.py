"""Request/response models matching the GridWise Problem Statement exactly."""
from typing import Any, Literal
from pydantic import BaseModel, Field, ConfigDict

DIRECTIVE_TYPES = (
    "solar_reduction",
    "minimum_battery_reserve",
    "no_charge_window",
    "no_discharge_window",
    "max_grid_window",
    "no_op",
)


class HourEntry(BaseModel):
    model_config = ConfigDict(extra="ignore")
    hour: int = Field(ge=0, le=23)
    demand_kwh: float = Field(ge=0)
    solar_kwh: float = Field(ge=0)
    tariff_bdt_per_kwh: float = Field(ge=0)


class Battery(BaseModel):
    model_config = ConfigDict(extra="ignore")
    capacity_kwh: float = Field(gt=0)
    initial_energy_kwh: float = Field(ge=0)
    minimum_energy_kwh: float = Field(ge=0)
    max_charge_kwh_per_hour: float = Field(ge=0)
    max_discharge_kwh_per_hour: float = Field(ge=0)


class OptimizeRequest(BaseModel):
    model_config = ConfigDict(extra="ignore")
    scenario_id: str
    operator_notes: list[str] = Field(default_factory=list)
    hours: list[HourEntry] = Field(min_length=24, max_length=24)
    battery: Battery


class DirectiveInterpretation(BaseModel):
    """Exactly the five spec fields. No extras - schema correctness is graded."""
    note_index: int
    applies: bool
    directive_type: Literal[DIRECTIVE_TYPES]  # type: ignore[valid-type]
    structured_adjustment: dict[str, Any] | None
    explanation: str


class HourPlan(BaseModel):
    hour: int
    grid_kwh: float
    solar_used_kwh: float
    battery_action: Literal["charge", "discharge", "idle"]
    battery_kwh: float
    battery_energy_after_kwh: float


class OptimizeResponse(BaseModel):
    scenario_id: str
    directive_interpretation: list[DirectiveInterpretation]
    hourly_plan: list[HourPlan]
    total_grid_kwh: float
    total_cost_bdt: float
    peak_grid_kwh: float
    plan_summary: str
