// Command cloudcc is the CloudCompiler CLI.
package main

import (
	"os"

	"github.com/cloudcompiler/cloudcc/internal/cli"
)

func main() {
	os.Exit(cli.Execute(os.Args[1:]))
}
