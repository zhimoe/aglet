package main

import (
	"os"

	"github.com/zhimoe/aglet/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
