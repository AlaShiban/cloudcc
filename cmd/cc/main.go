// Command cc is the CloudCompiler CLI.
package main

import (
	"os"

	"github.com/cloudcompiler/cc/internal/cli"
)

func main() {
	os.Exit(cli.Execute(os.Args[1:]))
}
