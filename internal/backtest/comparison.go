package backtest

import (
	"bytes"
	"encoding/json"
	"github.com/saintmili/billionaire/internal/analytics"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/strategy"
	"sort"
	"strings"
	"time"
)

// Snapshot is a read-only view of one retained run. A nil metrics object is
// unavailable evidence, never a zero-performance observation.
type Snapshot struct {
	Report            Report                   `json:"report"`
	Version           *strategy.Version        `json:"strategy_version"`
	Metrics           *analytics.Report        `json:"metrics"`
	EvidenceStatus    string                   `json:"evidence_status"`
	SignalStatus      string                   `json:"signal_status"`
	SignalReports     []analytics.SignalReport `json:"signal_reports"`
	RejectedDecisions *int                     `json:"rejected_decisions"`
	PendingOrders     *int                     `json:"pending_orders"`
	AmbiguousFills    *int                     `json:"ambiguous_fills"`
	Warnings          []string                 `json:"warnings"`
}
type Period struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}
type Difference struct {
	Field string `json:"field"`
	Left  any    `json:"left"`
	Right any    `json:"right"`
}
type Comparison struct {
	SchemaVersion      string       `json:"schema_version"`
	Left               Snapshot     `json:"left"`
	Right              Snapshot     `json:"right"`
	MatchedPeriod      *Period      `json:"matched_period"`
	DirectlyComparable bool         `json:"directly_comparable"`
	Differences        []Difference `json:"differences"`
	Warnings           []string     `json:"warnings"`
}

// Compare preserves whole-run metrics. The intersection is context only, not a
// recomputation of return on a selected overlapping sample.
func Compare(left, right Snapshot) Comparison {
	c := Comparison{SchemaVersion: "1", Left: left, Right: right, Differences: []Difference{}, Warnings: []string{"Metrics cover each original full run; matched_period is context only. No ranking, pooling, statistical significance or optimization is implied."}}
	from, to := left.Report.Config.From, left.Report.Config.To
	if right.Report.Config.From.After(from) {
		from = right.Report.Config.From
	}
	if right.Report.Config.To.Before(to) {
		to = right.Report.Config.To
	}
	if from.Before(to) {
		c.MatchedPeriod = &Period{from, to}
	}
	diff := func(field string, a, b any) {
		if !bytes.Equal(marshal(a), marshal(b)) {
			c.Differences = append(c.Differences, Difference{field, a, b})
		}
	}
	var walk func(string, any, any)
	walk = func(path string, a, b any) {
		am, ao := a.(map[string]any)
		bm, bo := b.(map[string]any)
		if ao && bo {
			keys := map[string]bool{}
			for k := range am {
				keys[k] = true
			}
			for k := range bm {
				keys[k] = true
			}
			ks := []string{}
			for k := range keys {
				ks = append(ks, k)
			}
			sort.Strings(ks)
			for _, k := range ks {
				walk(path+"."+k, am[k], bm[k])
			}
			return
		}
		as, aok := a.(string)
		bs, bok := b.(string)
		if aok && bok && (path == "config.initial_capital" || path == "config.fee_rate" || path == "config.slippage_bps" || strings.HasPrefix(path, "config.risk.") || strings.HasPrefix(path, "config.protection.") || strings.HasPrefix(path, "config.liquidation.")) {
			ad, ae := domain.ParseDecimal(as)
			bd, be := domain.ParseDecimal(bs)
			if ae == nil && be == nil && ad.Equal(bd) {
				return
			}
		}
		diff(path, a, b)
	}
	normalized := func(v any) any {
		var a any
		d := json.NewDecoder(bytes.NewReader(marshal(v)))
		d.UseNumber()
		_ = d.Decode(&a)
		return a
	}
	lc, rc := left.Report.Config, right.Report.Config
	lc.Timeframes = append([]domain.Timeframe{}, lc.Timeframes...)
	rc.Timeframes = append([]domain.Timeframe{}, rc.Timeframes...)
	sort.Slice(lc.Timeframes, func(i, j int) bool { return lc.Timeframes[i] < lc.Timeframes[j] })
	sort.Slice(rc.Timeframes, func(i, j int) bool { return rc.Timeframes[i] < rc.Timeframes[j] })
	walk("config", normalized(lc), normalized(rc))
	walk("manifest", normalized(left.Report.Manifest), normalized(right.Report.Manifest))
	diff("strategy_version", left.Version, right.Version)
	diff("signal_reports", left.SignalReports, right.SignalReports)
	c.DirectlyComparable = len(c.Differences) == 0 && left.EvidenceStatus == "verified" && right.EvidenceStatus == "verified" && left.Metrics != nil && right.Metrics != nil && left.Metrics.ValuationStatus == "complete" && right.Metrics.ValuationStatus == "complete"
	for _, s := range []Snapshot{left, right} {
		if s.EvidenceStatus != "verified" {
			c.Warnings = append(c.Warnings, s.Report.ID+": "+s.EvidenceStatus)
		}
		if strings.Contains(s.Report.Manifest.Build, "unavailable") || strings.Contains(s.Report.Manifest.Build, "unknown") || s.Report.Manifest.Build == "" {
			c.Warnings = append(c.Warnings, s.Report.ID+": build provenance incomplete")
			c.DirectlyComparable = false
		}
	}
	if len(c.Differences) > 0 {
		c.Warnings = append(c.Warnings, "Dataset, strategy, periods or assumptions differ; whole-run results are not directly comparable.")
	}
	return c
}
func marshal(v any) []byte { b, _ := json.Marshal(v); return b }
