package domain

import (
	"errors"
	"regexp"
	"strconv"
	"time"
)

type MarketType string

const (
	Spot   MarketType = "spot"
	Linear MarketType = "linear"
)

type Instrument struct {
	Provider string     `json:"provider"`
	Market   MarketType `json:"market"`
	Base     string     `json:"base"`
	Quote    string     `json:"quote"`
	ID       string     `json:"instrument_id"`
}

var assetCode = regexp.MustCompile(`^[A-Z0-9]+$`)

func (i Instrument) Validate() error {
	if i.Provider == "" || i.ID == "" {
		return errors.New("instrument provider and instrument_id are required")
	}
	if i.Market != Spot && i.Market != Linear {
		return errors.New("instrument market must be spot or linear")
	}
	if !assetCode.MatchString(i.Base) || i.Quote != "USDT" || i.Base == i.Quote {
		return errors.New("instrument requires an uppercase base and USDT quote")
	}
	return nil
}

// Timeframe distinguishes calendar months (1M) from minutes (1m).
// Aggregation and provider capability resolution belong to the market adapter.
type Timeframe string

func ParseTimeframe(text string) (Timeframe, error) {
	if text == "1M" || text == "1w" {
		return Timeframe(text), nil
	}
	if len(text) < 2 {
		return "", errors.New("timeframe must be positive minutes, hours, days, 1w or 1M")
	}
	n, err := strconv.Atoi(text[:len(text)-1])
	if err != nil || n < 1 || strconv.Itoa(n) != text[:len(text)-1] {
		return "", errors.New("invalid timeframe count")
	}
	unit := text[len(text)-1:]
	minutes := 0
	switch unit {
	case "m":
		if n <= 43200 {
			minutes = n
		}
	case "h":
		if n <= 720 {
			minutes = n * 60
		}
	case "d":
		if n <= 30 {
			minutes = n * 1440
		}
	}
	if minutes == 0 {
		return "", errors.New("unsupported timeframe: use minutes/hours/days up to 30d, 1w or 1M")
	}
	return Timeframe(text), nil
}

// Bounds aligns fixed intervals to the UTC Unix epoch, weeks to Monday, and
// calendar months to their actual UTC boundaries. Call ParseTimeframe first.
func (f Timeframe) Bounds(at time.Time) (time.Time, time.Time) {
	at = at.UTC()
	if f == "1M" {
		start := time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(0, 1, 0)
	}
	if f == "1w" {
		day := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
		start := day.AddDate(0, 0, -(int(day.Weekday())+6)%7)
		return start, start.AddDate(0, 0, 7)
	}
	if _, err := ParseTimeframe(string(f)); err != nil {
		return time.Time{}, time.Time{}
	}
	n, _ := strconv.Atoi(string(f[:len(f)-1]))
	duration := time.Duration(n) * time.Minute
	switch f[len(f)-1] {
	case 'h':
		duration = time.Duration(n) * time.Hour
	case 'd':
		duration = time.Duration(n) * 24 * time.Hour
	}
	seconds := int64(duration / time.Second)
	start := time.Unix(at.Unix()-((at.Unix()%seconds+seconds)%seconds), 0).UTC()
	return start, start.Add(duration)
}
