package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/strategy"
)

func (s *Store) AddStrategyVersion(ctx context.Context, definition strategy.Definition, registry *strategy.Registry) (strategy.Version, error) {
	var version strategy.Version
	d, err := strategy.Prepare(definition, registry)
	if err != nil {
		return version, err
	}
	version.ID = strategy.ContentHash(d)
	version.Definition = d
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return version, safeError(ctx, ErrUnavailable)
	}
	defer rollback(tx)
	if _, err := tx.Exec(ctx, "INSERT INTO millionaire.strategy_definitions(id,name) VALUES($1,$2) ON CONFLICT DO NOTHING", d.ID, d.Name); err != nil {
		return version, safeError(ctx, ErrUnavailable)
	}
	var name string
	if err := tx.QueryRow(ctx, "SELECT name FROM millionaire.strategy_definitions WHERE id=$1 FOR UPDATE", d.ID).Scan(&name); err != nil {
		return version, safeError(ctx, ErrUnavailable)
	}
	var stored []byte
	err = tx.QueryRow(ctx, "SELECT number,definition,created_at FROM millionaire.strategy_versions WHERE id=$1", version.ID).Scan(&version.Number, &stored, &version.CreatedAt)
	if err == nil {
		if json.Unmarshal(stored, &version.Definition) != nil || strategy.ContentHash(version.Definition) != version.ID {
			return version, ErrSchema
		}
		return version, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return version, safeError(ctx, ErrUnavailable)
	}
	if err := tx.QueryRow(ctx, "SELECT COALESCE(MAX(number),0)+1 FROM millionaire.strategy_versions WHERE strategy_id=$1", d.ID).Scan(&version.Number); err != nil {
		return version, safeError(ctx, ErrUnavailable)
	}
	payload, err := json.Marshal(d)
	if err != nil {
		return version, errors.New("invalid strategy definition")
	}
	if err := tx.QueryRow(ctx, "INSERT INTO millionaire.strategy_versions(id,strategy_id,number,definition) VALUES($1,$2,$3,$4) RETURNING created_at", version.ID, d.ID, version.Number, payload).Scan(&version.CreatedAt); err != nil {
		return version, safeError(ctx, ErrUnavailable)
	}
	if _, err := tx.Exec(ctx, "UPDATE millionaire.strategy_definitions SET name=$2 WHERE id=$1", d.ID, d.Name); err != nil {
		return version, safeError(ctx, ErrUnavailable)
	}
	if err := tx.Commit(ctx); err != nil {
		return version, safeError(ctx, ErrUnavailable)
	}
	return version, nil
}
func (s *Store) StrategyVersion(ctx context.Context, id string) (strategy.Version, error) {
	version := strategy.Version{ID: id}
	var payload []byte
	err := s.pool.QueryRow(ctx, "SELECT number,definition,created_at FROM millionaire.strategy_versions WHERE id=$1", id).Scan(&version.Number, &payload, &version.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return version, errors.New("strategy version not found")
	}
	if err != nil {
		return version, safeError(ctx, ErrUnavailable)
	}
	if json.Unmarshal(payload, &version.Definition) != nil || strategy.ContentHash(version.Definition) != id {
		return version, ErrSchema
	}
	return version, nil
}
func (s *Store) StrategyVersions(ctx context.Context) ([]strategy.Version, error) {
	rows, err := s.pool.Query(ctx, "SELECT id,number,definition,created_at FROM millionaire.strategy_versions ORDER BY strategy_id,number")
	if err != nil {
		return nil, safeError(ctx, ErrUnavailable)
	}
	defer rows.Close()
	result := []strategy.Version{}
	for rows.Next() {
		var version strategy.Version
		var payload []byte
		if rows.Scan(&version.ID, &version.Number, &payload, &version.CreatedAt) != nil || json.Unmarshal(payload, &version.Definition) != nil || strategy.ContentHash(version.Definition) != version.ID {
			return nil, ErrSchema
		}
		result = append(result, version)
	}
	if rows.Err() != nil {
		return nil, safeError(ctx, ErrUnavailable)
	}
	return result, nil
}
func (s *Store) AddStrategyInstance(ctx context.Context, instance strategy.Instance) error {
	version, err := s.StrategyVersion(ctx, instance.VersionID)
	if err != nil {
		return err
	}
	if instance.Action == "" {
		instance.Action = version.Definition.Action
	}
	instance.Enabled = false
	if err := version.Definition.ValidateInstance(instance); err != nil {
		return err
	}
	if _, err := s.PaperAccount(ctx, instance.Account); err != nil {
		return errors.New("strategy instance requires an existing paper account")
	}
	payload, err := json.Marshal(instance)
	if err != nil {
		return errors.New("invalid strategy instance")
	}
	tag, err := s.pool.Exec(ctx, `INSERT INTO millionaire.strategy_instances(id,version_id,binding) VALUES($1,$2,$3)
 ON CONFLICT(id) DO UPDATE SET id=EXCLUDED.id WHERE strategy_instances.version_id=EXCLUDED.version_id AND strategy_instances.binding=EXCLUDED.binding`, instance.ID, instance.VersionID, payload)
	if err != nil {
		return safeError(ctx, ErrUnavailable)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("instance binding conflicts; use a new instance id")
	}
	return nil
}
func (s *Store) SetStrategyEnabled(ctx context.Context, id string, enabled bool) error {
	if enabled {
		instance, err := s.StrategyInstance(ctx, id)
		if err != nil {
			return err
		}
		if _, err := s.PaperAccount(ctx, instance.Account); err != nil {
			return errors.New("strategy instance requires an existing paper account; recreate legacy placeholder bindings")
		}
	}

	tag, err := s.pool.Exec(ctx, "UPDATE millionaire.strategy_instances SET enabled=$2 WHERE id=$1", id, enabled)
	if err != nil {
		return safeError(ctx, ErrUnavailable)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("strategy instance not found")
	}
	return nil
}
func (s *Store) StrategyInstances(ctx context.Context) ([]strategy.Instance, error) {
	rows, err := s.pool.Query(ctx, "SELECT binding,enabled FROM millionaire.strategy_instances ORDER BY id")
	if err != nil {
		return nil, safeError(ctx, ErrUnavailable)
	}
	defer rows.Close()
	result := []strategy.Instance{}
	for rows.Next() {
		var payload []byte
		var enabled bool
		var i strategy.Instance
		if rows.Scan(&payload, &enabled) != nil || json.Unmarshal(payload, &i) != nil {
			return nil, ErrSchema
		}
		i.Enabled = enabled
		result = append(result, i)
	}
	if rows.Err() != nil {
		return nil, safeError(ctx, ErrUnavailable)
	}
	return result, nil
}

func strategyHistory(ctx context.Context, tx pgx.Tx, i domain.Instrument, frames []domain.Timeframe, at time.Time, event domain.Candle) ([]domain.Candle, error) {
	history := []domain.Candle{}
	for _, frame := range frames {
		rows, err := tx.Query(ctx, `WITH candidates AS (
   SELECT source,provider,market,instrument_id,timeframe,open_time,revision,payload FROM millionaire.market_candle_revisions
   UNION ALL SELECT source,provider,market,instrument_id,timeframe,open_time,revision,payload FROM millionaire.market_candles
  ) SELECT payload FROM (
   SELECT DISTINCT ON(open_time) open_time,payload FROM candidates
   WHERE source=$1 AND provider=$1 AND market=$2 AND instrument_id=$3 AND timeframe=$4
    AND (payload->>'observed_at')::timestamptz<=$5 AND (payload->>'end_time')::timestamptz<=$5
    AND payload->'instrument'->>'base'=$6 AND payload->'instrument'->>'quote'=$7
    AND (timeframe<>$9 OR open_time<$10)
   ORDER BY open_time DESC,(payload->>'observed_at')::timestamptz DESC,revision
  ) known ORDER BY open_time DESC LIMIT $8`, i.Provider, i.Market, i.ID, frame, at, i.Base, i.Quote, strategy.MaxBars, event.Timeframe, event.OpenTime)
		if err != nil {
			return nil, safeError(ctx, ErrUnavailable)
		}
		bars, err := readCandles(ctx, rows)
		rows.Close()
		if err != nil {
			return nil, err
		}
		history = append(history, bars...)
	}
	return history, nil
}

// EvaluateStrategyEvent locks one instance, then commits dedup, state and signals
// together. Retrying after a lost acknowledgment cannot create a second signal.
func (s *Store) EvaluateStrategyEvent(ctx context.Context, id string, event marketdata.Event, registry *strategy.Registry) (strategy.Evaluation, error) {
	empty := strategy.Evaluation{Signals: []strategy.Signal{}}
	if event.Candle == nil {
		return empty, errors.New("strategy evaluation requires a candle")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return empty, safeError(ctx, ErrUnavailable)
	}
	defer rollback(tx)
	var binding, statePayload []byte
	var enabled bool
	var versionID string
	err = tx.QueryRow(ctx, "SELECT binding,enabled,version_id,runtime_state FROM millionaire.strategy_instances WHERE id=$1 FOR UPDATE", id).Scan(&binding, &enabled, &versionID, &statePayload)
	if errors.Is(err, pgx.ErrNoRows) {
		return empty, errors.New("strategy instance not found")
	}
	if err != nil {
		return empty, safeError(ctx, ErrUnavailable)
	}
	instance := strategy.Instance{}
	state := strategy.State{}
	if json.Unmarshal(binding, &instance) != nil || json.Unmarshal(statePayload, &state) != nil {
		return empty, ErrSchema
	}
	instance.Enabled = enabled
	eventID := marketdata.EventID(*event.Candle)
	var exists bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM millionaire.strategy_evaluations WHERE instance_id=$1 AND event_id=$2)", id, eventID).Scan(&exists); err != nil {
		return empty, safeError(ctx, ErrUnavailable)
	}
	if exists {
		empty.EventID = eventID
		empty.State = state
		empty.Reason = "duplicate event"
		return empty, nil
	}
	version := strategy.Version{ID: versionID}
	var definition []byte
	if err := tx.QueryRow(ctx, "SELECT number,definition,created_at FROM millionaire.strategy_versions WHERE id=$1", versionID).Scan(&version.Number, &definition, &version.CreatedAt); err != nil {
		return empty, safeError(ctx, ErrUnavailable)
	}
	if json.Unmarshal(definition, &version.Definition) != nil {
		return empty, ErrSchema
	}
	at, err := strategy.EventTime(event)
	if err != nil {
		return empty, err
	}
	history, err := strategyHistory(ctx, tx, instance.Instrument, version.Definition.Timeframes, at, *event.Candle)
	if err != nil {
		return empty, err
	}
	result, err := strategy.Plan(registry, version, instance, event, history, state)
	if err != nil {
		return empty, err
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return empty, errors.New("invalid strategy evaluation")
	}
	source, err := strategy.EventJSON(event)
	if err != nil {
		return empty, errors.New("invalid strategy event")
	}
	if _, err := tx.Exec(ctx, "INSERT INTO millionaire.strategy_evaluations(instance_id,event_id,observed_at,event,result) VALUES($1,$2,$3,$4,$5)", id, eventID, at, source, payload); err != nil {
		return empty, safeError(ctx, ErrUnavailable)
	}
	for _, signal := range result.Signals {
		data, err := json.Marshal(signal)
		if err != nil {
			return empty, errors.New("invalid signal")
		}
		if _, err := tx.Exec(ctx, "INSERT INTO millionaire.strategy_signals(id,instance_id,event_id,kind,direction,observed_at,payload) VALUES($1,$2,$3,$4,$5,$6,$7)", signal.ID, id, eventID, signal.Kind, signal.Direction, signal.ObservedAt, data); err != nil {
			return empty, safeError(ctx, ErrUnavailable)
		}
	}
	nextState, err := json.Marshal(result.State)
	if err != nil {
		return empty, errors.New("invalid evaluation state")
	}
	if _, err := tx.Exec(ctx, "UPDATE millionaire.strategy_instances SET runtime_state=$2 WHERE id=$1", id, nextState); err != nil {
		return empty, safeError(ctx, ErrUnavailable)
	}
	if err := tx.Commit(ctx); err != nil {
		return empty, safeError(ctx, ErrUnavailable)
	}
	return result, nil
}
func (s *Store) StrategySignals(ctx context.Context, filter strategy.SignalFilter) ([]strategy.Signal, error) {
	rows, err := s.pool.Query(ctx, "SELECT payload FROM millionaire.strategy_signals WHERE ($1='' OR instance_id=$1) AND ($2='' OR payload->>'strategy_id'=$2) ORDER BY observed_at,id LIMIT 10000", filter.InstanceID, filter.StrategyID)
	if err != nil {
		return nil, safeError(ctx, ErrUnavailable)
	}
	defer rows.Close()
	result := []strategy.Signal{}
	for rows.Next() {
		var data []byte
		var signal strategy.Signal
		if rows.Scan(&data) != nil || json.Unmarshal(data, &signal) != nil {
			return nil, ErrSchema
		}
		result = append(result, signal)
	}
	if rows.Err() != nil {
		return nil, safeError(ctx, ErrUnavailable)
	}
	return result, nil
}
func (s *Store) EvaluateEnabledStrategies(ctx context.Context, event marketdata.Event, registry *strategy.Registry) ([]strategy.Signal, error) {
	signals := []strategy.Signal{}
	if event.Candle == nil {
		return signals, nil
	}
	instances, err := s.StrategyInstances(ctx)
	if err != nil {
		return nil, err
	}
	for _, i := range instances {
		if !i.Enabled || i.Instrument != event.Candle.Instrument {
			continue
		}
		version, err := s.StrategyVersion(ctx, i.VersionID)
		if err != nil {
			return nil, err
		}
		if version.Definition.Primary != event.Candle.Timeframe || (version.Definition.Evaluation == strategy.OnClose && !event.Candle.Closed) {
			continue
		}
		result, err := s.EvaluateStrategyEvent(ctx, i.ID, event, registry)
		if err != nil {
			return nil, err
		}
		signals = append(signals, result.Signals...)
	}
	return signals, nil
}

func (s *Store) ActiveStrategyVersions(ctx context.Context, i domain.Instrument, frame domain.Timeframe) ([]strategy.Version, error) {
	instances, err := s.StrategyInstances(ctx)
	if err != nil {
		return nil, err
	}
	versions := []strategy.Version{}
	seen := map[string]bool{}
	for _, instance := range instances {
		if !instance.Enabled || instance.Instrument != i || seen[instance.VersionID] {
			continue
		}
		version, err := s.StrategyVersion(ctx, instance.VersionID)
		if err != nil {
			return nil, err
		}
		if version.Definition.Primary == frame {
			versions = append(versions, version)
			seen[version.ID] = true
		}
	}
	return versions, nil
}

func (s *Store) StrategyInstance(ctx context.Context, id string) (strategy.Instance, error) {
	var instance strategy.Instance
	var payload []byte
	var enabled bool
	err := s.pool.QueryRow(ctx, "SELECT binding,enabled FROM millionaire.strategy_instances WHERE id=$1", id).Scan(&payload, &enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return instance, errors.New("strategy instance not found")
	}
	if err != nil {
		return instance, safeError(ctx, ErrUnavailable)
	}
	if json.Unmarshal(payload, &instance) != nil {
		return instance, ErrSchema
	}
	instance.Enabled = enabled
	return instance, nil
}
