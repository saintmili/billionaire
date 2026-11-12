// candle-spike verifies one fixed historical Bybit candle. It is an M0 tool,
// not the application CLI or the M2 market-data adapter.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const sampleStart int64 = 1670601600000

type response struct {
	Code   *int  `json:"retCode"`
	Time   int64 `json:"time"`
	Result struct {
		Category string     `json:"category"`
		Symbol   string     `json:"symbol"`
		List     [][]string `json:"list"`
	} `json:"result"`
}

type candle struct {
	Provider   string    `json:"provider"`
	Market     string    `json:"market"`
	Symbol     string    `json:"symbol"`
	Timeframe  string    `json:"timeframe"`
	OpenTime   time.Time `json:"open_time"`
	EndTime    time.Time `json:"end_time"`
	Open       string    `json:"open"`
	High       string    `json:"high"`
	Low        string    `json:"low"`
	Close      string    `json:"close"`
	BaseVolume string    `json:"base_volume"`
	Closed     bool      `json:"closed"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "candle-spike:", err)
		os.Exit(1)
	}
}

func run(args []string, out, diagnostics io.Writer) error {
	flags := flag.NewFlagSet("candle-spike", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	baseURL := flags.String("base-url", "https://api.bybit.com", "public REST origin (HTTPS; HTTP allowed for loopback tests)")
	fixture := flags.String("fixture", "", "read a Bybit JSON response offline")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	input, err := openResponse(*fixture, *baseURL)
	if err != nil {
		return err
	}
	defer input.Close()
	var payload response
	decoder := json.NewDecoder(io.LimitReader(input, 1<<20))
	if err := decoder.Decode(&payload); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("response must contain one JSON document")
	}
	if payload.Code == nil {
		return errors.New("response is missing retCode")
	}
	if *payload.Code != 0 {
		return fmt.Errorf("provider error: retCode=%d", *payload.Code)
	}
	if payload.Result.Category != "spot" || payload.Result.Symbol != "BTCUSDT" {
		return errors.New("response instrument does not match request")
	}
	if len(payload.Result.List) != 1 || len(payload.Result.List[0]) != 7 {
		return errors.New("expected exactly one seven-field candle")
	}
	row := payload.Result.List[0]
	start, err := strconv.ParseInt(row[0], 10, 64)
	if err != nil || start != sampleStart {
		return errors.New("response does not contain the requested historical candle")
	}
	if payload.Time < start+60000 {
		return errors.New("response candle is not yet closed at provider time")
	}
	digits := regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)
	for i, value := range row[1:] {
		if !digits.MatchString(value) {
			return errors.New("candle values must be nonnegative plain decimals")
		}
		if i < 4 && !strings.ContainsAny(value, "123456789") {
			return errors.New("candle prices must be positive")
		}
	}
	result := candle{
		Provider: "bybit", Market: payload.Result.Category, Symbol: payload.Result.Symbol, Timeframe: "1m",
		OpenTime: time.UnixMilli(start).UTC(), EndTime: time.UnixMilli(start + 60000).UTC(),
		Open: row[1], High: row[2], Low: row[3], Close: row[4], BaseVolume: row[5], Closed: payload.Time >= start+60000,
	}
	return json.NewEncoder(out).Encode(result)
}

func openResponse(fixture, baseURL string) (io.ReadCloser, error) {
	if fixture != "" {
		return os.Open(fixture)
	}
	origin, err := url.Parse(baseURL)
	if err != nil {
		return nil, errors.New("invalid public REST origin")
	}
	loopback := origin.Hostname() == "127.0.0.1" || origin.Hostname() == "localhost" || origin.Hostname() == "::1"
	if origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || (origin.Path != "" && origin.Path != "/") || (origin.Scheme != "https" && !(origin.Scheme == "http" && loopback)) {
		return nil, errors.New("base-url must be an HTTPS origin without credentials, path, query or fragment")
	}
	origin.Path = "/v5/market/kline"
	origin.RawQuery = url.Values{
		"category": {"spot"}, "symbol": {"BTCUSDT"}, "interval": {"1"},
		"start": {"1670601600000"}, "end": {"1670601659999"}, "limit": {"1"},
	}.Encode()
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(origin.String())
	if err != nil {
		return nil, fmt.Errorf("public candle request failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("public candle request: HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}
