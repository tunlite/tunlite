package main

import (
	"os"

	"github.com/tunlite/tunlite/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], cli.IO{Out: os.Stdout, Err: os.Stderr, In: os.Stdin}))
}
