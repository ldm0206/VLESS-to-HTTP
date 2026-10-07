package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/ldm0206/vless-to-http/internal/help"
	"github.com/ldm0206/vless-to-http/internal/tui"
	"github.com/ldm0206/vless-to-http/internal/version"
)

func printVersion(g globals) {
	if g.json {
		printJSON(map[string]any{
			"version": version.Version,
			"commit":  version.Commit,
			"built":   version.Date,
			"xray":    version.Xray(),
			"go":      version.GoRuntime(),
		})
		return
	}
	fmt.Printf("v2h %s\n", version.Full())
	fmt.Printf("构建时间  %s\n", version.Date)
	fmt.Printf("Xray 内核 %s\n", version.Xray())
	fmt.Printf("Go 版本   %s\n", version.GoRuntime())
}

func runTUI(g globals, args []string) int {
	fs := newFlagSet("tui", os.Stdout)
	if err := fs.Parse(reorder(fs, args)); err != nil {
		return 2
	}
	if _, err := g.apiClient(); err != nil {
		return fail(err)
	}

	base := g.api
	token := g.token
	if base == "" || token == "" {
		c, err := g.apiClient()
		if err != nil {
			return fail(err)
		}
		base, token = c.Base(), tokensFromConfig(g)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := tui.Run(ctx, tui.Options{BaseURL: base, Token: token}); err != nil {
		return fail(err)
	}
	return 0
}

// tokensFromConfig re-reads the token, since apiClient hides it.
func tokensFromConfig(g globals) string {
	fs := newFlagSet("token", os.Stdout)
	_ = fs
	path := configPath(g)
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "api_token:") {
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "api_token:")), `"'`)
		}
	}
	return ""
}

func configPath(g globals) string {
	return g.dataDirResolved() + string(os.PathSeparator) + "config.yaml"
}

// --- config --------------------------------------------------------------

func runConfig(g globals, args []string) int {
	if len(args) == 0 {
		fmt.Print(help.CommandText("config"))
		return 0
	}
	switch args[0] {
	case "path":
		fmt.Println(configPath(g))
		return 0
	case "show":
		return runConfigShow(g, args[1:])
	case "get":
		if len(args) < 2 {
			return fail(fmt.Errorf("用法：v2h config get <键>"))
		}
		return runConfigGet(g, args[1])
	case "set":
		if len(args) < 3 {
			return fail(fmt.Errorf("用法：v2h config set <键> <值>"))
		}
		return runConfigSet(g, args[1], strings.Join(args[2:], " "))
	default:
		fmt.Print(help.CommandText("config"))
		return 2
	}
}

func runConfigShow(g globals, args []string) int {
	fs := newFlagSet("config show", os.Stdout)
	jsonOut := fs.Bool("json", g.json, "以 JSON 输出")
	if err := fs.Parse(reorder(fs, args)); err != nil {
		return 2
	}
	c, err := g.apiClient()
	if err != nil {
		return fail(err)
	}
	ctx, cancel := ctx()
	defer cancel()

	settings, err := c.Settings(ctx)
	if err != nil {
		return fail(err)
	}
	if *jsonOut {
		return printJSON(settings)
	}
	printSettings(settings)
	return 0
}

func runConfigGet(g globals, key string) int {
	c, err := g.apiClient()
	if err != nil {
		return fail(err)
	}
	ctx, cancel := ctx()
	defer cancel()

	settings, err := c.Settings(ctx)
	if err != nil {
		return fail(err)
	}
	value, ok := navigate(settings, strings.Split(key, "."))
	if !ok {
		return fail(fmt.Errorf("配置项 %q 不存在", key))
	}
	if g.json {
		return printJSON(value)
	}
	fmt.Println(formatValue(value))
	return 0
}

func runConfigSet(g globals, key, raw string) int {
	c, err := g.apiClient()
	if err != nil {
		return fail(err)
	}
	ctx, cancel := ctx()
	defer cancel()

	settings, err := c.Settings(ctx)
	if err != nil {
		return fail(err)
	}
	parts := strings.Split(key, ".")
	existing, ok := navigate(settings, parts)
	if !ok {
		return fail(fmt.Errorf("配置项 %q 不存在", key))
	}
	value, err := coerce(raw, existing)
	if err != nil {
		return fail(err)
	}

	body := nest(parts, value)
	if err := c.PatchSettings(ctx, body); err != nil {
		return fail(err)
	}
	fmt.Printf("%s = %s\n", key, formatValue(value))
	return 0
}

// navigate walks a dotted path through the settings document.
func navigate(root map[string]any, parts []string) (any, bool) {
	var current any = root
	for _, part := range parts {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

// nest rebuilds the nested object a PATCH expects.
func nest(parts []string, value any) map[string]any {
	if len(parts) == 1 {
		return map[string]any{parts[0]: value}
	}
	return map[string]any{parts[0]: nest(parts[1:], value)}
}

// coerce converts a command-line string into the type of the current value.
func coerce(raw string, existing any) (any, error) {
	switch existing.(type) {
	case bool:
		switch strings.ToLower(raw) {
		case "true", "1", "on", "yes", "是":
			return true, nil
		case "false", "0", "off", "no", "否":
			return false, nil
		default:
			return nil, fmt.Errorf("需要 true 或 false")
		}
	case float64:
		n, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("需要整数")
		}
		return n, nil
	case []any:
		parts := strings.Split(raw, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out, nil
	default:
		return raw, nil
	}
}

func formatValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			parts = append(parts, formatValue(item))
		}
		return strings.Join(parts, ",")
	default:
		return fmt.Sprint(t)
	}
}

func printSettings(settings map[string]any) {
	for _, section := range []string{"panel", "proxy", "logs", "health"} {
		value, ok := settings[section]
		if !ok {
			continue
		}
		fmt.Printf("[%s]\n", section)
		if m, ok := value.(map[string]any); ok {
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			sortStrings(keys)
			for _, k := range keys {
				if nested, ok := m[k].(map[string]any); ok {
					subKeys := make([]string, 0, len(nested))
					for sk := range nested {
						subKeys = append(subKeys, sk)
					}
					sortStrings(subKeys)
					for _, sk := range subKeys {
						fmt.Printf("  %s.%s = %s\n", k, sk, formatValue(nested[sk]))
					}
					continue
				}
				fmt.Printf("  %s = %s\n", k, formatValue(m[k]))
			}
		}
		fmt.Println()
	}
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// --- admin ---------------------------------------------------------------

func runAdmin(g globals, args []string) int {
	if len(args) == 0 {
		fmt.Print(help.CommandText("admin"))
		return 0
	}
	switch args[0] {
	case "passwd", "password":
		return runAdminPassword(g, args[1:])
	case "token":
		return runAdminToken(g, args[1:])
	default:
		fmt.Print(help.CommandText("admin"))
		return 2
	}
}

func runAdminPassword(g globals, args []string) int {
	fs := newFlagSet("admin passwd", os.Stdout)
	current := fs.String("current", "", "当前密码")
	password := fs.String("password", "", "新密码（留空则交互输入）")
	jsonOut := fs.Bool("json", g.json, "以 JSON 输出")
	if err := fs.Parse(reorder(fs, args)); err != nil {
		return 2
	}

	cur := *current
	if cur == "" {
		cur = promptSecret("当前密码：")
	}
	next := *password
	if next == "" {
		next = promptSecret("新密码（至少 8 位）：")
		again := promptSecret("再输一次：")
		if next != again {
			return fail(fmt.Errorf("两次输入的密码不一致"))
		}
	}
	if len(next) < 8 {
		return fail(fmt.Errorf("新密码至少 8 位"))
	}

	c, err := g.apiClient()
	if err != nil {
		return fail(err)
	}
	ctx, cancel := ctx()
	defer cancel()
	if err := c.SetAdminPassword(ctx, cur, next); err != nil {
		return fail(err)
	}
	if *jsonOut {
		return printJSON(map[string]any{"ok": true})
	}
	fmt.Println("管理员密码已更新。")
	return 0
}

func runAdminToken(g globals, args []string) int {
	fs := newFlagSet("admin token", os.Stdout)
	show := fs.Bool("show", false, "只显示当前 Token，不轮换")
	yes := fs.Bool("yes", false, "跳过确认")
	if err := fs.Parse(reorder(fs, args)); err != nil {
		return 2
	}

	c, err := g.apiClient()
	if err != nil {
		return fail(err)
	}
	ctx, cancel := ctx()
	defer cancel()

	if *show {
		settings, err := c.Settings(ctx)
		if err != nil {
			return fail(err)
		}
		if panel, ok := settings["panel"].(map[string]any); ok {
			fmt.Println(formatValue(panel["api_token"]))
			return 0
		}
		return fail(fmt.Errorf("读取 Token 失败"))
	}

	if !*yes {
		fmt.Print("轮换后旧的 Token 立即失效，CLI/TUI 需要重新读取配置。继续？[y/N] ")
		if !confirm() {
			fmt.Println("已取消。")
			return 0
		}
	}
	token, err := c.RotateToken(ctx)
	if err != nil {
		return fail(err)
	}
	fmt.Printf("新的 API Token：%s\n", token)
	fmt.Println("（已写入 config.yaml，TUI 会自动读取）")
	return 0
}

func promptSecret(prompt string) string {
	fmt.Print(prompt)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		raw, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(raw))
	}
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line)
}

func confirm() bool {
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes", "是":
		return true
	default:
		return false
	}
}

// --- core ----------------------------------------------------------------

func runCore(g globals, args []string) int {
	if len(args) == 0 {
		fmt.Print(help.CommandText("core"))
		return 0
	}
	c, err := g.apiClient()
	if err != nil {
		return fail(err)
	}
	ctx, cancel := ctx()
	defer cancel()

	switch args[0] {
	case "restart":
		if err := c.RestartCore(ctx); err != nil {
			return fail(err)
		}
		fmt.Println("内核已重启。")
		return 0
	case "config":
		text, err := c.CoreConfig(ctx)
		if err != nil {
			return fail(err)
		}
		fmt.Println(text)
		return 0
	default:
		fmt.Print(help.CommandText("core"))
		return 2
	}
}
