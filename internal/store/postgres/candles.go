package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
)

func (s *Store) SaveInstrument(ctx context.Context, info marketdata.InstrumentInfo) error {
	if err := info.Validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(info)
	if err != nil {
		return errors.New("invalid instrument metadata")
	}
	i := info.Instrument
	tag, err := s.pool.Exec(ctx, `INSERT INTO billionaire.market_instruments(provider,market,instrument_id,base,quote,metadata) VALUES($1,$2,$3,$4,$5,$6)
 ON CONFLICT(provider,market,instrument_id) DO UPDATE SET metadata=EXCLUDED.metadata WHERE market_instruments.base=EXCLUDED.base AND market_instruments.quote=EXCLUDED.quote`, i.Provider, i.Market, i.ID, i.Base, i.Quote, payload)
	if err != nil {
		return safeError(ctx, ErrUnavailable)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("instrument identity conflicts with stored metadata")
	}
	return nil
}
func validateCandles(bars []domain.Candle) error {
	for _, c := range bars {
		if err := c.Validate(); err != nil {
			return err
		}
		start, end := c.Timeframe.Bounds(c.OpenTime)
		if !c.Closed || c.Source != c.Instrument.Provider || c.ObservedAt.IsZero() || !start.Equal(c.OpenTime) || !end.Equal(c.EndTime) || c.ObservedAt.Before(c.EndTime) || marketdata.Stamp(c).Revision != c.Revision {
			return errors.New("storage requires stamped, aligned closed candles with canonical source and observation time")
		}
	}
	return nil
}
func writeCandles(ctx context.Context, tx pgx.Tx, bars []domain.Candle) (int, error) {
	changed := 0
	for _, c := range bars {
		i := c.Instrument
		tag, err := tx.Exec(ctx, `INSERT INTO billionaire.market_instruments(provider,market,instrument_id,base,quote) VALUES($1,$2,$3,$4,$5)
   ON CONFLICT(provider,market,instrument_id) DO UPDATE SET base=EXCLUDED.base WHERE market_instruments.base=EXCLUDED.base AND market_instruments.quote=EXCLUDED.quote`, i.Provider, i.Market, i.ID, i.Base, i.Quote)
		if err != nil {
			return 0, safeError(ctx, ErrUnavailable)
		}
		if tag.RowsAffected() != 1 {
			return 0, errors.New("instrument identity conflicts with stored metadata")
		}
		payload, err := json.Marshal(c)
		if err != nil {
			return 0, errors.New("invalid candle payload")
		}
		_, err = tx.Exec(ctx, `INSERT INTO billionaire.market_candle_revisions VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING`, c.Source, i.Provider, i.Market, i.ID, c.Timeframe, c.OpenTime, c.Revision, payload)
		if err != nil {
			return 0, safeError(ctx, ErrUnavailable)
		}
		tag, err = tx.Exec(ctx, `INSERT INTO billionaire.market_candles(source,provider,market,instrument_id,timeframe,open_time,end_time,open,high,low,close,base_volume,revision,observed_at,payload)
   VALUES($1,$2,$3,$4,$5,$6,$7,$8::numeric,$9::numeric,$10::numeric,$11::numeric,$12::numeric,$13,$14,$15)
   ON CONFLICT(source,provider,market,instrument_id,timeframe,open_time) DO UPDATE SET end_time=EXCLUDED.end_time,open=EXCLUDED.open,high=EXCLUDED.high,low=EXCLUDED.low,close=EXCLUDED.close,base_volume=EXCLUDED.base_volume,revision=EXCLUDED.revision,observed_at=EXCLUDED.observed_at,payload=EXCLUDED.payload
   WHERE market_candles.observed_at<EXCLUDED.observed_at AND market_candles.revision<>EXCLUDED.revision`, c.Source, i.Provider, i.Market, i.ID, c.Timeframe, c.OpenTime, c.EndTime, c.Open.String(), c.High.String(), c.Low.String(), c.Close.String(), c.BaseVolume.String(), c.Revision, c.ObservedAt, payload)
		if err != nil {
			return 0, safeError(ctx, ErrUnavailable)
		}
		changed += int(tag.RowsAffected())
		// An identical observation is not a new candle event, but its timestamp is
		// still an ordering watermark. Keep that update under the same row lock.
		if tag.RowsAffected() == 0 {
			var observed time.Time
			var revision string
			err := tx.QueryRow(ctx, `SELECT observed_at,revision FROM billionaire.market_candles WHERE source=$1 AND provider=$2 AND market=$3 AND instrument_id=$4 AND timeframe=$5 AND open_time=$6`, c.Source, i.Provider, i.Market, i.ID, c.Timeframe, c.OpenTime).Scan(&observed, &revision)
			if err != nil {
				return 0, safeError(ctx, ErrUnavailable)
			}
			if observed.Equal(c.ObservedAt) && revision != c.Revision {
				return 0, errors.New("conflicting candle revisions at the same observation time")
			}
			if revision == c.Revision && c.ObservedAt.After(observed) {
				if _, err := tx.Exec(ctx, `UPDATE billionaire.market_candles SET observed_at=$7,payload=$8 WHERE source=$1 AND provider=$2 AND market=$3 AND instrument_id=$4 AND timeframe=$5 AND open_time=$6`, c.Source, i.Provider, i.Market, i.ID, c.Timeframe, c.OpenTime, c.ObservedAt, payload); err != nil {
					return 0, safeError(ctx, ErrUnavailable)
				}
			}
		}
	}
	return changed, nil
}
func (s *Store) SaveCandles(ctx context.Context, bars []domain.Candle) (int, error) {
	if err := validateCandles(bars); err != nil {
		return 0, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, safeError(ctx, ErrUnavailable)
	}
	defer rollback(tx)
	changed, err := writeCandles(ctx, tx, bars)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, safeError(ctx, ErrUnavailable)
	}
	return changed, nil
}

// SaveCandleEvent atomically records a canonical close/correction and its pending
// synchronous delivery. A timestamp cursor alone cannot retain old corrections.
// pending remains true on an uncertain commit so the caller retains it on exit.
func (s *Store) SaveCandleEvent(ctx context.Context, c domain.Candle, state marketdata.FeedState) (changed int, pending bool, err error) {
	if err := validateCandles([]domain.Candle{c}); err != nil {
		return 0, false, err
	}
	if state.Instrument != c.Instrument || state.Timeframe != c.Timeframe || state.PendingClosed != nil {
		return 0, false, errors.New("closed-event state does not match an idle delivery slot")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, false, safeError(ctx, ErrUnavailable)
	}
	defer rollback(tx)
	changed, err = writeCandles(ctx, tx, []domain.Candle{c})
	if err != nil {
		return 0, false, err
	}
	pending = changed > 0 || c.EndTime.After(state.LastDelivered)
	if pending {
		state.PendingClosed = &c
		if state.PendingAvailableAt.IsZero() {
			state.PendingAvailableAt = c.ObservedAt
		}
		if state.PendingAvailableAt.Before(c.ObservedAt) {
			return 0, pending, errors.New("pending availability precedes observation")
		}
		payload, err := marshalFeedState(state)
		if err != nil {
			return 0, pending, err
		}
		i := state.Instrument
		if _, err := tx.Exec(ctx, saveFeedSQL, i.Provider, i.Market, i.ID, state.Timeframe, payload); err != nil {
			return 0, pending, safeError(ctx, ErrUnavailable)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, pending, safeError(ctx, ErrUnavailable)
	}
	return changed, pending, nil
}
func (s *Store) Candles(ctx context.Context, q marketdata.Query) ([]domain.Candle, error) {
	if err := q.Validate(); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT payload FROM billionaire.market_candles WHERE source=$1 AND provider=$1 AND market=$2 AND instrument_id=$3 AND timeframe=$4 AND open_time>=$5 AND open_time<$6 AND payload->'instrument'->>'base'=$7 AND payload->'instrument'->>'quote'=$8 ORDER BY open_time`, q.Instrument.Provider, q.Instrument.Market, q.Instrument.ID, q.Timeframe, q.From, q.To, q.Instrument.Base, q.Instrument.Quote)
	if err != nil {
		return nil, safeError(ctx, ErrUnavailable)
	}
	defer rows.Close()
	return readCandles(ctx, rows)
}
func (s *Store) CandleRevisions(ctx context.Context, q marketdata.Query, at time.Time) ([]domain.Candle, error) {
	if err := q.Validate(); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT payload FROM billionaire.market_candle_revisions WHERE source=$1 AND provider=$1 AND market=$2 AND instrument_id=$3 AND timeframe=$4 AND open_time=$5 ORDER BY revision`, q.Instrument.Provider, q.Instrument.Market, q.Instrument.ID, q.Timeframe, at)
	if err != nil {
		return nil, safeError(ctx, ErrUnavailable)
	}
	defer rows.Close()
	return readCandles(ctx, rows)
}
func readCandles(ctx context.Context, rows pgx.Rows) ([]domain.Candle, error) {
	result := []domain.Candle{}
	for rows.Next() {
		var payload []byte
		var c domain.Candle
		if rows.Scan(&payload) != nil || json.Unmarshal(payload, &c) != nil {
			return nil, ErrSchema
		}
		result = append(result, c)
	}
	if rows.Err() != nil {
		return nil, safeError(ctx, ErrUnavailable)
	}
	return result, nil
}

const saveFeedSQL = `INSERT INTO billionaire.market_feed_state VALUES($1,$2,$3,$4,$5) ON CONFLICT(provider,market,instrument_id,timeframe) DO UPDATE SET payload=EXCLUDED.payload`

func marshalFeedState(state marketdata.FeedState) ([]byte, error) {
	if err := state.Instrument.Validate(); err != nil {
		return nil, err
	}
	if _, err := domain.ParseTimeframe(string(state.Timeframe)); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return nil, errors.New("invalid feed state")
	}
	return payload, nil
}
func (s *Store) SaveFeedState(ctx context.Context, state marketdata.FeedState) error {
	payload, err := marshalFeedState(state)
	if err != nil {
		return err
	}
	i := state.Instrument
	if _, err := s.pool.Exec(ctx, saveFeedSQL, i.Provider, i.Market, i.ID, state.Timeframe, payload); err != nil {
		return safeError(ctx, ErrUnavailable)
	}
	return nil
}
func (s *Store) FeedState(ctx context.Context, i domain.Instrument, frame domain.Timeframe) (marketdata.FeedState, error) {
	state := marketdata.FeedState{Instrument: i, Timeframe: frame, Reason: "no observations"}
	var payload []byte
	err := s.pool.QueryRow(ctx, `SELECT payload FROM billionaire.market_feed_state WHERE provider=$1 AND market=$2 AND instrument_id=$3 AND timeframe=$4`, i.Provider, i.Market, i.ID, frame).Scan(&payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return state, safeError(ctx, ErrUnavailable)
	}
	if json.Unmarshal(payload, &state) != nil {
		return state, ErrSchema
	}
	return state, nil
}
