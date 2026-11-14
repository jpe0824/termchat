package main

import (
	"fmt"
	"os"

	"github.com/jpe0824/termchat/internal/cli"
)

func main() {
	if err := cli.Root().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "termchat:", err)
		os.Exit(1)
	}
}
