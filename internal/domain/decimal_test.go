package domain_test

import (
	"strings"
	"testing"

	"github.com/saintmili/billionaire/internal/domain"
)

func TestDecimalPreservesFinancialPrecision(t *testing.T) {
	a, err := domain.ParseDecimal("9007199254740993.01")
	if err != nil {
		t.Fatal(err)
	}
	b, err := domain.ParseDecimal("0.09")
	if err != nil {
		t.Fatal(err)
	}
	if got := a.Add(b).String(); got != "9007199254740993.1" {
		t.Fatalf("sum = %s", got)
	}
}

func TestDecimalRejectsNonFiniteAndImpreciseInputs(t *testing.T) {
	for _, text := range []string{"NaN", "Inf", "1e3", " 1", "1.", ".1", "", strings.Repeat("9", 129)} {
		if _, err := domain.ParseDecimal(text); err == nil {
			t.Errorf("accepted %q", text)
		}
	}
	for _, text := range []string{"0", "-0.01", "+2.5"} {
		if _, err := domain.ParseDecimal(text); err != nil {
			t.Errorf("rejected %q", text)
		}
	}
}

func TestInstrumentIdentityKeepsMarketsSeparate(t *testing.T) {
	spot := domain.Instrument{Provider: "bybit", Market: domain.Spot, Base: "BTC", Quote: "USDT", ID: "BTCUSDT"}
	if err := spot.Validate(); err != nil {
		t.Fatal(err)
	}
	linear := spot
	linear.Market = domain.Linear
	if spot == linear {
		t.Fatal("spot and linear identities collapsed")
	}
	linear.Market = "inverse"
	if err := linear.Validate(); err == nil {
		t.Fatal("unsupported market accepted")
	}
	if _, err := domain.ParseTimeframe("17s"); err == nil {
		t.Fatal("unsupported interval accepted")
	}
}
