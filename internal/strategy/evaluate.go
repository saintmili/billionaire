package strategy

import (
	"errors"
)

func Evaluate(registry *Registry, d Definition, s Snapshot) (Decision, error) {
	normalized, validationErr := Prepare(d, registry)
	if validationErr != nil {
		return Decision{}, validationErr
	}
	d = normalized
	if s.frames == nil || s.Event.Timeframe != d.Primary {
		return Decision{}, errors.New("snapshot does not match the primary timeframe")
	}
	allowed := false
	for _, market := range d.Markets {
		allowed = allowed || s.Event.Instrument.Market == market
	}
	if !allowed {
		return Decision{}, errors.New("strategy does not support the event market")
	}
	if d.Evaluation == OnClose && !s.Event.Closed {
		return Decision{Reason: "awaiting candle close"}, nil
	}
	implementation, err := registry.Lookup(d.Implementation)
	if err != nil {
		return Decision{}, err
	}
	result, err := implementation.Evaluate(d, s)
	if errors.Is(err, ErrWarmup) {
		return Decision{Reason: ErrWarmup.Error()}, nil
	}
	if err != nil {
		return result, err
	}
	if result.Evidence == nil {
		result.Evidence = map[string]string{}
	}
	result.Evidence["observed_close"] = s.Event.Close.String()
	if d.Entry != nil {
		matches, err := evaluateRule(*d.Entry, d, s, result.Evidence, "entry")
		if errors.Is(err, ErrWarmup) {
			return Decision{Reason: ErrWarmup.Error()}, nil
		}
		if err != nil {
			return result, err
		}
		result.Entry = result.Entry && matches
	}
	if d.Exit != nil {
		matches, err := evaluateRule(*d.Exit, d, s, result.Evidence, "exit")
		if errors.Is(err, ErrWarmup) {
			return Decision{Reason: ErrWarmup.Error()}, nil
		}
		if err != nil {
			return result, err
		}
		result.Exit = matches
	}
	if result.Entry {
		offset, _ := DecimalParam(d, "stop_offset", "0")
		reward, _ := DecimalParam(d, "reward_risk", "2")
		result.Price = s.Event.Close
		if d.Direction == Long {
			result.Stop = s.Event.Low.Sub(offset)
			result.Target = result.Price.Add(result.Price.Sub(result.Stop).Mul(reward))
		} else {
			result.Stop = s.Event.High.Add(offset)
			result.Target = result.Price.Sub(result.Stop.Sub(result.Price).Mul(reward))
		}
		if !result.Stop.IsPositive() || !result.Target.IsPositive() || (d.Direction == Long && !result.Price.GreaterThan(result.Stop)) || (d.Direction == Short && !result.Stop.GreaterThan(result.Price)) {
			result.Entry = false
			result.Reason = "invalid stop/entry geometry"
		}
	}
	return result, nil
}
