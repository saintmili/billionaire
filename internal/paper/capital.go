package paper

import (
	"errors"
	"github.com/saintmili/billionaire/internal/domain"
	"strings"
	"time"
)

// CapitalAdjustment is an external simulated cash flow, never trading P&L.
// Its stable identity is the enclosing control request ID.
type CapitalAdjustment struct {
	AccountID string    `json:"account_id"`
	Direction string    `json:"direction"`
	Amount    string    `json:"amount"`
	Currency  string    `json:"currency"`
	Reason    string    `json:"reason"`
	At        time.Time `json:"at"`
}

func (r CapitalAdjustment) Validate() (domain.Decimal, error) {
	if r.AccountID == "" {
		return domain.Decimal{}, errors.New("account_id is required")
	}
	if r.Direction != "deposit" && r.Direction != "withdraw" {
		return domain.Decimal{}, errors.New("direction must be deposit or withdraw")
	}
	if r.Currency != "USDT" {
		return domain.Decimal{}, errors.New("capital currency must be USDT")
	}
	if strings.TrimSpace(r.Reason) == "" || len(r.Reason) > 500 || strings.ContainsAny(r.Reason, "\x00\r\n") {
		return domain.Decimal{}, errors.New("reason must be 1-500 bytes without line breaks")
	}
	if r.At.IsZero() || r.At.Year() < 1970 || r.At.Year() > 9999 {
		return domain.Decimal{}, errors.New("at must be an explicit RFC3339 timestamp")
	}
	amount, err := ValidateInitialBalance(r.Amount)
	if err != nil {
		return amount, errors.New("amount must be positive, at most 1000000000000000000 USDT, with at most 18 decimal places")
	}
	return amount, nil
}
