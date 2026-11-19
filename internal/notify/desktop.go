package notify

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"os"
	"os/exec"
	"time"
	"unicode"
	"unicode/utf8"
)

// Desktop invokes the optional Linux notify-send utility using literal arguments.
// The parent environment supplies the user's desktop session; child output is discarded.
type Desktop struct{}

func (Desktop) Send(ctx context.Context, event Event) error {
	for _, value := range []string{event.ID, event.Kind, event.Level, event.CorrelationID, event.StrategyID, event.VersionID, event.Instrument, event.Timeframe, event.Price, event.Reason} {
		if len(value) > 4096 || !utf8.ValidString(value) {
			return &DeliveryError{Category: "desktop_invalid_event", Permanent: true}
		}
		for _, r := range value {
			if unicode.IsControl(r) {
				return &DeliveryError{Category: "desktop_invalid_event", Permanent: true}
			}
		}
	}
	binary, err := exec.LookPath("notify-send")
	if err != nil {
		return &DeliveryError{Category: "desktop_missing_binary", Permanent: true}
	}
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return &DeliveryError{Category: "desktop_session_unavailable", Permanent: true}
	}
	body, err := json.Marshal(event)
	if err != nil {
		return &DeliveryError{Category: "desktop_invalid_event", Permanent: true}
	}
	bounded, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	command := exec.CommandContext(bounded, binary, "--app-name=Billionaire", "--", "Billionaire: "+event.Kind, html.EscapeString(string(body)))
	if err = command.Run(); err != nil {
		if errors.Is(bounded.Err(), context.DeadlineExceeded) || errors.Is(bounded.Err(), context.Canceled) {
			return &DeliveryError{Category: "desktop_timeout"}
		}
		return &DeliveryError{Category: "desktop_failed"}
	}
	return nil
}
