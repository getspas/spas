package main

import (
	"os"

	"github.com/getspas/spas/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
