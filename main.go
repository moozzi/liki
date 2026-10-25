package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"liki/internal/cli"
	"liki/internal/tui"
)

func main() {
	// With no arguments in a terminal, start the interactive mode.
	if len(os.Args) == 1 && isTerminal(os.Stdin) && isTerminal(os.Stdout) {
		if err := tui.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "liki:", err)
			os.Exit(1)
		}
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := cli.Run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "liki:", err)
		os.Exit(1)
	}
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
