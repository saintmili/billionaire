package paper

import "encoding/json"

// ControlOutcome is an immutable command receipt, never a current order read model.
type ControlOutcome struct {
	RequestID string          `json:"request_id"`
	Status    string          `json:"status"`
	Result    json.RawMessage `json:"result,omitempty"`
	Reason    string          `json:"reason,omitempty"`
}
