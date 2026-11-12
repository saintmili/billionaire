package strategy_test

import (
	"strings"
	"testing"
	"time"

	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/strategy"
)

func TestEmissionPoliciesAndCooldownRemainSeparateFromReplayDedup(t *testing.T) {
	registry := strategy.DefaultRegistry()
	d, err := strategy.Parse(strings.NewReader(breakoutYAML), registry)
	if err != nil {
		t.Fatal(err)
	}
	d.Evaluation = strategy.OnUpdate
	bars := barsForBreakout(t)
	first := bars[20]
	first.Closed = false
	first.ObservedAt = first.OpenTime.Add(10 * time.Second)
	first = marketdata.Stamp(first)
	for _, tc := range []struct {
		policy   strategy.EmissionMode
		cooldown string
		counts   []int
	}{{strategy.OnTransition, "0s", []int{1, 0, 0}}, {strategy.OncePerCandle, "0s", []int{1, 0, 0}}, {strategy.WhileTrue, "0s", []int{1, 1, 1}}, {strategy.WhileTrue, "2s", []int{1, 0, 1}}} {
		d.Emission = tc.policy
		d.SignalCooldown = tc.cooldown
		version := strategy.Version{ID: strategy.ContentHash(d), Definition: d}
		instance := strategy.Instance{ID: "demo", VersionID: version.ID, Instrument: first.Instrument, Account: "demo", Action: strategy.AlertOnly, Enabled: true}
		state := strategy.State{}
		for n, want := range tc.counts {
			event := first
			event.ObservedAt = first.ObservedAt.Add(time.Duration(n) * time.Second)
			event = marketdata.Stamp(event)
			result, err := strategy.Plan(registry, version, instance, marketdata.Event{Kind: "candle", Candle: &event}, bars, state)
			if err != nil || len(result.Signals) != want {
				t.Fatalf("%s cooldown %s event %d: %+v %v", tc.policy, tc.cooldown, n, result, err)
			}
			state = result.State
		}
	}
}
func TestEntryAndExitConditionsHaveIndependentTransitionState(t *testing.T) {
	registry := strategy.DefaultRegistry()
	d, err := strategy.Parse(strings.NewReader(breakoutYAML), registry)
	if err != nil {
		t.Fatal(err)
	}
	d.Implementation = "rules"
	d.Parameters = map[string]string{}
	d.Entry = &strategy.Rule{Op: "gt", Left: strategy.Value{Field: "close"}, Right: strategy.Value{Constant: constant("10")}}
	d.Exit = &strategy.Rule{Op: "lt", Left: strategy.Value{Field: "close"}, Right: strategy.Value{Constant: constant("10")}}
	d, err = strategy.Prepare(d, registry)
	if err != nil {
		t.Fatal(err)
	}
	bars := barsForBreakout(t)
	version := strategy.Version{ID: strategy.ContentHash(d), Definition: d}
	instance := strategy.Instance{ID: "demo", VersionID: version.ID, Instrument: bars[20].Instrument, Account: "demo", Action: strategy.AlertOnly, Enabled: true}
	first, err := strategy.Plan(registry, version, instance, marketdata.Event{Kind: "candle", Candle: &bars[20]}, bars, strategy.State{})
	if err != nil || len(first.Signals) != 1 || first.Signals[0].Kind != "entry" {
		t.Fatalf("entry: %+v %v", first, err)
	}
	second, err := strategy.Plan(registry, version, instance, marketdata.Event{Kind: "candle", Candle: &bars[21]}, bars, first.State)
	if err != nil || len(second.Signals) != 1 || second.Signals[0].Kind != "exit" || second.Signals[0].Stop != nil {
		t.Fatalf("exit: %+v %v", second, err)
	}
}

func TestRecoveryDeliveryClockPreservesChronologicalTransitions(t *testing.T) {
	registry := strategy.DefaultRegistry()
	d, err := strategy.Parse(strings.NewReader(breakoutYAML), registry)
	if err != nil {
		t.Fatal(err)
	}
	bars := barsForBreakout(t)
	base := bars[0].OpenTime
	first := bars[20]
	first.ObservedAt = base.Add(3 * time.Hour)
	first = marketdata.Stamp(first)
	second := bars[21]
	second.ObservedAt = base.Add(2 * time.Hour)
	second = marketdata.Stamp(second)
	version := strategy.Version{ID: strategy.ContentHash(d), Definition: d}
	instance := strategy.Instance{ID: "recovery-demo", VersionID: version.ID, Instrument: first.Instrument, Account: "demo", Action: strategy.AlertOnly, Enabled: true}
	one, err := strategy.Plan(registry, version, instance, marketdata.Event{Kind: "recovered_candle", Candle: &first, AvailableAt: base.Add(3 * time.Hour)}, bars, strategy.State{})
	if err != nil || len(one.Signals) != 1 {
		t.Fatalf("first recovery: %+v %v", one, err)
	}
	two, err := strategy.Plan(registry, version, instance, marketdata.Event{Kind: "recovered_candle", Candle: &second, AvailableAt: base.Add(3 * time.Hour)}, bars, one.State)
	if err != nil || !two.Ready || two.State.EntryTrue {
		t.Fatalf("reversed fetch timestamps skipped a chronological condition reset: %+v %v", two, err)
	}
}
