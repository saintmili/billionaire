package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/saintmili/billionaire/internal/app"
	"github.com/saintmili/billionaire/internal/control"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
	"strconv"
)

func defaultControlSocket() string {
	return filepath.Join(os.TempDir(), "billionaire-"+strconv.Itoa(os.Getuid()), "control.sock")
}
func (o *options) controlMutation(ctx context.Context, id, kind string, payload any, target any) error {
	if id == "" {
		return fmt.Errorf("--request-id is required for owner mutations")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	result, err := control.Call(ctx, o.controlSocket, control.Request{Command: &app.Command{ID: id, Kind: kind, Payload: raw}})
	if err != nil {
		return err
	}
	if result.Status == "rejected" && len(result.Result) == 0 {
		if err := o.write(result, fmt.Sprintf("Request %s: rejected %s", result.RequestID, result.Reason)); err != nil {
			return err
		}
		return fmt.Errorf("control rejected: %s", result.Reason)
	}
	if target != nil {
		return json.Unmarshal(result.Result, target)
	}
	return o.write(result, fmt.Sprintf("Request %s: %s %s", result.RequestID, result.Status, result.Reason))
}
func (o *options) addControlCommands(root *cobra.Command) {
	root.PersistentFlags().StringVar(&o.controlSocket, "control-socket", defaultControlSocket(), "absolute private owner Unix socket path")
	root.PersistentFlags().BoolVar(&o.offline, "offline", false, "explicit fixture/setup mode; requires exclusive ownership, never falls back from socket")
	group := &cobra.Command{Use: "control", Short: "Recover immutable command outcomes"}
	group.AddCommand(&cobra.Command{Use: "outcome REQUEST-ID", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		result, err := control.Call(cmd.Context(), o.controlSocket, control.Request{Lookup: args[0]})
		if err != nil {
			return err
		}
		return o.write(result, fmt.Sprintf("Request %s: %s %s", result.RequestID, result.Status, result.Reason))
	}})
	root.AddCommand(group)
}
