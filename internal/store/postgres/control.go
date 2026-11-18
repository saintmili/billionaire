package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/strategy"
	"regexp"
)

var controlID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

// Control joins nested financial transactions and the immutable receipt in one
// transaction on the non-reconnecting owner session. All work holds its mutex.
func (o *PaperOwner) Control(ctx context.Context, id string, payload []byte, execute func(*PaperOwner) (any, error)) (paper.ControlOutcome, error) {
	var result paper.ControlOutcome
	if !controlID.MatchString(id) || !json.Valid(payload) {
		return result, errors.New("invalid control request identity or payload")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.conn == nil {
		return result, errors.New("owner closed")
	}
	tx, err := o.conn.Begin(ctx)
	if err != nil {
		return result, ErrUnavailable
	}
	defer rollback(tx)
	var original, stored []byte
	err = tx.QueryRow(ctx, "SELECT payload,outcome FROM billionaire.control_outcomes WHERE request_id=$1", id).Scan(&original, &stored)
	if err == nil {
		var same bool
		if err = tx.QueryRow(ctx, "SELECT $1::jsonb=$2::jsonb", original, payload).Scan(&same); err != nil {
			return result, ErrUnavailable
		}
		if !same {
			return result, errors.New("request ID already used with different command inputs")
		}
		if json.Unmarshal(stored, &result) != nil {
			return result, ErrSchema
		}
		return result, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return result, ErrUnavailable
	}
	work, err := tx.Begin(ctx)
	if err != nil {
		return result, ErrUnavailable
	}
	child := &PaperOwner{namespace: o.namespace, conn: &scopedConn{Conn: o.conn.Conn, schema: o.conn.schema, transaction: work}}
	value, actionErr := execute(child)
	result = paper.ControlOutcome{RequestID: id, Status: "accepted"}
	if actionErr != nil {
		if err = work.Rollback(ctx); err != nil {
			return paper.ControlOutcome{}, ErrUnavailable
		}
		if errors.Is(actionErr, ErrUnavailable) || ctx.Err() != nil {
			return paper.ControlOutcome{}, actionErr
		}
		result.Status = "rejected"
		result.Reason = actionErr.Error()
	} else {
		if err = work.Commit(ctx); err != nil {
			return paper.ControlOutcome{}, ErrUnavailable
		}
		raw, e := json.Marshal(value)
		if e != nil {
			return result, e
		}
		result.Result = raw
		switch v := value.(type) {
		case paper.Order:
			result.Status = v.Status
			result.Reason = v.Reason
		case paper.SignalDecision:
			result.Status = v.Status
			result.Reason = v.Reason
			if v.Order != nil {
				result.Status = v.Order.Status
				result.Reason = v.Order.Reason
			}
		}
	}
	encoded, _ := json.Marshal(result)
	if _, err = tx.Exec(ctx, "INSERT INTO billionaire.control_outcomes(request_id,payload,outcome) VALUES($1,$2,$3)", id, payload, encoded); err != nil {
		return paper.ControlOutcome{}, ErrUnavailable
	}
	if err = tx.Commit(ctx); err != nil {
		return paper.ControlOutcome{}, errors.New("control commit outcome unknown; query outcome with the same request ID")
	}
	return result, nil
}
func (o *PaperOwner) ControlOutcome(ctx context.Context, id string) (paper.ControlOutcome, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	var result paper.ControlOutcome
	if o.conn == nil {
		return result, errors.New("owner closed")
	}
	var raw []byte
	err := o.conn.QueryRow(ctx, "SELECT outcome FROM billionaire.control_outcomes WHERE request_id=$1", id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, errors.New("control outcome not found; request may not have been accepted")
	}
	if err != nil {
		return result, ErrUnavailable
	}
	err = json.Unmarshal(raw, &result)
	return result, err
}
func (o *PaperOwner) SetControl(ctx context.Context, kind, id, strategyID string, enabled bool, policy paper.RiskPolicy) (any, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.conn == nil {
		return nil, ErrUnavailable
	}
	tx, err := o.conn.Begin(ctx)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rollback(tx)
	switch kind {
	case "kill-switch":
		_, err = tx.Exec(ctx, "UPDATE billionaire.paper_controls SET kill_switch=$1 WHERE id", enabled)
	case "strategy.enabled":
		if enabled {
			var account, found string
			if err = tx.QueryRow(ctx, "SELECT binding->>'account' FROM billionaire.strategy_instances WHERE id=$1", id).Scan(&account); err != nil {
				return nil, errors.New("strategy instance unavailable")
			}
			if err = tx.QueryRow(ctx, "SELECT id FROM billionaire.paper_accounts WHERE id=$1", account).Scan(&found); err != nil {
				return nil, errors.New("strategy instance requires an existing paper account")
			}
		}
		tag, e := tx.Exec(ctx, "UPDATE billionaire.strategy_instances SET enabled=$2 WHERE id=$1", id, enabled)
		err = e
		if err == nil && tag.RowsAffected() != 1 {
			return nil, errors.New("strategy instance not found")
		}
	case "risk.set":
		if err = policy.Validate(); err != nil {
			return nil, err
		}
		var found string
		if err = tx.QueryRow(ctx, "SELECT id FROM billionaire.paper_accounts WHERE id=$1 FOR UPDATE", id).Scan(&found); err != nil {
			return nil, errors.New("paper account unavailable")
		}
		if strategyID != "" {
			if err = tx.QueryRow(ctx, "SELECT id FROM billionaire.strategy_definitions WHERE id=$1", strategyID).Scan(&found); err != nil {
				return nil, errors.New("strategy definition unavailable")
			}
		}
		raw, e := json.Marshal(policy)
		if e != nil {
			return nil, e
		}
		_, err = tx.Exec(ctx, `INSERT INTO billionaire.paper_risk_policies(account_id,strategy_id,policy) VALUES($1,$2,$3) ON CONFLICT(account_id,strategy_id) DO UPDATE SET policy=EXCLUDED.policy`, id, strategyID, raw)
	default:
		return nil, errors.New("unsupported owner control")
	}
	if err != nil {
		return nil, safeError(ctx, ErrUnavailable)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, ErrUnavailable
	}
	if kind == "risk.set" {
		return policy, nil
	}
	return map[string]any{"id": id, "enabled": enabled}, nil
}
func (o *PaperOwner) SignalInstrument(ctx context.Context, id string) (domain.Instrument, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.conn == nil {
		return domain.Instrument{}, ErrUnavailable
	}
	signal, err := decodePaperRow[strategy.Signal](o.conn.QueryRow(ctx, "SELECT payload FROM billionaire.strategy_signals WHERE id=$1", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Instrument{}, errors.New("signal not found")
	}
	if err != nil {
		return domain.Instrument{}, ErrUnavailable
	}
	return signal.Instrument, nil
}

// RegisterStrategy keeps explicit offline setup on the held owner's session.
func (o *PaperOwner) RegisterStrategy(ctx context.Context, definition strategy.Definition, binding *strategy.Instance) (strategy.Version, *strategy.Instance, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	var version strategy.Version
	if o.conn == nil {
		return version, nil, ErrUnavailable
	}
	tx, err := o.conn.Begin(ctx)
	if err != nil {
		return version, nil, ErrUnavailable
	}
	defer rollback(tx)
	store := &Store{pool: &scopedPool{schema: o.namespace, transaction: tx}}
	version, err = store.AddStrategyVersion(ctx, definition, strategy.DefaultRegistry())
	if err != nil {
		return version, nil, err
	}
	var instance *strategy.Instance
	if binding != nil {
		copy := *binding
		copy.VersionID = version.ID
		if err = store.AddStrategyInstance(ctx, copy); err != nil {
			return version, nil, err
		}
		actual, e := store.StrategyInstance(ctx, copy.ID)
		if e != nil {
			return version, nil, e
		}
		instance = &actual
	}
	if err = tx.Commit(ctx); err != nil {
		return version, nil, ErrUnavailable
	}
	return version, instance, nil
}
