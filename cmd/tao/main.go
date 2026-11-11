// Command tao is the CLI entry point. It delegates to internal/cli and owns no
// workflow logic.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/iamseth/tao/internal/cli"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, in io.Reader, out io.Writer, errOut io.Writer) int {
	app := cli.App{In: in, Out: out, Err: errOut}
	return reportExit(app.Run(ctx, args), errOut)
}

func reportExit(err error, errOut io.Writer) int {
	if err != nil {
		_, _ = fmt.Fprintln(errOut, err)
	}
	return cli.ExitCode(err)
}
