package strategy_test

import (
	"strings"
	"testing"
	"time"

	"github.com/saintmili/billionaire/internal/domain"
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
			if len(result.Candidates) != 1 || result.Candidates[0].Emitted != (want == 1) {
				t.Fatalf("lost satisfied candidate %s: %+v", tc.policy, result)
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

func TestUnavailableRulePreservesItsStateWithoutSuppressingOtherKind(t *testing.T) {
	registry := strategy.DefaultRegistry()
	bars := barsForBreakout(t)
	for _, unavailable := range []string{"entry", "exit"} {
		t.Run(unavailable, func(t *testing.T) {
			d, err := strategy.Parse(strings.NewReader(breakoutYAML), registry)
			if err != nil {
				t.Fatal(err)
			}
			d.Implementation = "rules"
			d.Parameters = map[string]string{}
			d.Timeframes = []domain.Timeframe{"1m", "4h"}
			ready := &strategy.Rule{Op: "gt", Left: strategy.Value{Field: "close"}, Right: strategy.Value{Constant: constant("10")}}
			missing := &strategy.Rule{Op: "gt", Left: strategy.Value{Field: "sma", Timeframe: "4h", Period: 2}, Right: strategy.Value{Constant: constant("10")}}
			d.Entry, d.Exit = ready, missing
			prior := strategy.State{ExitTrue: true}
			want := "entry"
			if unavailable == "entry" {
				d.Entry, d.Exit = missing, ready
				prior = strategy.State{EntryTrue: true}
				want = "exit"
			}
			d, err = strategy.Prepare(d, registry)
			if err != nil {
				t.Fatal(err)
			}
			version := strategy.Version{ID: strategy.ContentHash(d), Definition: d}
			instance := strategy.Instance{ID: "independent", VersionID: version.ID, Instrument: bars[20].Instrument, Account: "demo", Action: strategy.AlertOnly, Enabled: true}
			result, err := strategy.Plan(registry, version, instance, marketdata.Event{Kind: "candle", Candle: &bars[20]}, bars, prior)
			if err != nil || len(result.Signals) != 1 || result.Signals[0].Kind != want {
				t.Fatalf("unavailable %s suppressed ready %s: %+v %v", unavailable, want, result, err)
			}
			if !result.State.EntryTrue || !result.State.ExitTrue {
				t.Fatalf("unavailable kind lost transition state: %+v", result.State)
			}
		})
	}
}

type exitOnlyStrategy struct{}

func (exitOnlyStrategy) Validate(strategy.Definition) error { return nil }
func (exitOnlyStrategy) Requirements(d strategy.Definition) (map[domain.Timeframe]int, error) {
	return map[domain.Timeframe]int{d.Primary: 1}, nil
}
func (exitOnlyStrategy) Evaluate(_ strategy.Definition, s strategy.Snapshot) (strategy.Decision, error) {
	ten, _ := domain.ParseDecimal("10")
	return strategy.Decision{Ready: true, Exit: s.Event.Close.GreaterThan(ten), Reason: "compiled exit"}, nil
}

func TestRegisteredStrategyEmitsAndResetsExitWithoutDeclarativeTree(t *testing.T) {
	registry := strategy.DefaultRegistry()
	if err := registry.Register("exit_only", exitOnlyStrategy{}); err != nil {
		t.Fatal(err)
	}
	d, err := strategy.Parse(strings.NewReader(breakoutYAML), registry)
	if err != nil {
		t.Fatal(err)
	}
	d.Implementation = "exit_only"
	d.Parameters = map[string]string{}
	d, err = strategy.Prepare(d, registry)
	if err != nil {
		t.Fatal(err)
	}
	bars := barsForBreakout(t)
	version := strategy.Version{ID: strategy.ContentHash(d), Definition: d}
	instance := strategy.Instance{ID: "compiled", VersionID: version.ID, Instrument: bars[20].Instrument, Account: "demo", Action: strategy.AlertOnly, Enabled: true}
	state := strategy.State{}
	for n, want := range []int{1, 0, 1} {
		event := bars[20]
		if n == 1 {
			event = bars[21]
		}
		event.ObservedAt = bars[21].EndTime.Add(time.Duration(n) * time.Second)
		event = marketdata.Stamp(event)
		result, err := strategy.Plan(registry, version, instance, marketdata.Event{Kind: "candle", Candle: &event}, bars, state)
		if err != nil || len(result.Signals) != want {
			t.Fatalf("compiled exit event%d: %+v %v", n, result, err)
		}
		if want == 1 && result.Signals[0].Kind != "exit" {
			t.Fatal("wrong signal kind")
		}
		state = result.State
	}
}

func TestSignalCandidateEvidenceRemainsCausalWithFutureSuccessPrices(t *testing.T) {
	registry := strategy.DefaultRegistry()
	d, e := strategy.Parse(strings.NewReader(breakoutYAML), registry)
	if e != nil {
		t.Fatal(e)
	}
	bars := barsForBreakout(t)
	v := strategy.Version{ID: strategy.ContentHash(d), Definition: d}
	i := strategy.Instance{ID: "causal-report", VersionID: v.ID, Instrument: bars[20].Instrument, Account: "demo", Action: strategy.AlertOnly, Enabled: true}
	first, e := strategy.Plan(registry, v, i, marketdata.Event{Kind: "candle", Candle: &bars[20]}, bars, strategy.State{})
	if e != nil {
		t.Fatal(e)
	}
	// An enormous future target touch changes retrospective success evidence only.
	future := bars[21]
	future.High, _ = domain.ParseDecimal("1000000")
	future.Close = future.High
	future = marketdata.Stamp(future)
	changed := append(append([]domain.Candle{}, bars[:21]...), future)
	second, e := strategy.Plan(registry, v, i, marketdata.Event{Kind: "candle", Candle: &bars[20]}, changed, strategy.State{})
	if e != nil {
		t.Fatal(e)
	}
	if len(first.Signals) != 1 || len(first.Candidates) != 1 || first.Signals[0].ID != second.Signals[0].ID || first.Signals[0].ProposedEntry.String() != second.Signals[0].ProposedEntry.String() {
		t.Fatalf("future success prices changed generation: %+v %+v", first, second)
	}
}
