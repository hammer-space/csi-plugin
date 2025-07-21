package common

import (
	"encoding/json"
	"fmt"
)

type ProgressValue float64

// UnmarshalJSON implements the json.Unmarshaler interface for ProgressValue.
func (p *ProgressValue) UnmarshalJSON(data []byte) error {
	var floatVal float64
	if err := json.Unmarshal(data, &floatVal); err == nil {
		*p = ProgressValue(floatVal)
		return nil
	}

	var intVal int64
	if err := json.Unmarshal(data, &intVal); err == nil {
		// Convert int (assume old format: 1–100) to float
		*p = ProgressValue(float64(intVal) / 100.0)
		return nil
	}

	return fmt.Errorf("invalid progress format: %s", string(data))
}

func (p ProgressValue) Percentage() int {
	return int(float64(p) * 100)
}
