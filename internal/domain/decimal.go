// Package domain defines shared market identities and exact financial values.
package domain

import (
	"errors"
	"regexp"

	"github.com/shopspring/decimal"
)

// Decimal is an exact base-ten value. Construct external values with ParseDecimal.
// Sign and range constraints belong to each domain field; funding can be negative.
type Decimal = decimal.Decimal

var plainDecimal = regexp.MustCompile(`^[+-]?[0-9]+(\.[0-9]+)?$`)

func ParseDecimal(text string) (Decimal, error) {
	if len(text) > 128 || !plainDecimal.MatchString(text) {
		return Decimal{}, errors.New("expected a plain decimal of at most 128 characters")
	}
	value, err := decimal.NewFromString(text)
	if err != nil {
		return Decimal{}, errors.New("invalid decimal")
	}
	return value, nil
}
