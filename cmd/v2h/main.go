// Command v2h runs the proxy core, the web control panel and the terminal
// console from a single binary.
package main

import (
	"os"

	"github.com/ldm0206/vless-to-http/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
