package domain

import (
	"fmt"
	"math"
)

const DefaultGenerationTemperature = 0.4
const DefaultCheckTemperature = 0.2

// ModelSamplingConfig controls sampling only, never reasoning or output budgets.
type ModelSamplingConfig struct {
	GenerationTemperature float64 `json:"generation_temperature"`
	CheckTemperature      float64 `json:"check_temperature"`
	Revision              int64   `json:"revision"`
}

func DefaultModelSamplingConfig() ModelSamplingConfig {
	return ModelSamplingConfig{GenerationTemperature: DefaultGenerationTemperature, CheckTemperature: DefaultCheckTemperature}
}

func ValidateTemperature(value float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 2 {
		return fmt.Errorf("温度须在0到2之间")
	}
	return nil
}

func (c ModelSamplingConfig) Validate() error {
	if err := ValidateTemperature(c.GenerationTemperature); err != nil {
		return err
	}
	return ValidateTemperature(c.CheckTemperature)
}
