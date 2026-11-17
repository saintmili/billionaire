package analytics

import (
	"errors"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/strategy"
	"github.com/shopspring/decimal"
	"sort"
	"time"
)

type SuccessRule struct {
	ThresholdPercent string `json:"threshold_percent"`
	StopPercent      string `json:"stop_percent"`
	WindowBars       int    `json:"window_bars"`
	Basis            string `json:"observation_basis"`
}

func (r SuccessRule) Validate() error {
	target, e := domain.ParseDecimal(r.ThresholdPercent)
	if e != nil || !target.IsPositive() {
		return errors.New("positive threshold percent required")
	}
	stop, e := domain.ParseDecimal(r.StopPercent)
	if e != nil || !stop.IsPositive() || !stop.LessThan(decimal.NewFromInt(100)) {
		return errors.New("stop percent must be greater than zero and below 100")
	}
	if r.WindowBars < 1 || r.WindowBars > 100000 || r.Basis != "subsequent-closed-ohlcv" {
		return errors.New("success rule requires 1..100000 bars and subsequent-closed-ohlcv basis")
	}
	return nil
}

type SignalFilter struct {
	Filter
	Timeframe domain.Timeframe `json:"timeframe,omitempty"`
}
type SignalInput struct {
	Scope               string
	Signals             []strategy.Signal
	Candidates          []strategy.Candidate
	CandidatesAvailable bool
	Candles             []domain.Candle
	Fills               []paper.Fill
	Orders              []paper.Order
	Through             time.Time
}
type SignalGroup struct {
	Instrument       domain.Instrument `json:"instrument"`
	VersionID        string            `json:"strategy_version"`
	Timeframe        domain.Timeframe  `json:"timeframe"`
	Generated        int               `json:"distinct_generated"`
	Candidates       int               `json:"satisfied_candidates"`
	Suppressed       int               `json:"suppressed"`
	SuppressionRate  *domain.Decimal   `json:"suppression_rate"`
	Entries          int               `json:"signals_with_filled_entry"`
	ConversionRate   *domain.Decimal   `json:"signal_to_entry_conversion"`
	IntervalsSeconds []domain.Decimal  `json:"intervals_seconds"`
}
type SignalOutcome struct {
	SignalID     string `json:"signal_id"`
	Status       string `json:"status"`
	ObservedBars int    `json:"observed_bars"`
}
type SignalReport struct {
	ID           string          `json:"id"`
	RunID        string          `json:"run_id,omitempty"`
	Through      time.Time       `json:"observations_through"`
	EvidenceHash string          `json:"evidence_hash"`
	Scope        string          `json:"scope"`
	Filter       SignalFilter    `json:"filter"`
	Rule         SuccessRule     `json:"success_rule"`
	Groups       []SignalGroup   `json:"groups"`
	Outcomes     []SignalOutcome `json:"outcomes"`
	Warnings     []string        `json:"warnings"`
}

func Signals(in SignalInput, f SignalFilter, rule SuccessRule) (SignalReport, error) {
	r := SignalReport{Scope: in.Scope, Filter: f, Rule: rule, Groups: []SignalGroup{}, Outcomes: []SignalOutcome{}, Warnings: []string{}}
	if in.Scope != "backtest" && in.Scope != "forward-paper" {
		return r, errors.New("explicit backtest or forward-paper scope required")
	}
	if err := f.Validate(); err != nil {
		return r, err
	}
	if err := rule.Validate(); err != nil {
		return r, err
	}
	if f.Timeframe != "" {
		if _, e := domain.ParseTimeframe(string(f.Timeframe)); e != nil {
			return r, e
		}
	}
	matches := func(i domain.Instrument, v string, tf domain.Timeframe, at time.Time) bool {
		return (f.Instrument == nil || *f.Instrument == i) && (f.StrategyVersion == "" || f.StrategyVersion == v) && (f.Timeframe == "" || f.Timeframe == tf) && (f.From.IsZero() || !at.Before(f.From)) && (f.To.IsZero() || at.Before(f.To))
	}
	key := func(i domain.Instrument, v string, tf domain.Timeframe) string {
		return i.Provider + "/" + string(i.Market) + "/" + i.ID + "/" + v + "/" + string(tf)
	}
	groups := map[string]*SignalGroup{}
	times := map[string][]time.Time{}
	get := func(i domain.Instrument, v string, tf domain.Timeframe) *SignalGroup {
		k := key(i, v, tf)
		if groups[k] == nil {
			groups[k] = &SignalGroup{Instrument: i, VersionID: v, Timeframe: tf, IntervalsSeconds: []domain.Decimal{}}
		}
		return groups[k]
	}
	seen := map[string]bool{}
	for _, c := range in.Candidates {
		if c.Kind != "entry" || seen[c.ID] || !matches(c.Instrument, c.VersionID, c.Timeframe, c.ObservedAt) {
			continue
		}
		seen[c.ID] = true
		g := get(c.Instrument, c.VersionID, c.Timeframe)
		g.Candidates++
		if !c.Emitted {
			g.Suppressed++
		}
	}
	filledOrders := map[string]bool{}
	for _, fill := range in.Fills {
		filledOrders[fill.OrderID] = true
	}
	converted := map[string]bool{}
	for _, o := range in.Orders {
		if o.Request.Action == "open" && filledOrders[o.ID] {
			converted[o.Request.SignalID] = true
		}
	}
	seen = map[string]bool{}
	for _, s := range in.Signals {
		if s.Kind != "entry" || seen[s.ID] || !matches(s.Instrument, s.VersionID, s.Timeframe, s.ObservedAt) {
			continue
		}
		seen[s.ID] = true
		g := get(s.Instrument, s.VersionID, s.Timeframe)
		g.Generated++
		if converted[s.ID] {
			g.Entries++
		}
		k := key(s.Instrument, s.VersionID, s.Timeframe)
		times[k] = append(times[k], s.ObservedAt)
		r.Outcomes = append(r.Outcomes, observe(s, in.Candles, in.Through, rule))
	}
	keys := []string{}
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		g := groups[k]
		if g.Candidates > 0 && in.CandidatesAvailable {
			v := decimal.NewFromInt(int64(g.Suppressed)).Div(decimal.NewFromInt(int64(g.Candidates)))
			g.SuppressionRate = &v
		}
		if g.Generated > 0 {
			v := decimal.NewFromInt(int64(g.Entries)).Div(decimal.NewFromInt(int64(g.Generated)))
			g.ConversionRate = &v
		}
		ts := times[k]
		sort.Slice(ts, func(i, j int) bool { return ts[i].Before(ts[j]) })
		for i := 1; i < len(ts); i++ {
			g.IntervalsSeconds = append(g.IntervalsSeconds, decimal.NewFromInt(ts[i].Sub(ts[i-1]).Nanoseconds()).Div(decimal.NewFromInt(1e9)))
		}
		r.Groups = append(r.Groups, *g)
	}
	if !in.CandidatesAvailable {
		r.Warnings = append(r.Warnings, "Satisfied-candidate evidence unavailable; suppression rate is unknown")
	}
	r.Warnings = append(r.Warnings, "Entry candidates only; suppression denominator is distinct ready satisfied evaluation events before emission policy/cooldown; conversion requires an actual entry fill", "Hypothetical outcomes use proposed signal price, subsequent full closed bars and percent thresholds; independent of paper profit and entry rejection")
	sort.Slice(r.Outcomes, func(i, j int) bool { return r.Outcomes[i].SignalID < r.Outcomes[j].SignalID })
	return r, nil
}
func observe(s strategy.Signal, candles []domain.Candle, through time.Time, r SuccessRule) SignalOutcome {
	out := SignalOutcome{SignalID: s.ID, Status: "incomplete"}
	if !s.ProposedEntry.IsPositive() {
		out.Status = "missing-data"
		return out
	}
	pct, _ := domain.ParseDecimal(r.ThresholdPercent)
	stop, _ := domain.ParseDecimal(r.StopPercent)
	hundred := decimal.NewFromInt(100)
	up := s.ProposedEntry.Mul(decimal.NewFromInt(1).Add(pct.Div(hundred)))
	down := s.ProposedEntry.Mul(decimal.NewFromInt(1).Sub(stop.Div(hundred)))
	if s.Direction == strategy.Short {
		up = s.ProposedEntry.Mul(decimal.NewFromInt(1).Add(stop.Div(hundred)))
		down = s.ProposedEntry.Mul(decimal.NewFromInt(1).Sub(pct.Div(hundred)))
	}
	_, expected := s.Timeframe.Bounds(s.ObservedAt)
	if start, _ := s.Timeframe.Bounds(s.ObservedAt); start.Equal(s.ObservedAt) {
		expected = start
	}
	if !s.CandleOpen.IsZero() {
		_, afterSignalCandle := s.Timeframe.Bounds(s.CandleOpen)
		if expected.Before(afterSignalCandle) {
			expected = afterSignalCandle
		}
	}
	bars := map[time.Time]domain.Candle{}
	for _, c := range candles {
		if c.Instrument == s.Instrument && c.Timeframe == s.Timeframe && c.Closed && !c.OpenTime.Before(expected) && !c.EndTime.After(through) {
			old, ok := bars[c.OpenTime]
			if !ok || c.ObservedAt.After(old.ObservedAt) || (c.ObservedAt.Equal(old.ObservedAt) && c.Revision > old.Revision) {
				bars[c.OpenTime] = c
			}
		}
	}
	for i := 0; i < r.WindowBars; i++ {
		_, end := s.Timeframe.Bounds(expected)
		if end.After(through) {
			return out
		}
		c, ok := bars[expected]
		if !ok {
			out.Status = "missing-data"
			return out
		}
		out.ObservedBars++
		hi := !c.High.LessThan(up)
		lo := !c.Low.GreaterThan(down)
		if hi && lo {
			out.Status = "ambiguous"
			return out
		}
		if hi || lo {
			out.Status = "failure"
			if (s.Direction == strategy.Long && hi) || (s.Direction == strategy.Short && lo) {
				out.Status = "success"
			}
			return out
		}
		expected = end
	}
	out.Status = "failure"
	return out
}
