package main

import (
	"fmt"
	"os"

	"liki/internal/cli"
)

func main() {
	if err := cli.Run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "liki:", err)
		os.Exit(1)
	}
}
