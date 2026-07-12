package main

import (
	"os"

	"github.com/pbiondich/openmrs-cli/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
