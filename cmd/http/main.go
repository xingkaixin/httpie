package main

import (
	"httpie/internal/cli"
	"os"
)

func main() { os.Exit(cli.Run("http", os.Args[1:])) }
