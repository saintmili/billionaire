package strategy_test

import (
	"strings"
	"testing"

	"github.com/saintmili/billionaire/internal/strategy"
)

const breakoutYAML = `schema_version: 1
id: breakout-20
name: Breakout 20
implementation: breakout
primary_timeframe: 1m
timeframes: [1m]
direction: long
evaluation: on_candle_close
emission: on_transition
parameters:
  lookback: "20"
  stop_offset: "0.1"
  reward_risk: "2"
`

func TestStrategyYAMLValidatesAndHashesImmutableContent(t *testing.T) {
	registry := strategy.DefaultRegistry()
	definition, err := strategy.Parse(strings.NewReader(breakoutYAML), registry)
	if err != nil {
		t.Fatal(err)
	}
	if definition.ID != "breakout-20" || definition.Parameters["lookback"] != "20" {
		t.Fatal("definition not decoded")
	}
	original := strategy.ContentHash(definition)
	definition.Parameters["lookback"] = "21"
	if strategy.ContentHash(definition) == original {
		t.Fatal("edited parameters did not change version identity")
	}
	for _, text := range []string{strings.Replace(breakoutYAML, "on_transition", "busy_loop", 1), strings.Replace(breakoutYAML, `lookback: "20"`, `lookback: "0"`, 1), strings.Replace(breakoutYAML, "implementation: breakout", "implementation: shell", 1), breakoutYAML + "api_key: sensitive-value\n"} {
		_, err := strategy.Parse(strings.NewReader(text), registry)
		if err == nil {
			t.Fatal("invalid strategy accepted")
		}
		if strings.Contains(err.Error(), "sensitive-value") {
			t.Fatal("submitted value disclosed")
		}
	}
}
