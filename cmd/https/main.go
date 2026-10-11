package main

import (
	"httpie/internal/cli"
	"os"
)

func main() { os.Exit(cli.Run("https", os.Args[1:])) }
