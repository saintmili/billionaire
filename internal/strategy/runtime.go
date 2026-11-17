package strategy

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
)

type State struct {
	LastObserved  time.Time `json:"last_observed"`
	EntryTrue     bool      `json:"entry_true"`
	ExitTrue      bool      `json:"exit_true"`
	EntryCandle   time.Time `json:"entry_candle"`
	ExitCandle    time.Time `json:"exit_candle"`
	EntryEmission time.Time `json:"entry_emission"`
	ExitEmission  time.Time `json:"exit_emission"`
}
type Signal struct {
	ID               string            `json:"id"`
	StrategyID       string            `json:"strategy_id"`
	VersionID        string            `json:"version_id"`
	InstanceID       string            `json:"instance_id"`
	EventID          string            `json:"event_id"`
	Kind             string            `json:"kind"`
	Direction        Direction         `json:"direction"`
	Instrument       domain.Instrument `json:"instrument"`
	Timeframe        domain.Timeframe  `json:"timeframe"`
	ObservedAt       time.Time         `json:"observed_at"`
	SourceObservedAt time.Time         `json:"source_observed_at"`
	CandleOpen       time.Time         `json:"candle_open"`
	ProposedEntry    domain.Decimal    `json:"proposed_entry"`
	Stop             *domain.Decimal   `json:"stop,omitempty"`
	Target           *domain.Decimal   `json:"target,omitempty"`
	RiskPercent      string            `json:"risk_percent"`
	Action           Action            `json:"action"`
	SourceReady      bool              `json:"source_ready"`
	Reason           string            `json:"reason"`
	Evidence         map[string]string `json:"evidence"`
}
type Candidate struct {
	ID         string            `json:"id"`
	VersionID  string            `json:"version_id"`
	Kind       string            `json:"kind"`
	Instrument domain.Instrument `json:"instrument"`
	Timeframe  domain.Timeframe  `json:"timeframe"`
	ObservedAt time.Time         `json:"observed_at"`
	Emitted    bool              `json:"emitted"`
}

type Evaluation struct {
	Candidates []Candidate `json:"candidates"`
	EventID    string      `json:"event_id"`
	State      State       `json:"state"`
	Ready      bool        `json:"ready"`
	Reason     string      `json:"reason"`
	Signals    []Signal    `json:"signals"`
}

func Plan(registry *Registry, version Version, instance Instance, event marketdata.Event, history []domain.Candle, prior State) (Evaluation, error) {
	c := event.Candle
	result := Evaluation{State: prior, Signals: []Signal{}}
	if c == nil {
		return result, errors.New("strategy evaluation requires a candle event")
	}
	d, err := Prepare(version.Definition, registry)
	if err != nil {
		return result, err
	}
	if ContentHash(d) != version.ID || instance.VersionID != version.ID {
		return result, errors.New("strategy version identity mismatch")
	}
	if err := d.ValidateInstance(instance); err != nil {
		return result, err
	}
	if c.Instrument != instance.Instrument || c.Timeframe != d.Primary {
		return result, errors.New("event does not match strategy instance/primary timeframe")
	}
	result.EventID = marketdata.EventID(*c)
	at, err := EventTime(event)
	if err != nil {
		return result, err
	}
	snapshot, err := NewSnapshotAt(*c, history, d.Timeframes, at)
	if err != nil {
		return result, err
	}
	if !instance.Enabled {
		result.Reason = "instance disabled"
		return result, nil
	}
	if d.Evaluation == OnClose && !c.Closed {
		result.Reason = "awaiting candle close"
		return result, nil
	}
	if at.Before(prior.LastObserved) {
		result.Reason = "out-of-order evaluation skipped"
		return result, nil
	}
	result.State.LastObserved = at
	decision, err := Evaluate(registry, d, snapshot)
	if err != nil {
		return result, err
	}
	result.Ready = decision.Ready
	result.Reason = decision.Reason
	if !decision.Ready {
		return result, nil
	}
	cooldown, _ := time.ParseDuration(d.SignalCooldown)
	for _, kind := range []string{"entry", "exit"} {
		if (kind == "entry" && !decision.EntryReady) || (kind == "exit" && !decision.ExitReady) {
			continue
		}
		condition, previous, candle, last := decision.Entry, prior.EntryTrue, prior.EntryCandle, prior.EntryEmission
		if kind == "exit" {
			condition = decision.Exit
			previous = prior.ExitTrue
			candle = prior.ExitCandle
			last = prior.ExitEmission
		}
		emit := condition
		switch d.Emission {
		case OnTransition:
			emit = condition && !previous
		case OncePerCandle:
			emit = condition && !candle.Equal(c.OpenTime)
		}
		if emit && !last.IsZero() && at.Sub(last) < cooldown {
			emit = false
		}
		if kind == "entry" {
			result.State.EntryTrue = condition
		} else {
			result.State.ExitTrue = condition
		}
		id := fmt.Sprintf("%x", sha256.Sum256([]byte(instance.ID+":"+result.EventID+":"+kind+":"+string(d.Direction))))
		if condition {
			result.Candidates = append(result.Candidates, Candidate{ID: id, VersionID: version.ID, Kind: kind, Instrument: instance.Instrument, Timeframe: d.Primary, ObservedAt: at, Emitted: emit})
		}
		if !emit {
			continue
		}
		signal := Signal{ID: id, StrategyID: d.ID, VersionID: version.ID, InstanceID: instance.ID, EventID: result.EventID, Kind: kind, Direction: d.Direction, Instrument: instance.Instrument, Timeframe: d.Primary, ObservedAt: at, SourceObservedAt: c.ObservedAt.UTC(), CandleOpen: c.OpenTime, ProposedEntry: c.Close, RiskPercent: d.RiskPercent, Action: instance.Action, SourceReady: event.AllowsEntries && event.Kind != "recovered_candle", Reason: decision.Reason, Evidence: decision.Evidence}
		if kind == "exit" {
			signal.Reason = "exit rule matched"
		}
		if d.Implementation == "rules" && kind == "entry" {
			signal.Reason = "entry rule matched"
		}
		if kind == "entry" {
			stop, target := decision.Stop, decision.Target
			signal.ProposedEntry = decision.Price
			signal.Stop = &stop
			signal.Target = &target
			result.State.EntryCandle = c.OpenTime
			result.State.EntryEmission = at
		} else {
			result.State.ExitCandle = c.OpenTime
			result.State.ExitEmission = at
		}
		result.Signals = append(result.Signals, signal)
	}
	return result, nil
}

// EventJSON preserves the observed update for audit/replay; no order is created.
func EventJSON(event marketdata.Event) ([]byte, error) { return json.Marshal(event) }

// EventTime separates provider observation time from the instant that recovered
// pages become available. Partial/closed identity still uses the provider event.
func EventTime(event marketdata.Event) (time.Time, error) {
	if event.Candle == nil || event.Candle.ObservedAt.IsZero() {
		return time.Time{}, errors.New("event observation time is required")
	}
	if event.AvailableAt.IsZero() {
		return event.Candle.ObservedAt.UTC(), nil
	}
	if event.AvailableAt.Before(event.Candle.ObservedAt) {
		return time.Time{}, errors.New("event availability precedes observation")
	}
	return event.AvailableAt.UTC(), nil
}
