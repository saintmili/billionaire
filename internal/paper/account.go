// Package paper defines paper account contracts independently of transport/storage.
package paper

import (
	"errors"
	"regexp"
	"time"

	"github.com/saintmili/billionaire/internal/domain"
)

var (
	accountName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _.-]{0,79}$`)
	requestID   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
)

type CreateAccount struct {
	RequestID      string
	Name           string
	Currency       string
	InitialBalance string
}

func ValidateInitialBalance(text string) (domain.Decimal, error) {
	value, err := domain.ParseDecimal(text)
	maximum, _ := domain.ParseDecimal("1000000000000000000")
	if err != nil || !value.IsPositive() || value.GreaterThan(maximum) || value.Exponent() < -18 {
		return domain.Decimal{}, errors.New("initial balance must be positive, at most 1000000000000000000 USDT, with at most 18 decimal places")
	}
	return value, nil
}

func (r CreateAccount) Validate() (domain.Decimal, error) {
	if !accountName.MatchString(r.Name) {
		return domain.Decimal{}, errors.New("account name must be 1-80 ASCII letters, digits, spaces, underscores, dots or hyphens, beginning with a letter or digit")
	}
	if !requestID.MatchString(r.RequestID) {
		return domain.Decimal{}, errors.New("request ID must be 1-128 ASCII letters, digits, underscores, dots, colons or hyphens, beginning with a letter or digit")
	}
	if r.Currency != "USDT" {
		return domain.Decimal{}, errors.New("paper account currency must be USDT")
	}
	return ValidateInitialBalance(r.InitialBalance)
}

type Account struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Currency  string         `json:"currency"`
	Balance   domain.Decimal `json:"balance"`
	CreatedAt time.Time      `json:"created_at"`
}

type LedgerEntry struct {
	ID        string         `json:"id"`
	AccountID string         `json:"account_id"`
	Kind      string         `json:"kind"`
	Amount    domain.Decimal `json:"amount"`
	CreatedAt time.Time      `json:"created_at"`
}
