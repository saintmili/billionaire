package strategy

import (
	"errors"
	"fmt"

	"github.com/saintmili/billionaire/internal/domain"
)

type Decision struct {
	Ready, Entry, Exit  bool
	Reason              string
	Price, Stop, Target domain.Decimal
	Evidence            map[string]string
}
type builtin struct{ name string }

func (b builtin) Validate(d Definition) error {
	allowed := map[string]bool{"stop_offset": true, "reward_risk": true}
	if b.name == "breakout" || b.name == "support_resistance" {
		allowed["lookback"] = true
	}
	if b.name == "support_resistance" {
		for _, k := range []string{"pivot_left", "pivot_right", "zone_tolerance", "min_touches", "setup"} {
			allowed[k] = true
		}
	}
	for k := range d.Parameters {
		if !allowed[k] {
			return errors.New("strategy.parameters contains an unsupported parameter")
		}
	}
	if b.name == "breakout" {
		if _, err := IntParam(d, "lookback", 20, 1, 5000); err != nil {
			return err
		}
	}
	offset, err := DecimalParam(d, "stop_offset", "0")
	if err != nil {
		return err
	}
	if offset.IsNegative() {
		return errors.New("strategy.parameters.stop_offset must be nonnegative")
	}
	reward, err := DecimalParam(d, "reward_risk", "2")
	if err != nil {
		return err
	}
	if !reward.IsPositive() {
		return errors.New("strategy.parameters.reward_risk must be positive")
	}
	if b.name == "support_resistance" {
		lookback, err := IntParam(d, "lookback", 50, 4, 5000)
		if err != nil {
			return err
		}
		left, err := IntParam(d, "pivot_left", 2, 1, 100)
		if err != nil {
			return err
		}
		right, err := IntParam(d, "pivot_right", 2, 1, 100)
		if err != nil {
			return err
		}
		if lookback < left+right+1 {
			return errors.New("strategy.parameters.lookback must cover pivot confirmation")
		}
		if _, err := IntParam(d, "min_touches", 1, 1, lookback); err != nil {
			return err
		}
		tolerance, err := DecimalParam(d, "zone_tolerance", "0")
		if err != nil {
			return err
		}
		if tolerance.IsNegative() {
			return errors.New("strategy.parameters.zone_tolerance must be nonnegative")
		}
		if setup := d.Parameters["setup"]; setup != "" && setup != "rejection" && setup != "break" {
			return errors.New("strategy.parameters.setup must be rejection or break")
		}
	}
	if b.name == "rules" && d.Entry == nil {
		return errors.New("rules implementation requires entry")
	}
	return nil
}
func (b builtin) Evaluate(d Definition, s Snapshot) (Decision, error) {
	result := Decision{Ready: true, Reason: b.name, Evidence: map[string]string{}}
	if b.name == "rules" {
		result.Entry = true
	}
	if b.name == "breakout" {
		count, _ := IntParam(d, "lookback", 20, 1, 5000)
		bars, err := s.Need(d.Primary, count+1)
		if err != nil {
			return Decision{}, err
		}
		boundary := bars[0].High
		if d.Direction == Short {
			boundary = bars[0].Low
		}
		for _, c := range bars[:len(bars)-1] {
			if d.Direction == Long && c.High.GreaterThan(boundary) {
				boundary = c.High
			}
			if d.Direction == Short && c.Low.LessThan(boundary) {
				boundary = c.Low
			}
		}
		result.Entry = s.Event.Close.GreaterThan(boundary)
		if d.Direction == Short {
			result.Entry = s.Event.Close.LessThan(boundary)
		}
		result.Evidence["preceding_boundary"] = boundary.String()
		result.Evidence["lookback"] = fmt.Sprint(count)
	}
	if b.name == "engulfing" {
		if !s.Event.Closed {
			return result, nil
		}
		bars, err := s.Need(d.Primary, 2)
		if err != nil {
			return Decision{}, err
		}
		previous, current := bars[0], bars[1]
		result.Evidence["previous_open"] = previous.Open.String()
		result.Evidence["previous_close"] = previous.Close.String()
		result.Evidence["current_open"] = current.Open.String()
		if d.Direction == Long {
			result.Entry = previous.Close.LessThan(previous.Open) && current.Close.GreaterThan(current.Open) && !current.Open.GreaterThan(previous.Close) && !current.Close.LessThan(previous.Open) && (current.Open.LessThan(previous.Close) || current.Close.GreaterThan(previous.Open))
		} else {
			result.Entry = previous.Close.GreaterThan(previous.Open) && current.Close.LessThan(current.Open) && !current.Open.LessThan(previous.Close) && !current.Close.GreaterThan(previous.Open) && (current.Open.GreaterThan(previous.Close) || current.Close.LessThan(previous.Open))
		}
	}
	if b.name == "support_resistance" {
		return supportResistance(d, s)
	}
	return result, nil
}

func supportResistance(d Definition, s Snapshot) (Decision, error) {
	result := Decision{Ready: true, Reason: "confirmed pivot zone", Evidence: map[string]string{}}
	lookback, _ := IntParam(d, "lookback", 50, 4, 5000)
	left, _ := IntParam(d, "pivot_left", 2, 1, 100)
	right, _ := IntParam(d, "pivot_right", 2, 1, 100)
	minimum, _ := IntParam(d, "min_touches", 1, 1, lookback)
	tolerance, _ := DecimalParam(d, "zone_tolerance", "0")
	bars, err := s.Need(d.Primary, lookback)
	if err != nil {
		return result, err
	}
	completed := bars
	if !s.Event.Closed {
		completed = completed[:len(completed)-1]
	}
	breaking := d.Parameters["setup"] == "break"
	support := d.Direction == Long
	if breaking {
		support = !support
	}
	pivot := -1
	level := domain.Decimal{}
	for n := left; n+right < len(completed); n++ {
		candidate := completed[n].Low
		if !support {
			candidate = completed[n].High
		}
		confirmed := true
		for j := n - left; j <= n+right; j++ {
			if j == n {
				continue
			}
			other := completed[j].Low
			if !support {
				other = completed[j].High
			}
			if (support && !candidate.LessThan(other)) || (!support && !candidate.GreaterThan(other)) {
				confirmed = false
				break
			}
		}
		if confirmed {
			pivot = n
			level = candidate
		}
	}
	if pivot < 0 {
		return result, nil
	}
	touches := 0
	for _, c := range bars[pivot+1 : len(bars)-1] {
		extreme := c.Low
		if !support {
			extreme = c.High
		}
		if extreme.Sub(level).Abs().LessThanOrEqual(tolerance) {
			touches++
		}
	}
	current := s.Event
	if breaking {
		result.Entry = (d.Direction == Long && current.Close.GreaterThan(level.Add(tolerance))) || (d.Direction == Short && current.Close.LessThan(level.Sub(tolerance)))
	} else {
		extreme := current.Low
		if !support {
			extreme = current.High
		}
		touch := extreme.Sub(level).Abs().LessThanOrEqual(tolerance)
		if touch {
			touches++
		}
		result.Entry = touch && ((d.Direction == Long && current.Close.GreaterThan(level) && current.Close.GreaterThan(current.Open)) || (d.Direction == Short && current.Close.LessThan(level) && current.Close.LessThan(current.Open)))
	}
	result.Entry = result.Entry && touches >= minimum
	result.Evidence["zone"] = level.String()
	result.Evidence["touches"] = fmt.Sprint(touches)
	result.Evidence["confirmed_at"] = completed[pivot+right].EndTime.UTC().Format("2006-01-02T15:04:05Z07:00")
	return result, nil
}
