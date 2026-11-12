// Package strategy owns immutable strategy definitions and deterministic evaluation.
package strategy

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/yamlutil"
)

type EvaluationMode string
type EmissionMode string
type Direction string
type Action string

const (
	OnClose       EvaluationMode = "on_candle_close"
	OnUpdate      EvaluationMode = "on_update"
	OnTransition  EmissionMode   = "on_transition"
	OncePerCandle EmissionMode   = "once_per_candle"
	WhileTrue     EmissionMode   = "while_true"
	Long          Direction      = "long"
	Short         Direction      = "short"
	AlertOnly     Action         = "alert_only"
	ManualPaper   Action         = "manual_paper"
	AutoPaper     Action         = "auto_paper"
)

type Definition struct {
	SchemaVersion  int                 `yaml:"schema_version" json:"schema_version"`
	ID             string              `yaml:"id" json:"id"`
	Name           string              `yaml:"name" json:"name"`
	Implementation string              `yaml:"implementation" json:"implementation"`
	Parameters     map[string]string   `yaml:"parameters" json:"parameters"`
	Primary        domain.Timeframe    `yaml:"primary_timeframe" json:"primary_timeframe"`
	Timeframes     []domain.Timeframe  `yaml:"timeframes" json:"timeframes"`
	Markets        []domain.MarketType `yaml:"markets" json:"markets"`
	Direction      Direction           `yaml:"direction" json:"direction"`
	Evaluation     EvaluationMode      `yaml:"evaluation" json:"evaluation"`
	Emission       EmissionMode        `yaml:"emission" json:"emission"`
	SignalCooldown string              `yaml:"signal_cooldown" json:"signal_cooldown"`
	RiskPercent    string              `yaml:"risk_percent" json:"risk_percent"`
	Action         Action              `yaml:"action" json:"action"`
	Entry          *Rule               `yaml:"entry" json:"entry,omitempty"`
	Exit           *Rule               `yaml:"exit" json:"exit,omitempty"`
}
type Value struct {
	Field     string           `yaml:"field" json:"field,omitempty"`
	Timeframe domain.Timeframe `yaml:"timeframe" json:"timeframe,omitempty"`
	Offset    int              `yaml:"offset" json:"offset,omitempty"`
	Period    int              `yaml:"period" json:"period,omitempty"`
	Constant  *string          `yaml:"constant" json:"constant,omitempty"`
}
type Rule struct {
	All   []Rule `yaml:"all" json:"all,omitempty"`
	Any   []Rule `yaml:"any" json:"any,omitempty"`
	Not   *Rule  `yaml:"not" json:"not,omitempty"`
	Op    string `yaml:"op" json:"op,omitempty"`
	Left  Value  `yaml:"left" json:"left,omitempty"`
	Right Value  `yaml:"right" json:"right,omitempty"`
}
type Version struct {
	ID         string     `json:"id"`
	Number     int        `json:"number"`
	Definition Definition `json:"definition"`
	CreatedAt  time.Time  `json:"created_at"`
}
type SignalFilter struct{ InstanceID, StrategyID string }

type Instance struct {
	ID         string            `json:"id"`
	VersionID  string            `json:"version_id"`
	Instrument domain.Instrument `json:"instrument"`
	Account    string            `json:"account"`
	Action     Action            `json:"action"`
	Enabled    bool              `json:"enabled"`
}

var identifier = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func ValidID(id string) bool    { return identifier.MatchString(id) }
func ValidAction(a Action) bool { return a == AlertOnly || a == ManualPaper || a == AutoPaper }
func ContentHash(d Definition) string {
	data, _ := json.Marshal(d)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
func Parse(input io.Reader, registry *Registry) (Definition, error) {
	var d Definition
	data, err := io.ReadAll(io.LimitReader(input, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return d, errors.New("strategy: unreadable or oversized YAML")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&d); err != nil {
		return d, yamlutil.DecodeError("strategy", data, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return d, errors.New("strategy: expected one YAML document")
	}
	return Prepare(d, registry)
}
func Prepare(d Definition, registry *Registry) (Definition, error) {
	// Copy all nested maps/rules so normalization never mutates an existing version.
	raw, err := json.Marshal(d)
	if err != nil {
		return d, errors.New("strategy: invalid definition")
	}
	var copied Definition
	if json.Unmarshal(raw, &copied) != nil {
		return d, errors.New("strategy: invalid definition")
	}
	d = copied
	if d.Primary == "" {
		d.Primary = "1m"
	}
	if len(d.Timeframes) == 0 {
		d.Timeframes = []domain.Timeframe{d.Primary}
	}
	if len(d.Markets) == 0 {
		d.Markets = []domain.MarketType{domain.Spot, domain.Linear}
	}
	if d.Direction == "" {
		d.Direction = Long
	}
	if d.Evaluation == "" {
		d.Evaluation = OnClose
	}
	if d.Emission == "" {
		d.Emission = OnTransition
	}
	if d.Action == "" {
		d.Action = AlertOnly
	}
	if d.SignalCooldown == "" {
		d.SignalCooldown = "0s"
	}
	if d.RiskPercent == "" {
		d.RiskPercent = "1"
	}
	if d.Parameters == nil {
		d.Parameters = map[string]string{}
	}
	if d.SchemaVersion != 1 {
		return d, errors.New("strategy.schema_version must be 1")
	}
	if !ValidID(d.ID) || strings.TrimSpace(d.Name) == "" || len(d.Name) > 128 {
		return d, errors.New("strategy.id/name are invalid")
	}
	if d.Direction != Long && d.Direction != Short {
		return d, errors.New("strategy.direction must be long or short")
	}
	if d.Evaluation != OnClose && d.Evaluation != OnUpdate {
		return d, errors.New("strategy.evaluation is invalid")
	}
	if d.Emission != OnTransition && d.Emission != OncePerCandle && d.Emission != WhileTrue {
		return d, errors.New("strategy.emission is invalid")
	}
	if !ValidAction(d.Action) {
		return d, errors.New("strategy.action is invalid")
	}
	if duration, err := time.ParseDuration(d.SignalCooldown); err != nil || duration < 0 {
		return d, errors.New("strategy.signal_cooldown must be nonnegative")
	}
	risk, err := domain.ParseDecimal(d.RiskPercent)
	hundred, _ := domain.ParseDecimal("100")
	if err != nil || !risk.IsPositive() || risk.GreaterThan(hundred) {
		return d, errors.New("strategy.risk_percent must be in (0,100]")
	}
	if len(d.Timeframes) > 8 {
		return d, errors.New("strategy.timeframes supports at most 8 intervals")
	}
	seen := map[domain.Timeframe]bool{}
	for _, frame := range d.Timeframes {
		if _, err := domain.ParseTimeframe(string(frame)); err != nil {
			return d, err
		}
		if seen[frame] {
			return d, errors.New("strategy.timeframes contains a duplicate")
		}
		seen[frame] = true
	}
	if !seen[d.Primary] {
		return d, errors.New("strategy.primary_timeframe must be in timeframes")
	}
	markets := map[domain.MarketType]bool{}
	for _, market := range d.Markets {
		if (market != domain.Spot && market != domain.Linear) || markets[market] {
			return d, errors.New("strategy.markets is invalid")
		}
		markets[market] = true
	}
	if d.Direction == Short && markets[domain.Spot] {
		return d, errors.New("short strategies must exclude spot markets")
	}
	nodes := 0
	for _, rule := range []*Rule{d.Entry, d.Exit} {
		if rule != nil {
			if err := validateRule(*rule, d, 0, &nodes); err != nil {
				return d, err
			}
		}
	}
	implementation, err := registry.Lookup(d.Implementation)
	if err != nil {
		return d, err
	}
	return d, implementation.Validate(d)
}
func (d Definition) ValidateInstance(i Instance) error {
	if !ValidID(i.ID) || i.VersionID == "" || !ValidID(i.Account) || !ValidAction(i.Action) {
		return errors.New("instance id/version/account/action are required and must be valid")
	}
	if err := i.Instrument.Validate(); err != nil {
		return err
	}
	allowed := false
	for _, market := range d.Markets {
		allowed = allowed || market == i.Instrument.Market
	}
	if !allowed {
		return errors.New("instance market is not supported by the version")
	}
	return nil
}
func IntParam(d Definition, key string, fallback, low, high int) (int, error) {
	text, ok := d.Parameters[key]
	if !ok {
		return fallback, nil
	}
	n, err := strconv.Atoi(text)
	if err != nil || n < low || n > high || strconv.Itoa(n) != text {
		return 0, fmt.Errorf("strategy.parameters.%s must be an integer in [%d,%d]", key, low, high)
	}
	return n, nil
}
func DecimalParam(d Definition, key, fallback string) (domain.Decimal, error) {
	text, ok := d.Parameters[key]
	if !ok {
		text = fallback
	}
	value, err := domain.ParseDecimal(text)
	if err != nil {
		return value, fmt.Errorf("strategy.parameters.%s must be a plain decimal", key)
	}
	return value, nil
}

type Strategy interface {
	Validate(Definition) error
	Evaluate(Definition, Snapshot) (Decision, error)
	Requirements(Definition) (map[domain.Timeframe]int, error)
}
type Registry struct {
	mu    sync.RWMutex
	items map[string]Strategy
}

func NewRegistry() *Registry { return &Registry{items: map[string]Strategy{}} }
func (r *Registry) Register(name string, implementation Strategy) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if name == "" || implementation == nil || r.items[name] != nil {
		return errors.New("strategy registration is invalid or duplicated")
	}
	r.items[name] = implementation
	return nil
}
func (r *Registry) Lookup(name string) (Strategy, error) {
	if r == nil {
		return nil, errors.New("strategy registry is required")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	item := r.items[name]
	if item == nil {
		return nil, errors.New("strategy.implementation is not registered")
	}
	return item, nil
}
func DefaultRegistry() *Registry {
	r := NewRegistry()
	for _, name := range []string{"breakout", "support_resistance", "engulfing", "rules"} {
		_ = r.Register(name, builtin{name})
	}
	return r
}
