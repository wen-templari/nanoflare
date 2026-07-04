package main

import (
	"fmt"
	"os"

	"github.com/clas/nanoflare/internal/cli"
)

func main() {
	if err := cli.NewRunner(os.Stdout, os.Stderr).Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "nanoflare:", err)
		os.Exit(1)
	}
}
