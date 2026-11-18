package cli_test

import (
	"context"
	"github.com/saintmili/billionaire/internal/cli"
	"io"
)

func executeOffline(ctx context.Context, args []string, out, diagnostics io.Writer) error {
	return cli.Execute(ctx, append(args, "--offline"), out, diagnostics)
}
