package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/paper"
)

func instrumentKey(i domain.Instrument) string {
	return i.Provider + ":" + string(i.Market) + ":" + i.ID
}

func decodePaperRow[T any](row pgx.Row) (T, error) {
	var value T
	var payload []byte
	if err := row.Scan(&payload); err != nil {
		return value, err
	}
	if json.Unmarshal(payload, &value) != nil {
		return value, ErrSchema
	}
	return value, nil
}
func decodePaperRows[T any](rows pgx.Rows) ([]T, error) {
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (T, error) { return decodePaperRow[T](row) })
}

func (o *PaperOwner) SubmitSpotOrder(ctx context.Context, request paper.OrderRequest, info marketdata.InstrumentInfo, enabled bool) (paper.Order, error) {
	order, err := paper.PrepareOrder(request, info)
	if err != nil {
		return order, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.conn == nil {
		return order, errors.New("paper owner is closed")
	}
	tx, err := o.conn.Begin(ctx)
	if err != nil {
		return order, safeError(ctx, ErrUnavailable)
	}
	defer rollback(tx)
	existing, err := decodePaperRow[paper.Order](tx.QueryRow(ctx, "SELECT payload FROM millionaire.paper_orders WHERE id=$1", order.ID))
	if err == nil {
		if !paper.SameOrder(existing, order) {
			return order, errors.New("request ID already used with different order inputs")
		}
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return order, safeError(ctx, ErrUnavailable)
	}
	var accountID string
	if err := tx.QueryRow(ctx, "SELECT id FROM millionaire.paper_accounts WHERE id=$1 FOR UPDATE", request.AccountID).Scan(&accountID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return order, errors.New("paper account not found")
		}
		return order, safeError(ctx, ErrUnavailable)
	}
	if request.Action == "close" {
		p, err := decodePaperRow[paper.Position](tx.QueryRow(ctx, "SELECT payload FROM millionaire.paper_positions WHERE id=$1 FOR UPDATE", request.PositionID))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return order, errors.New("paper position not found")
			}
			return order, safeError(ctx, ErrUnavailable)
		}
		quantity, _ := domain.ParseDecimal(order.Request.Quantity)
		if p.AccountID != request.AccountID || p.Instrument != request.Instrument || p.Status != "open" || quantity.GreaterThan(p.RemainingQuantity) {
			order.Status = "rejected"
			order.Reason = "close exceeds owned holdings or position identity is invalid"
		}
	}
	if request.Action == "open" && !enabled {
		order.Status = "rejected"
		order.Reason = "market disabled"
	}
	payload, err := json.Marshal(order)
	if err != nil {
		return order, errors.New("invalid order payload")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO millionaire.paper_orders(id,account_id,instrument_key,intent_at,status,payload) VALUES($1,$2,$3,$4,$5,$6)`, order.ID, request.AccountID, instrumentKey(request.Instrument), request.At, order.Status, payload); err != nil {
		return order, safeError(ctx, ErrUnavailable)
	}
	if err := tx.Commit(ctx); err != nil {
		return order, safeError(ctx, errors.New("order commit outcome unknown; retry with the same request ID and intent time"))
	}
	return order, nil
}

// ObserveSpot owns event replay and commits all decisions and financial changes
// for one delivered price together. A failed event remains safely retryable.
func (o *PaperOwner) ObserveSpot(ctx context.Context, observation paper.Observation, gate paper.ExecutionGate) (paper.ObservationResult, error) {
	result := paper.ObservationResult{ObservationID: observation.ID, Orders: []paper.Order{}}
	if err := observation.Validate(); err != nil {
		return result, err
	}
	if gate.Now.IsZero() || gate.StaleAfter <= 0 || observation.AvailableAt.After(gate.Now) {
		return result, errors.New("observation must be available at a valid execution clock with positive stale threshold")
	}
	observation.ObservedAt = observation.ObservedAt.UTC()
	observation.AvailableAt = observation.AvailableAt.UTC()
	gate.Now = gate.Now.UTC()
	payload, err := json.Marshal(observation)
	if err != nil {
		return result, errors.New("invalid observation payload")
	}
	key := instrumentKey(observation.Instrument)
	eventID := paper.Identity("observation", key+":"+observation.ID)
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.conn == nil {
		return result, errors.New("paper owner is closed")
	}
	tx, err := o.conn.Begin(ctx)
	if err != nil {
		return result, safeError(ctx, ErrUnavailable)
	}
	defer rollback(tx)
	var previous, storedResult []byte
	err = tx.QueryRow(ctx, "SELECT payload,result FROM millionaire.paper_price_events WHERE id=$1", eventID).Scan(&previous, &storedResult)
	if err == nil {
		var stored paper.Observation
		if json.Unmarshal(previous, &stored) != nil || json.Unmarshal(storedResult, &result) != nil {
			return result, ErrSchema
		}
		canonical, _ := json.Marshal(stored)
		if string(canonical) != string(payload) {
			return result, errors.New("observation ID conflicts with previously recorded content")
		}
		return result, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return result, safeError(ctx, ErrUnavailable)
	}
	// JSON retains the original nanosecond clocks; TIMESTAMPTZ columns are
	// only query aids because PostgreSQL timestamps have microsecond precision.
	last, err := decodePaperRow[paper.Observation](tx.QueryRow(ctx, `SELECT payload FROM millionaire.paper_price_events WHERE instrument_key=$1 AND accepted ORDER BY sequence DESC LIMIT 1`, key))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return result, safeError(ctx, ErrUnavailable)
	}
	accepted := errors.Is(err, pgx.ErrNoRows) || (!observation.ObservedAt.Before(last.ObservedAt) && !observation.AvailableAt.Before(last.AvailableAt))
	if !accepted {
		result.Reason = "out-of-order observation skipped"
	} else {
		rows, err := tx.Query(ctx, `SELECT payload FROM millionaire.paper_orders WHERE instrument_key=$1 AND status='pending' ORDER BY intent_at,id FOR UPDATE`, key)
		if err != nil {
			return result, safeError(ctx, ErrUnavailable)
		}
		orders, err := decodePaperRows[paper.Order](rows)
		if err != nil {
			return result, safeError(ctx, ErrSchema)
		}
		sort.Slice(orders, func(i, j int) bool {
			if orders[i].Request.At.Equal(orders[j].Request.At) {
				return orders[i].ID < orders[j].ID
			}
			return orders[i].Request.At.Before(orders[j].Request.At)
		})
		for _, order := range orders {
			var balanceText string
			if err := tx.QueryRow(ctx, "SELECT balance::text FROM millionaire.paper_accounts WHERE id=$1 FOR UPDATE", order.Request.AccountID).Scan(&balanceText); err != nil {
				return result, safeError(ctx, ErrUnavailable)
			}
			balance, err := domain.ParseDecimal(balanceText)
			if err != nil {
				return result, ErrSchema
			}
			var position *paper.Position
			if order.Request.Action == "close" {
				p, err := decodePaperRow[paper.Position](tx.QueryRow(ctx, "SELECT payload FROM millionaire.paper_positions WHERE id=$1 FOR UPDATE", order.Request.PositionID))
				if err != nil {
					return result, safeError(ctx, ErrSchema)
				}
				position = &p
			}
			plan, err := paper.PlanSpotFill(order, observation, gate, balance, position)
			if err != nil {
				return result, err
			}
			if plan.Order.Status != "pending" {
				if err := commitSpotPlan(ctx, tx, plan); err != nil {
					return result, safeError(ctx, ErrUnavailable)
				}
			}
			result.Orders = append(result.Orders, plan.Order)
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return result, errors.New("invalid observation result")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO millionaire.paper_price_events(id,instrument_key,observed_at,available_at,payload,result,accepted) VALUES($1,$2,$3,$4,$5,$6,$7)`, eventID, key, observation.ObservedAt, observation.AvailableAt, payload, encoded, accepted); err != nil {
		return result, safeError(ctx, ErrUnavailable)
	}
	if err := tx.Commit(ctx); err != nil {
		return result, safeError(ctx, errors.New("observation commit outcome unknown; retry the same observation"))
	}
	return result, nil
}

func commitSpotPlan(ctx context.Context, tx pgx.Tx, plan paper.Execution) error {
	orderPayload, err := json.Marshal(plan.Order)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE millionaire.paper_orders SET status=$2,payload=$3 WHERE id=$1", plan.Order.ID, plan.Order.Status, orderPayload); err != nil {
		return err
	}
	if plan.Fill == nil {
		return nil
	}
	p := plan.Position
	positionPayload, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO millionaire.paper_positions(id,account_id,status,payload) VALUES($1,$2,$3,$4) ON CONFLICT(id) DO UPDATE SET status=EXCLUDED.status,payload=EXCLUDED.payload`, p.ID, p.AccountID, p.Status, positionPayload); err != nil {
		return err
	}
	f := plan.Fill
	fillPayload, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO millionaire.paper_fills(id,order_id,position_id,account_id,payload) VALUES($1,$2,$3,$4,$5)`, f.ID, f.OrderID, f.PositionID, p.AccountID, fillPayload); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE millionaire.paper_accounts SET balance=balance+$2::numeric WHERE id=$1", p.AccountID, plan.CashDelta.String()); err != nil {
		return err
	}
	for _, movement := range plan.Movements {
		if _, err := tx.Exec(ctx, `INSERT INTO millionaire.paper_ledger_entries(id,account_id,kind,amount,fill_id) VALUES($1,$2,$3,$4,$5)`, f.ID+":"+movement.Kind, p.AccountID, movement.Kind, movement.Amount.String(), f.ID); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO millionaire.journal_outbox(event_id,kind,payload) VALUES($1,'paper_fill',$2)`, f.ID, fillPayload)
	return err
}

func (s *Store) PaperPositions(ctx context.Context, accountID string) ([]paper.Position, error) {
	rows, err := s.pool.Query(ctx, `SELECT payload FROM millionaire.paper_positions WHERE ($1='' OR account_id=$1) ORDER BY id`, accountID)
	if err != nil {
		return nil, safeError(ctx, ErrUnavailable)
	}
	result, err := decodePaperRows[paper.Position](rows)
	if err != nil {
		return nil, safeError(ctx, ErrSchema)
	}
	if result == nil {
		result = []paper.Position{}
	}
	return result, nil
}
func (s *Store) PaperPosition(ctx context.Context, id string) (paper.Position, error) {
	result, err := decodePaperRow[paper.Position](s.pool.QueryRow(ctx, "SELECT payload FROM millionaire.paper_positions WHERE id=$1", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return result, errors.New("paper position not found")
	}
	if err != nil {
		return result, safeError(ctx, ErrUnavailable)
	}
	return result, nil
}
func (s *Store) PaperOrders(ctx context.Context, accountID string) ([]paper.Order, error) {
	rows, err := s.pool.Query(ctx, `SELECT payload FROM millionaire.paper_orders WHERE ($1='' OR account_id=$1) ORDER BY intent_at,id`, accountID)
	if err != nil {
		return nil, safeError(ctx, ErrUnavailable)
	}
	result, err := decodePaperRows[paper.Order](rows)
	if err != nil {
		return nil, safeError(ctx, ErrSchema)
	}
	if result == nil {
		result = []paper.Order{}
	}
	return result, nil
}
func (s *Store) PaperFills(ctx context.Context, accountID string) ([]paper.Fill, error) {
	rows, err := s.pool.Query(ctx, `SELECT payload FROM millionaire.paper_fills WHERE ($1='' OR account_id=$1) ORDER BY id`, accountID)
	if err != nil {
		return nil, safeError(ctx, ErrUnavailable)
	}
	result, err := decodePaperRows[paper.Fill](rows)
	if err != nil {
		return nil, safeError(ctx, ErrSchema)
	}
	if result == nil {
		result = []paper.Fill{}
	}
	return result, nil
}
