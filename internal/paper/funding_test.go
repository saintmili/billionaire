package paper_test

import (
	"testing"
	"time"

	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/paper"
)

func TestFundingSignsAndIsolatedCollateral(t *testing.T) {
	for _, tc := range []struct{ side, rate, want string }{{"long", "0.01", "-2"}, {"short", "0.01", "2"}, {"long", "-0.01", "2"}, {"short", "-0.01", "-2"}} {
		p := paper.Position{Side: tc.side, RemainingQuantity: fundDecimal("2"), ReservedMargin: fundDecimal("40")}
		delta, err := paper.FundingPayment(p, tc.rate, fundDecimal("100"))
		if err != nil || delta.String() != tc.want {
			t.Fatalf("%+v: %s %v", tc, delta, err)
		}
	}
}
func TestSimplifiedLiquidationEqualityAndDirection(t *testing.T) {
	for _, tc := range []struct {
		side, mark string
		hit        bool
	}{{"long", "80.4", false}, {"long", "80", true}, {"short", "119", false}, {"short", "120", true}} {
		p := paper.Position{Side: tc.side, Status: "open", Instrument: domain.Instrument{Market: domain.Linear}, RemainingQuantity: fundDecimal("2"), ReservedMargin: fundDecimal("40"), EntryPrice: fundDecimal("100")}
		hit := paper.LiquidationRequired(p, fundDecimal(tc.mark), paper.LiquidationModel{MaintenanceRate: "0", FeeRate: "0"})
		if hit != tc.hit {
			t.Fatalf("%+v: %v", tc, hit)
		}
	}
}
func fundDecimal(s string) domain.Decimal { d, _ := domain.ParseDecimal(s); return d }

func TestLiquidationOrdersCandleBoundariesAgainstProtection(t *testing.T) {
	at := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		side, open, high, low, stop, want string
		gap                               bool
	}{
		{"long", "100", "125", "70", "90", "stop_loss", false},
		{"long", "70", "125", "60", "90", "liquidation_approximate", true},
		{"long", "100", "125", "70", "75", "liquidation_approximate", false},
		{"short", "100", "130", "75", "110", "stop_loss", false},
		{"short", "130", "140", "75", "110", "liquidation_approximate", true},
	} {
		t.Run(tc.side+tc.open+tc.stop, func(t *testing.T) {
			info := marketdata.InstrumentInfo{Instrument: domain.Instrument{Provider: "fixture", Market: domain.Linear, Base: "BTC", Quote: "USDT", ID: "BTCUSDT"}, PriceStep: fundDecimal("0.01"), QuantityStep: fundDecimal("0.001"), MinQuantity: fundDecimal("0.001"), MinNotional: fundDecimal("5")}
			p := paper.Position{Status: "open", Instrument: info.Instrument, Metadata: info, Side: tc.side, EntryPrice: fundDecimal("100"), ReservedMargin: fundDecimal("40"), RemainingQuantity: fundDecimal("2"), Protection: &paper.ProtectionState{ProtectionConfig: paper.ProtectionConfig{StopPrice: tc.stop}, UpdatedAt: at}}
			obs := paper.Observation{ID: "candle", Instrument: info.Instrument, Source: "fixture", Price: fundDecimal("100"), Ready: true, ObservedAt: at.Add(2 * time.Second), AvailableAt: at.Add(2 * time.Second), Bar: &paper.PriceBar{Start: at.Add(time.Second), End: at.Add(2 * time.Second), Open: fundDecimal(tc.open), High: fundDecimal(tc.high), Low: fundDecimal(tc.low), Close: fundDecimal("100")}}
			protect, err := paper.EvaluateProtection(p, obs)
			if err != nil {
				t.Fatal(err)
			}
			liq, err := paper.EvaluateLiquidation(p, obs, protect)
			if err != nil {
				t.Fatal(err)
			}
			decision := protect
			if liq.Reason != "" {
				decision = liq
			}
			if decision.Reason != tc.want || decision.Gap != tc.gap {
				t.Fatalf("decision %+v want %s gap %v", decision, tc.want, tc.gap)
			}
		})
	}
}

func TestFundingAndLiquidationRejectInvalidAssumptions(t *testing.T) {
	for _, m := range []paper.LiquidationModel{{MaintenanceRate: "-0.01", FeeRate: "0"}, {MaintenanceRate: "0.9", FeeRate: "0.1"}, {MaintenanceRate: "0", FeeRate: "0.0000000000000000001"}} {
		if m.Validate() == nil {
			t.Fatalf("invalid liquidation assumptions accepted %+v", m)
		}
	}
	for _, rate := range []string{"1.01", "-1.01", "0.0000000000000000001", "NaN"} {
		if _, err := paper.FundingPayment(paper.Position{}, rate, fundDecimal("100")); err == nil {
			t.Fatalf("invalid funding rate accepted %q", rate)
		}
	}
	at := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	e := paper.FundingEvent{ID: "missing-rate", Instrument: domain.Instrument{Provider: "fixture", Market: domain.Linear, Base: "BTC", Quote: "USDT", ID: "BTCUSDT"}, EffectiveAt: at, AvailableAt: at, Source: "fixture", Provenance: "explicit interval"}
	if e.Validate() == nil {
		t.Fatal("missing rate without reason accepted")
	}
	e.MissingReason = "unavailable"
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
}
