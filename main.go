// Command agentsync-tui is a Bubble Tea TUI that drives the agentsync CLI.
package main

import (
	"fmt"
	"os"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("agentsync-tui " + version)
		return
	}
}
