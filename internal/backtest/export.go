package backtest

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"strconv"
)

// Export uses exact typed JSON rows for both encodings. CSV JSON cells retain
// nested provenance without lossy floating-point conversion or schema flattening.
type Export struct {
	SchemaVersion string   `json:"schema_version"`
	Kind          string   `json:"kind"`
	Context       Snapshot `json:"context"`
	Rows          []any    `json:"rows"`
}

func WriteExport(w io.Writer, format, kind string, s Snapshot) error {
	e := Export{SchemaVersion: "1", Kind: kind, Context: s, Rows: []any{}}
	switch kind {
	case "report":
		e.Rows = append(e.Rows, s)
	case "trades":
		if s.Metrics != nil {
			for _, t := range s.Metrics.Trades {
				e.Rows = append(e.Rows, t)
			}
		}
	case "equity":
		if s.Metrics != nil {
			for _, p := range s.Metrics.Equity {
				e.Rows = append(e.Rows, p)
			}
		}
	default:
		return errors.New("--kind must be report, trades or equity")
	}
	switch format {
	case "json":
		return json.NewEncoder(w).Encode(e)
	case "csv":
		c := csv.NewWriter(w)
		if err := c.Write([]string{"schema_version", "kind", "row_type", "sequence", "context_json", "row_json"}); err != nil {
			return err
		}
		context, err := json.Marshal(s)
		if err != nil {
			return err
		}
		if len(e.Rows) == 0 {
			if err = c.Write([]string{"1", kind, "context", "", string(context), "null"}); err != nil {
				return err
			}
		}
		for i, r := range e.Rows {
			row, err := json.Marshal(r)
			if err != nil {
				return err
			}
			if err = c.Write([]string{"1", kind, "data", strconv.Itoa(i), string(context), string(row)}); err != nil {
				return err
			}
		}
		c.Flush()
		return c.Error()
	default:
		return errors.New("--format must be json or csv")
	}
}
