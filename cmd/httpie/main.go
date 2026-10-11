package main

import (
	"httpie/internal/cli"
	"os"
)

func main() { os.Exit(cli.Run("httpie", os.Args[1:])) }
