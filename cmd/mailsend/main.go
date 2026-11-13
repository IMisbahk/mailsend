package main

import (
	"github.com/example-account/mailsend/internal/cli"
	"os"
)

func main() { os.Exit(cli.Execute()) }
