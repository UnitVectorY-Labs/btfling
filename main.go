package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/UnitVectorY-Labs/btfling/internal/app"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := app.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "btfling:", err)
		if ctx.Err() != nil {
			os.Exit(130)
		}
		if app.IsUsage(err) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}
