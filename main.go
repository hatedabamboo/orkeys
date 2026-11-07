package main

import (
	"os"

	"github.com/hatedabamboo/orkeys/internal/cli"
)

var version = "dev"

func main() {
	os.Exit(cli.Execute(version))
}
