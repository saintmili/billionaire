package paper

import "time"

// SignalEntryRequest explicitly distinguishes approval from offline consumption.
// Empty sizing/fee fields select risk sizing, 1x leverage, 0.1% fee and zero slippage.
type SignalEntryRequest struct {
	Protection  *ProtectionConfig `json:"protection_overrides,omitempty"`
	SignalID    string            `json:"signal_id"`
	Approve     bool              `json:"approve"`
	At          time.Time         `json:"at"`
	Quantity    string            `json:"quantity"`
	Leverage    int               `json:"leverage"`
	FeeRate     string            `json:"fee_rate"`
	SlippageBPS string            `json:"slippage_bps"`
}

type SignalDecision struct {
	SignalID string             `json:"signal_id"`
	Status   string             `json:"status"`
	Reason   string             `json:"reason,omitempty"`
	Request  SignalEntryRequest `json:"request"`
	Order    *Order             `json:"order,omitempty"`
}

func (r SignalEntryRequest) Normalize() SignalEntryRequest {
	r.At = r.At.UTC()
	if r.Quantity == "" {
		r.Quantity = "risk"
	}
	if r.Leverage == 0 {
		r.Leverage = 1
	}
	if r.FeeRate == "" {
		r.FeeRate = "0.001"
	}
	if r.SlippageBPS == "" {
		r.SlippageBPS = "0"
	}
	return r
}
