// Package notify delivers durable events independently of financial processing.
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"
)

type Event struct {
	ID            string    `json:"id"`
	Kind          string    `json:"kind"`
	Level         string    `json:"level"`
	CorrelationID string    `json:"correlation_id"`
	StrategyID    string    `json:"strategy_id"`
	VersionID     string    `json:"version_id"`
	Instrument    string    `json:"instrument"`
	Timeframe     string    `json:"timeframe"`
	Price         string    `json:"price"`
	At            time.Time `json:"at"`
	Reason        string    `json:"reason"`
}
type Policy struct {
	Targets  []string
	Key      string
	Cooldown time.Duration
}
type Sender interface {
	Send(context.Context, Event) error
}
type Terminal struct{ Writer io.Writer }

func (t Terminal) Send(ctx context.Context, e Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if t.Writer == nil {
		return &DeliveryError{Category: "terminal_unavailable", Permanent: true}
	}
	return json.NewEncoder(t.Writer).Encode(e)
}

type DeliveryError struct {
	Category   string
	Permanent  bool
	RetryAfter time.Duration
}

func (e *DeliveryError) Error() string { return e.Category }

type Delivery struct {
	Event         Event      `json:"event"`
	Target        string     `json:"target"`
	State         string     `json:"state"`
	Attempts      int        `json:"attempts"`
	Error         string     `json:"error,omitempty"`
	Token         string     `json:"-"`
	NextAttemptAt time.Time  `json:"next_attempt_at,omitzero"`
	DeliveredAt   *time.Time `json:"delivered_at,omitempty"`
	LeaseUntil    *time.Time `json:"lease_until,omitempty"`
}
type Queue interface {
	ClaimNotifications(context.Context, time.Time, int) ([]Delivery, error)
	FinishNotification(context.Context, Delivery, string, string, time.Time) error
}

// Dispatch claims a bounded batch. Sends occur after the claim transaction commits.
// A crashed sender can repeat display after lease expiry; acknowledgement is guarded.
func Dispatch(ctx context.Context, q Queue, senders map[string]Sender, now time.Time, limit int) (int, error) {
	rows, err := q.ClaimNotifications(ctx, now, limit)
	if err != nil {
		return 0, err
	}
	for _, d := range rows {
		state, category := "delivered", ""
		next := now
		sender := senders[d.Target]
		var sendErr error
		if d.Attempts > 5 {
			sendErr = &DeliveryError{Category: "attempts_exhausted", Permanent: true}
		} else if sender == nil {
			sendErr = &DeliveryError{Category: "target_unavailable", Permanent: true}
		} else {
			c, cancel := context.WithTimeout(ctx, 2*time.Second)
			sendErr = sender.Send(c, d.Event)
			cancel()
		}
		if sendErr != nil {
			state = "pending"
			category = "delivery_failed"
			next = now.Add(time.Duration(1<<min(d.Attempts, 8)) * time.Second)
			var classified *DeliveryError
			if errors.As(sendErr, &classified) {
				category = classified.Category
				if classified.Permanent {
					state = "failed"
				}
				if classified.RetryAfter > 0 {
					next = now.Add(classified.RetryAfter)
				}
			}
			if d.Attempts >= 5 {
				state = "failed"
			}
		}
		if err = q.FinishNotification(ctx, d, state, category, next); err != nil {
			return len(rows), err
		}
	}
	return len(rows), nil
}

// Run is a cancellation-aware worker with bounded batches and no in-memory backlog.
func Run(ctx context.Context, q Queue, senders map[string]Sender, diagnostics io.Writer) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if _, err := Dispatch(ctx, q, senders, now.UTC(), 16); err != nil && diagnostics != nil {
				io.WriteString(diagnostics, "notification worker unavailable\n")
			}
		}
	}
}
