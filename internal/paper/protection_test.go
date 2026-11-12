package paper_test

import (
	"github.com/saintmili/billionaire/internal/paper"
	"testing"
	"time"
)

func TestProtectionAdverseFirstAndGap(t *testing.T) {
	at := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	p := paper.Position{ID: "p", AccountID: "a", Instrument: metadata().Instrument, Metadata: metadata(), Status: "open", EntryPrice: amount("100"), RemainingQuantity: amount("2"), Protection: &paper.ProtectionState{ProtectionConfig: paper.ProtectionConfig{StopPrice: "90", TargetPrice: "120"}, UpdatedAt: at}}
	o := paper.Observation{ID: "bar", Instrument: p.Instrument, Source: "fixture", Price: amount("110"), ObservedAt: at.Add(2 * time.Minute), AvailableAt: at.Add(2 * time.Minute), Ready: true, Bar: &paper.PriceBar{Start: at.Add(time.Minute), End: at.Add(2 * time.Minute), Open: amount("100"), High: amount("125"), Low: amount("85"), Close: amount("110")}}
	d, err := paper.EvaluateProtection(p, o)
	if err != nil || d.Reason != "stop_loss" || !d.Ambiguous || d.Price.String() != "90" || d.Quantity != "2" {
		t.Fatalf("collision: %+v %v", d, err)
	}
	o.Bar.Open = amount("80")
	o.Bar.Low = amount("75")
	d, err = paper.EvaluateProtection(p, o)
	if err != nil || d.Price.String() != "80" || !d.Gap {
		t.Fatalf("gap: %+v %v", d, err)
	}
}

func TestTrailingUsesNextObservationAndPartialTargetOnlyOnce(t *testing.T) {
	at := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	p := paper.Position{ID: "p", Instrument: metadata().Instrument, Metadata: metadata(), Status: "open", RemainingQuantity: amount("2"), Protection: &paper.ProtectionState{ProtectionConfig: paper.ProtectionConfig{StopPrice: "90", TargetPrice: "120", TargetQuantity: "0.5", TrailActivation: "110", TrailDistance: "10"}, UpdatedAt: at}}
	o := paper.Observation{ID: "bar", Instrument: p.Instrument, Source: "fixture", Price: amount("115"), ObservedAt: at.Add(2 * time.Minute), AvailableAt: at.Add(2 * time.Minute), Ready: true, Bar: &paper.PriceBar{Start: at.Add(time.Minute), End: at.Add(2 * time.Minute), Open: amount("100"), High: amount("125.009"), Low: amount("95"), Close: amount("115")}}
	d, err := paper.EvaluateProtection(p, o)
	if err != nil || d.Reason != "take_profit" || d.Quantity != "0.5" || d.State.StopPrice != "115" {
		t.Fatalf("partial and ratchet: %+v %v", d, err)
	}
	p.Protection = &d.State
	p.RemainingQuantity = amount("1.5")
	o.Bar = nil
	o.ID = "next"
	o.Price = amount("125")
	o.ObservedAt = o.ObservedAt.Add(time.Second)
	o.AvailableAt = o.ObservedAt
	d, err = paper.EvaluateProtection(p, o)
	if err != nil || d.Reason != "" {
		t.Fatalf("target repeated: %+v %v", d, err)
	}
	p.Protection = &d.State
	o.Price = amount("110")
	o.ObservedAt = o.ObservedAt.Add(time.Second)
	o.AvailableAt = o.ObservedAt
	d, err = paper.EvaluateProtection(p, o)
	if err != nil || d.Reason != "trailing_stop" || d.Price.String() != "110" || d.Quantity != "1.5" {
		t.Fatalf("trailing gap: %+v %v", d, err)
	}
}

func TestManualStopAdjustmentValidatesCurrentReferenceAndPreservesTarget(t *testing.T) {
	at := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	p := paper.Position{ID: "p", Instrument: metadata().Instrument, Metadata: metadata(), Status: "open", RemainingQuantity: amount("2"), Protection: &paper.ProtectionState{ProtectionConfig: paper.ProtectionConfig{StopPrice: "90", TargetPrice: "120"}, UpdatedAt: at}}
	o := paper.Observation{ID: "price", Instrument: p.Instrument, Source: "fixture", Price: amount("110"), ObservedAt: at.Add(time.Second), AvailableAt: at.Add(time.Second), Ready: true}
	stop := "105"
	r := paper.ProtectionAdjustment{StaleAfter: time.Minute, RequestID: "adjust", PositionID: p.ID, At: at.Add(2 * time.Second), StopPrice: &stop}
	updated, err := paper.AdjustProtection(p, r, o)
	if err != nil || updated.Protection.StopPrice != "105" || updated.Protection.TargetPrice != "120" {
		t.Fatalf("adjust: %+v %v", updated, err)
	}
	stop = "110"
	if _, err := paper.AdjustProtection(p, r, o); err == nil {
		t.Fatal("stop at current price accepted")
	}
}

func TestProtectionGeometryAndUnusableObservations(t *testing.T) {
	at := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	p := paper.Position{ID: "p", Instrument: metadata().Instrument, Metadata: metadata(), Status: "open", RemainingQuantity: amount("2"), Protection: &paper.ProtectionState{ProtectionConfig: paper.ProtectionConfig{StopPrice: "90", TargetPrice: "120"}, UpdatedAt: at}}
	for _, c := range []paper.ProtectionConfig{{StopPrice: "0"}, {StopPrice: "90.001"}, {StopPrice: "100"}, {TargetPrice: "99"}, {TrailActivation: "110"}, {TrailActivation: "10", TrailDistance: "10"}, {TargetPrice: "120", TargetQuantity: "3"}, {TargetQuantity: "1"}} {
		if err := paper.ValidateProtection(c, p, amount("100")); err == nil {
			t.Fatalf("invalid geometry accepted: %+v", c)
		}
	}
	o := paper.Observation{ID: "price", Instrument: p.Instrument, Source: "fixture", Price: amount("80"), ObservedAt: at.Add(time.Second), AvailableAt: at.Add(time.Second), Ready: false}
	d, err := paper.EvaluateProtection(p, o)
	if err != nil || d.Reason != "" {
		t.Fatalf("unready fill: %+v %v", d, err)
	}
	o.Ready = true
	o.Price = amount("0")
	if _, err := paper.EvaluateProtection(p, o); err == nil {
		t.Fatal("zero observation accepted")
	}
	o.Price = amount("80")
	o.Bar = &paper.PriceBar{Start: at.Add(-time.Second), End: at.Add(time.Second), Open: amount("100"), High: amount("125"), Low: amount("75"), Close: amount("80")}
	d, err = paper.EvaluateProtection(p, o)
	if err != nil || d.Reason != "" {
		t.Fatalf("overlapping bar filled: %+v %v", d, err)
	}
	o.Bar.Low = amount("90")
	if _, err := paper.EvaluateProtection(p, o); err == nil {
		t.Fatal("inconsistent OHLC accepted")
	}
}

func TestStopAdjustmentAfterPartialTargetPreservesConsumedTarget(t *testing.T) {
	at := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	p := paper.Position{ID: "p", Instrument: metadata().Instrument, Metadata: metadata(), Status: "open", RemainingQuantity: amount("0.5"), Protection: &paper.ProtectionState{ProtectionConfig: paper.ProtectionConfig{StopPrice: "90", TargetPrice: "120", TargetQuantity: "1.5"}, TargetDone: true, UpdatedAt: at}}
	o := paper.Observation{ID: "price", Instrument: p.Instrument, Source: "fixture", Price: amount("125"), ObservedAt: at.Add(time.Second), AvailableAt: at.Add(time.Second), Ready: true}
	stop := "122"
	r := paper.ProtectionAdjustment{RequestID: "adjust", PositionID: p.ID, At: o.AvailableAt, StaleAfter: time.Minute, StopPrice: &stop}
	updated, err := paper.AdjustProtection(p, r, o)
	if err != nil || !updated.Protection.TargetDone || updated.Protection.StopPrice != "122" {
		t.Fatalf("stop after target: %+v %v", updated, err)
	}
}

func TestAdjustmentRejectsStaleBackdatedAndInvalidInputs(t *testing.T) {
	at := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	p := paper.Position{ID: "p", Instrument: metadata().Instrument, Metadata: metadata(), Status: "open", RemainingQuantity: amount("2"), Protection: &paper.ProtectionState{ProtectionConfig: paper.ProtectionConfig{StopPrice: "90"}, UpdatedAt: at}}
	o := paper.Observation{ID: "price", Instrument: p.Instrument, Source: "fixture", Price: amount("100"), ObservedAt: at, AvailableAt: at, Ready: true}
	stop := "95"
	r := paper.ProtectionAdjustment{RequestID: "adjust", PositionID: p.ID, At: at.Add(2 * time.Minute), StaleAfter: time.Minute, StopPrice: &stop}
	if _, err := paper.AdjustProtection(p, r, o); err == nil {
		t.Fatal("stale reference accepted")
	}
	r.At = at.Add(-time.Second)
	if _, err := paper.AdjustProtection(p, r, o); err == nil {
		t.Fatal("backdated adjustment accepted")
	}
	r.At = at
	stop = "95.001"
	if _, err := paper.AdjustProtection(p, r, o); err == nil {
		t.Fatal("off-tick stop accepted")
	}
	stop = ""
	updated, err := paper.AdjustProtection(p, r, o)
	if err != nil || updated.Protection.StopPrice != "" {
		t.Fatalf("explicit disable: %+v %v", updated, err)
	}
}
