package main

import (
	"os"

	"github.com/hatedabamboo/orkeys/internal/cli"
)

// version is set at build time: go build -ldflags "-X main.version=1.0.0"
var version = "dev"

func main() {
	os.Exit(cli.Execute(version))
}
