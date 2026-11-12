package strategy

import (
	"errors"
	"fmt"

	"github.com/saintmili/billionaire/internal/domain"
)

func validateRule(rule Rule, d Definition, depth int, count *int) error {
	*count = *count + 1
	if depth > 8 || *count > 128 {
		return errors.New("strategy rules exceed depth/node limits")
	}
	variants := 0
	if len(rule.All) > 0 {
		variants++
	}
	if len(rule.Any) > 0 {
		variants++
	}
	if rule.Not != nil {
		variants++
	}
	if rule.Op != "" {
		variants++
	}
	if variants != 1 {
		return errors.New("strategy rule must select exactly one of all/any/not/op")
	}
	if rule.Op == "" {
		if rule.Left != (Value{}) || rule.Right != (Value{}) {
			return errors.New("strategy logical rule cannot contain comparison values")
		}
		children := append(append([]Rule{}, rule.All...), rule.Any...)
		if rule.Not != nil {
			children = append(children, *rule.Not)
		}
		for _, child := range children {
			if err := validateRule(child, d, depth+1, count); err != nil {
				return err
			}
		}
		return nil
	}
	switch rule.Op {
	case "gt", "gte", "lt", "lte", "eq", "crosses_above", "crosses_below":
	default:
		return errors.New("strategy rule.op is unsupported")
	}
	for _, value := range []Value{rule.Left, rule.Right} {
		if value.Constant != nil {
			if value.Field != "" || value.Timeframe != "" || value.Offset != 0 || value.Period != 0 {
				return errors.New("strategy constant cannot include field/timeframe/offset/period")
			}
			if _, err := domain.ParseDecimal(*value.Constant); err != nil {
				return errors.New("strategy rule constant must be a plain decimal")
			}
			continue
		}
		if value.Offset < 0 || value.Offset > 4999 {
			return errors.New("strategy rule offset must be 0..4999")
		}
		switch value.Field {
		case "open", "high", "low", "close", "volume":
			if value.Period != 0 {
				return errors.New("strategy rule period is only valid for SMA")
			}
		case "sma":
			if value.Period < 1 || value.Period+value.Offset > 5000 {
				return errors.New("strategy SMA period plus offset must be 1..5000")
			}
		default:
			return errors.New("strategy rule field is unsupported")
		}
		frame := value.Timeframe
		if frame == "" {
			frame = d.Primary
		}
		found := false
		for _, f := range d.Timeframes {
			found = found || f == frame
		}
		if !found {
			return errors.New("strategy rule timeframe is not selected")
		}
	}
	return nil
}
func valueAt(v Value, d Definition, s Snapshot, shift int) (domain.Decimal, error) {
	if v.Constant != nil {
		return domain.ParseDecimal(*v.Constant)
	}
	frame := v.Timeframe
	if frame == "" {
		frame = d.Primary
	}
	period := 1
	if v.Field == "sma" {
		period = v.Period
	}
	bars, err := s.Need(frame, v.Offset+shift+period)
	if err != nil {
		return domain.Decimal{}, err
	}
	last := len(bars) - 1 - v.Offset - shift
	switch v.Field {
	case "open":
		return bars[last].Open, nil
	case "high":
		return bars[last].High, nil
	case "low":
		return bars[last].Low, nil
	case "close":
		return bars[last].Close, nil
	case "volume":
		return bars[last].BaseVolume, nil
	case "sma":
		sum := domain.Decimal{}
		for _, c := range bars[last-period+1 : last+1] {
			sum = sum.Add(c.Close)
		}
		denominator, _ := domain.ParseDecimal(fmt.Sprint(period))
		return sum.DivRound(denominator, 18), nil
	}
	return domain.Decimal{}, errors.New("unsupported rule field")
}
func evaluateRule(r Rule, d Definition, s Snapshot, trace map[string]string, path string) (bool, error) {
	// Evaluate every child so an OR branch cannot hide insufficient warm-up.
	if r.Op == "" {
		if r.Not != nil {
			match, err := evaluateRule(*r.Not, d, s, trace, path+".not")
			return !match, err
		}
		children := r.All
		result := true
		any := len(r.Any) > 0
		if any {
			children = r.Any
			result = false
		}
		for index, child := range children {
			match, err := evaluateRule(child, d, s, trace, fmt.Sprintf("%s.%d", path, index))
			if err != nil {
				return false, err
			}
			if any {
				result = result || match
			} else {
				result = result && match
			}
		}
		return result, nil
	}
	left, err := valueAt(r.Left, d, s, 0)
	if err != nil {
		return false, err
	}
	right, err := valueAt(r.Right, d, s, 0)
	if err != nil {
		return false, err
	}
	trace[path+".left"] = left.String()
	trace[path+".right"] = right.String()
	trace[path+".operator"] = r.Op
	switch r.Op {
	case "gt":
		return left.GreaterThan(right), nil
	case "gte":
		return !left.LessThan(right), nil
	case "lt":
		return left.LessThan(right), nil
	case "lte":
		return !left.GreaterThan(right), nil
	case "eq":
		return left.Equal(right), nil
	case "crosses_above", "crosses_below":
		priorLeft, err := valueAt(r.Left, d, s, 1)
		if err != nil {
			return false, err
		}
		priorRight, err := valueAt(r.Right, d, s, 1)
		if err != nil {
			return false, err
		}
		trace[path+".previous_left"] = priorLeft.String()
		trace[path+".previous_right"] = priorRight.String()
		if r.Op == "crosses_above" {
			return left.GreaterThan(right) && !priorLeft.GreaterThan(priorRight), nil
		}
		return left.LessThan(right) && !priorLeft.LessThan(priorRight), nil
	}
	return false, errors.New("unsupported rule operation")
}
