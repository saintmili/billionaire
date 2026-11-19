package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"io"
	"time"
)

type Command struct {
	ID      string          `json:"request_id"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// Control is transport independent. Metadata is the owner's validated snapshot.
type Control struct {
	owner       *postgres.PaperOwner
	instruments map[string]marketdata.InstrumentInfo
	staleAfter  time.Duration
}

func NewControl(owner *postgres.PaperOwner, instruments []marketdata.InstrumentInfo, staleAfter time.Duration) *Control {
	c := &Control{owner: owner, instruments: map[string]marketdata.InstrumentInfo{}, staleAfter: staleAfter}
	for _, info := range instruments {
		c.instruments[info.Instrument.ID+":"+string(info.Instrument.Market)] = info
	}
	return c
}
func DecodeControl(data []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return errors.New("invalid control JSON or unknown field")
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("expected one control JSON document")
	}
	return nil
}
func (c *Control) Outcome(ctx context.Context, id string) (paper.ControlOutcome, error) {
	return c.owner.ControlOutcome(ctx, id)
}
func (c *Control) Execute(ctx context.Context, command Command) (paper.ControlOutcome, error) {
	payload, err := json.Marshal(command)
	if err != nil {
		return paper.ControlOutcome{}, err
	}
	return c.owner.Control(ctx, command.ID, payload, func(owner *postgres.PaperOwner) (any, error) {
		switch command.Kind {
		case "account.adjust":
			var r paper.CapitalAdjustment
			if err := DecodeControl(command.Payload, &r); err != nil {
				return nil, err
			}
			return owner.AdjustCapital(ctx, command.ID, r, c.staleAfter)
		case "account.create":
			var r paper.CreateAccount
			if err := DecodeControl(command.Payload, &r); err != nil {
				return nil, err
			}
			return owner.CreateAccount(ctx, r)
		case "order.submit":
			var r paper.OrderRequest
			if err := DecodeControl(command.Payload, &r); err != nil {
				return nil, err
			}
			if r.Action == "close" {
				position, err := owner.ControlPosition(ctx, r.PositionID)
				if err != nil {
					return nil, err
				}
				r.AccountID = position.AccountID
				r.Instrument = position.Instrument
				r.Side = position.Direction()
				r.Leverage = position.EffectiveLeverage()
				return owner.SubmitOrder(ctx, r, position.Metadata, true)
			}
			info, ok := c.instruments[r.Instrument.ID+":"+string(r.Instrument.Market)]
			if !ok || info.Instrument != r.Instrument {
				return nil, errors.New("instrument is not configured by owner")
			}
			return owner.SubmitOrder(ctx, r, info, true)
		case "position.adjust":
			var r paper.ProtectionAdjustment
			if err := DecodeControl(command.Payload, &r); err != nil {
				return nil, err
			}
			r.StaleAfter = c.staleAfter
			return owner.AdjustProtection(ctx, r)
		case "signal.approve":
			var r paper.SignalEntryRequest
			if err := DecodeControl(command.Payload, &r); err != nil {
				return nil, err
			}
			instrument, err := owner.SignalInstrument(ctx, r.SignalID)
			if err != nil {
				return nil, err
			}
			info, ok := c.instruments[instrument.ID+":"+string(instrument.Market)]
			if !ok || info.Instrument != instrument {
				return nil, errors.New("signal instrument is not configured by owner")
			}
			r.Approve = true
			return owner.DecideSignal(ctx, r, info, true)
		case "kill-switch", "strategy.enabled", "risk.set":
			var r struct {
				ID         string           `json:"id"`
				StrategyID string           `json:"strategy_id"`
				Enabled    bool             `json:"enabled"`
				Policy     paper.RiskPolicy `json:"policy"`
			}
			if err := DecodeControl(command.Payload, &r); err != nil {
				return nil, err
			}
			return owner.SetControl(ctx, command.Kind, r.ID, r.StrategyID, r.Enabled, r.Policy)
		default:
			return nil, errors.New("unsupported control command")
		}
	})
}
