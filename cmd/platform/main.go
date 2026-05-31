package main

import (
	"fmt"
	"os"

	"github.com/clas/platform/internal/cli"
)

func main() {
	if err := cli.NewRunner(os.Stdout, os.Stderr).Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "platform:", err)
		os.Exit(1)
	}
}
