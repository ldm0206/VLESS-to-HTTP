// Package cli implements the v2h command line.
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ldm0206/vless-to-http/internal/client"
	"github.com/ldm0206/vless-to-http/internal/config"
	"github.com/ldm0206/vless-to-http/internal/help"
)

// globals holds the flags every command accepts.
type globals struct {
	dataDir string
	api     string
	token   string
	json    bool
}

// Run executes one command line and returns the process exit code.
func Run(args []string) int {
	g, rest := extractGlobals(args)

	if len(rest) == 0 {
		fmt.Print(help.Text())
		return 0
	}

	command, sub := rest[0], rest[1:]
	switch command {
	case "help", "-h", "--help":
		if len(sub) > 0 {
			fmt.Print(help.CommandText(sub[0]))
			return 0
		}
		fmt.Print(help.Text())
		return 0
	case "version", "--version", "-v":
		printVersion(g)
		return 0
	case "serve":
		return runServe(g, sub)
	case "tui":
		return runTUI(g, sub)
	case "status":
		return runStatus(g, sub)
	case "user":
		return runUser(g, sub)
	case "sub":
		return runSub(g, sub)
	case "node":
		return runNode(g, sub)
	case "logs":
		return runLogs(g, sub)
	case "config":
		return runConfig(g, sub)
	case "admin":
		return runAdmin(g, sub)
	case "core":
		return runCore(g, sub)
	default:
		fmt.Fprintf(os.Stderr, "未知命令 %q\n\n", command)
		fmt.Print(help.Text())
		return 2
	}
}

// extractGlobals pulls the shared flags out of the argument list so they can
// appear before or after the command name.
func extractGlobals(args []string) (globals, []string) {
	g := globals{}
	rest := make([]string, 0, len(args))

	for i := 0; i < len(args); i++ {
		name, value, hasValue := strings.Cut(args[i], "=")

		switch name {
		case "--data", "--api", "--token":
			if !hasValue {
				if i+1 >= len(args) {
					return g, append(rest, args[i])
				}
				i++
				value = args[i]
			}
			switch name {
			case "--data":
				g.dataDir = value
			case "--api":
				g.api = value
			case "--token":
				g.token = value
			}
		case "--json":
			g.json = true
		default:
			rest = append(rest, args[i])
		}
	}
	return g, rest
}

// dataDir resolves where config.yaml lives, matching the server's own logic.
func (g globals) dataDirResolved() string {
	if g.dataDir != "" {
		return g.dataDir
	}
	if env := os.Getenv("V2H_DATA_DIR"); env != "" {
		return env
	}
	if dir := os.Getenv("V2H_DATA"); dir != "" {
		return dir
	}
	if writable("/data") {
		return "/data"
	}
	return "data"
}

func writable(dir string) bool {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	probe := filepath.Join(dir, ".v2h-write-test")
	if err := os.WriteFile(probe, []byte("x"), 0o600); err != nil {
		return false
	}
	os.Remove(probe)
	return true
}

// resolveTarget works out which panel to talk to, falling back to the config
// file next to the data directory.
func (g globals) resolveTarget() (string, string) {
	base, token := g.api, g.token
	if base != "" && token != "" {
		return base, token
	}

	store := config.NewStore(g.dataDirResolved())
	if _, err := store.Load(); err == nil {
		cfg := store.Get()
		if base == "" {
			base = cfg.Panel.Listen
		}
		if token == "" {
			token = cfg.Panel.APIToken
		}
	}
	if base == "" {
		base = "127.0.0.1:9080"
	}
	return base, token
}

// apiClient connects to the running instance.
func (g globals) apiClient() (*client.Client, error) {
	base, token := g.resolveTarget()
	c := client.New(base, token)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.Health(ctx); err != nil {
		return nil, fmt.Errorf("连接服务失败（%s）：%w\n提示：先用 v2h serve 启动服务，或用 --api/--token 指定面板地址和令牌", c.Base(), err)
	}
	return c, nil
}

// newFlagSet builds a flag set whose help output goes to stdout.
func newFlagSet(name string, out io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() {}
	return fs
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "错误："+err.Error())
	return 1
}

func ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 3*time.Minute)
}

// reorder moves flags in front of positional arguments. Go's flag package
// stops parsing at the first positional token, which would silently drop every
// flag in commands written the natural way, e.g. `v2h sub import 名称 --file x`.
func reorder(fs *flag.FlagSet, args []string) []string {
	boolean := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) {
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			boolean[f.Name] = true
		}
	})

	var flags, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(arg) < 2 || arg[0] != '-' {
			positional = append(positional, arg)
			continue
		}

		flags = append(flags, arg)
		name := strings.TrimLeft(arg, "-")
		if strings.ContainsRune(name, '=') {
			continue
		}
		// A flag that takes a value swallows the next token, so that token is
		// never mistaken for a positional argument.
		if !boolean[name] && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, positional...)
}
