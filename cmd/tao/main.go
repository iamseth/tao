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
	if err := app.Run(ctx, args); err != nil {
		_, _ = fmt.Fprintln(errOut, err)
		return 1
	}
	return 0
}
