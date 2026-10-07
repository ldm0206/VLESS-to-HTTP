package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ldm0206/vless-to-http/internal/client"
	"github.com/ldm0206/vless-to-http/internal/config"
	"github.com/ldm0206/vless-to-http/internal/engine"
	"github.com/ldm0206/vless-to-http/internal/help"
)

func runUser(g globals, args []string) int {
	if len(args) == 0 {
		fmt.Print(help.CommandText("user"))
		return 0
	}
	switch args[0] {
	case "list", "ls":
		return runUserList(g, args[1:])
	case "show":
		return runUserShow(g, args[1:])
	case "add", "create":
		return runUserAdd(g, args[1:])
	case "edit":
		return runUserEdit(g, args[1:])
	case "rm", "remove", "delete":
		return runUserRemove(g, args[1:])
	case "enable", "disable":
		return runUserToggle(g, args[0] == "enable", args[1:])
	case "passwd", "password":
		return runUserPassword(g, args[1:])
	case "targets":
		return runUserTargets(g, args[1:])
	default:
		fmt.Print(help.CommandText("user"))
		return 2
	}
}

func runUserList(g globals, args []string) int {
	fs := newFlagSet("user list", os.Stdout)
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

	users, err := c.Users(ctx)
	if err != nil {
		return fail(err)
	}
	if *jsonOut {
		return printJSON(map[string]any{"users": users})
	}
	if len(users) == 0 {
		fmt.Println("还没有账号。")
		return 0
	}
	t := newTable("账号", "状态", "模式", "兜底", "密码", "出口", "上行", "下行")
	for _, u := range users {
		status := "启用"
		if !u.Enabled {
			status = "停用"
		}
		t.add(u.Name, status, modeLabel(u.Mode), fallbackLabel(u.Fallback), u.Password,
			client.UserStatusSummary(u), client.FormatBytes(u.Traffic.Up), client.FormatBytes(u.Traffic.Down))
	}
	t.render(os.Stdout)
	return 0
}

func modeLabel(mode string) string {
	switch mode {
	case config.ModeAuto:
		return "自动切换"
	case config.ModeFixed:
		return "固定节点"
	default:
		return "按序优先"
	}
}

func runUserShow(g globals, args []string) int {
	fs := newFlagSet("user show", os.Stdout)
	jsonOut := fs.Bool("json", g.json, "以 JSON 输出")
	if err := fs.Parse(reorder(fs, args)); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		return fail(errors.New("用法：v2h user show <账号>"))
	}

	c, err := g.apiClient()
	if err != nil {
		return fail(err)
	}
	ctx, cancel := ctx()
	defer cancel()

	user, err := c.User(ctx, fs.Arg(0))
	if err != nil {
		return fail(err)
	}
	if *jsonOut {
		return printJSON(user)
	}

	fmt.Printf("账号        %s\n", user.Name)
	fmt.Printf("状态        %s\n", enabledLabel(user.Enabled))
	fmt.Printf("密码        %s\n", user.Password)
	fmt.Printf("选路模式    %s\n", modeLabel(user.Mode))
	fmt.Printf("兜底策略    %s\n", fallbackLabel(user.Fallback))
	if user.Note != "" {
		fmt.Printf("备注        %s\n", user.Note)
	}
	fmt.Printf("累计流量    上行 %s / 下行 %s\n", client.FormatBytes(user.Traffic.Up), client.FormatBytes(user.Traffic.Down))
	if user.Online > 0 {
		fmt.Printf("在线地址    %s\n", strings.Join(user.OnlineIPs, ", "))
	}
	fmt.Printf("当前出口    %s\n", client.UserStatusSummary(*user))

	if len(user.RawTargets) > 0 {
		fmt.Println("\n目标（按优先级）：")
		for i, t := range user.RawTargets {
			desc := t.Node
			if t.All {
				desc = "整条订阅"
				if t.Limit > 0 {
					desc = fmt.Sprintf("整条订阅（最多 %d 个）", t.Limit)
				}
			}
			fmt.Printf("  %d. %s · %s\n", i+1, subLabel(user, t.Sub), desc)
		}
	}
	if len(user.Missing) > 0 {
		fmt.Println("\n失效的目标：")
		for _, m := range user.Missing {
			fmt.Printf("  - %s\n", m)
		}
	}
	return 0
}

func subLabel(user *engine.UserStatus, subID string) string {
	for _, t := range user.Targets {
		if t.Sub == subID && t.SubName != "" {
			return t.SubName
		}
	}
	if subID == "" {
		return "任意订阅"
	}
	return subID
}

func enabledLabel(enabled bool) string {
	if enabled {
		return "启用"
	}
	return "停用"
}

func runUserAdd(g globals, args []string) int {
	fs := newFlagSet("user add", os.Stdout)
	password := fs.String("password", "", "代理密码（留空自动生成）")
	mode := fs.String("mode", config.ModePriority, "选路模式：priority/auto/fixed")
	fallback := fs.String("fallback", config.FallbackInherit, "兜底策略：inherit/direct/reject")
	note := fs.String("note", "", "备注")
	disabled := fs.Bool("disabled", false, "创建后先停用")
	targets := multiFlag{}
	fs.Var(&targets, "target", "目标，可重复：\"订阅:节点\" 或 \"订阅:*\"")
	jsonOut := fs.Bool("json", g.json, "以 JSON 输出")
	if err := fs.Parse(reorder(fs, args)); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		return fail(errors.New("用法：v2h user add <账号> --password <密码> --target \"订阅:节点\""))
	}

	name := fs.Arg(0)
	c, err := g.apiClient()
	if err != nil {
		return fail(err)
	}
	ctx, cancel := ctx()
	defer cancel()

	parsed, err := resolveTargets(ctx, c, targets)
	if err != nil {
		return fail(err)
	}

	body := map[string]any{
		"name":     name,
		"mode":     *mode,
		"fallback": *fallback,
		"note":     *note,
		"enabled":  !*disabled,
		"targets":  parsed,
	}
	if *password != "" {
		body["password"] = *password
	}

	user, err := c.CreateUser(ctx, body)
	if err != nil {
		return fail(err)
	}
	if *jsonOut {
		return printJSON(user)
	}
	fmt.Printf("已创建账号 %s\n", user.Name)
	fmt.Printf("  密码   %s\n", user.Password)
	fmt.Printf("  模式   %s\n", modeLabel(user.Mode))
	fmt.Printf("  出口   %s\n", client.UserStatusSummary(*user))
	if user.Password != "" && *password == "" {
		fmt.Println("\n这是自动生成的密码，请保存好。")
	}
	return 0
}

func runUserEdit(g globals, args []string) int {
	fs := newFlagSet("user edit", os.Stdout)
	name := fs.String("name", "", "改名")
	mode := fs.String("mode", "", "选路模式：priority/auto/fixed")
	fallback := fs.String("fallback", "", "兜底策略：inherit/direct/reject")
	note := fs.String("note", "", "备注")
	enable := fs.Bool("enable", false, "启用")
	disable := fs.Bool("disable", false, "停用")
	jsonOut := fs.Bool("json", g.json, "以 JSON 输出")
	if err := fs.Parse(reorder(fs, args)); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		return fail(errors.New("用法：v2h user edit <账号> [--mode auto] [--fallback direct] …"))
	}

	c, err := g.apiClient()
	if err != nil {
		return fail(err)
	}
	ctx, cancel := ctx()
	defer cancel()

	body := map[string]any{}
	if *name != "" {
		body["name"] = *name
	}
	if *mode != "" {
		body["mode"] = *mode
	}
	if *fallback != "" {
		body["fallback"] = *fallback
	}
	if *note != "" {
		body["note"] = *note
	}
	changed := false
	if *enable && *disable {
		return fail(errors.New("--enable 和 --disable 不能同时使用"))
	}
	if *enable {
		body["enabled"] = true
		changed = true
	}
	if *disable {
		body["enabled"] = false
		changed = true
	}
	_ = changed

	if len(body) == 0 {
		return fail(errors.New("没有需要修改的内容"))
	}

	user, err := c.UpdateUser(ctx, fs.Arg(0), body)
	if err != nil {
		return fail(err)
	}
	if *jsonOut {
		return printJSON(user)
	}
	fmt.Printf("账号 %s 已更新：%s · %s\n", user.Name, modeLabel(user.Mode), client.UserStatusSummary(*user))
	return 0
}

func runUserRemove(g globals, args []string) int {
	fs := newFlagSet("user rm", os.Stdout)
	yes := fs.Bool("yes", false, "跳过确认")
	if err := fs.Parse(reorder(fs, args)); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		return fail(errors.New("用法：v2h user rm <账号>"))
	}

	c, err := g.apiClient()
	if err != nil {
		return fail(err)
	}
	ctx, cancel := ctx()
	defer cancel()

	target := fs.Arg(0)
	if !*yes {
		fmt.Printf("删除账号 %s？该账号将立即断开。[y/N] ", target)
		if !confirm() {
			fmt.Println("已取消。")
			return 0
		}
	}
	if err := c.DeleteUser(ctx, target); err != nil {
		return fail(err)
	}
	fmt.Printf("已删除账号 %s。\n", target)
	return 0
}

func runUserToggle(g globals, enable bool, args []string) int {
	fs := newFlagSet("user enable", os.Stdout)
	if err := fs.Parse(reorder(fs, args)); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		return fail(errors.New("用法：v2h user enable|disable <账号>"))
	}

	c, err := g.apiClient()
	if err != nil {
		return fail(err)
	}
	ctx, cancel := ctx()
	defer cancel()

	user, err := c.UpdateUser(ctx, fs.Arg(0), map[string]any{"enabled": enable})
	if err != nil {
		return fail(err)
	}
	fmt.Printf("账号 %s 已%s。\n", user.Name, enabledLabel(enable))
	return 0
}

func runUserPassword(g globals, args []string) int {
	fs := newFlagSet("user passwd", os.Stdout)
	password := fs.String("password", "", "新密码")
	generate := fs.Bool("generate", false, "生成随机密码")
	jsonOut := fs.Bool("json", g.json, "以 JSON 输出")
	if err := fs.Parse(reorder(fs, args)); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		return fail(errors.New("用法：v2h user passwd <账号> [--generate]"))
	}

	next := *password
	if *generate {
		next = config.NewPassword(16)
	}
	if next == "" {
		next = promptSecret("新的代理密码：")
	}
	if next == "" {
		return fail(errors.New("密码不能为空"))
	}

	c, err := g.apiClient()
	if err != nil {
		return fail(err)
	}
	ctx, cancel := ctx()
	defer cancel()

	user, err := c.UpdateUser(ctx, fs.Arg(0), map[string]any{"password": next})
	if err != nil {
		return fail(err)
	}
	if *jsonOut {
		return printJSON(map[string]any{"name": user.Name, "password": next})
	}
	fmt.Printf("账号 %s 的新密码：%s\n", user.Name, next)
	return 0
}

func runUserTargets(g globals, args []string) int {
	fs := newFlagSet("user targets", os.Stdout)
	set := fs.String("set", "", "整组替换，逗号分隔：\"订阅:节点,订阅:*\"")
	add := multiFlag{}
	fs.Var(&add, "add", "追加一个目标，可重复")
	clear := fs.Bool("clear", false, "清空所有目标")
	jsonOut := fs.Bool("json", g.json, "以 JSON 输出")
	if err := fs.Parse(reorder(fs, args)); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		return fail(errors.New("用法：v2h user targets <账号> [--set \"订阅:节点\"] [--add \"订阅:*\"] [--clear]"))
	}

	c, err := g.apiClient()
	if err != nil {
		return fail(err)
	}
	ctx, cancel := ctx()
	defer cancel()

	name := fs.Arg(0)
	user, err := c.User(ctx, name)
	if err != nil {
		return fail(err)
	}

	var targets []map[string]any
	switch {
	case *clear:
		targets = []map[string]any{}
	case *set != "":
		var list multiFlag
		for _, part := range strings.Split(*set, ",") {
			if strings.TrimSpace(part) != "" {
				list = append(list, strings.TrimSpace(part))
			}
		}
		parsed, err := resolveTargets(ctx, c, list)
		if err != nil {
			return fail(err)
		}
		targets = parsed
	case len(add) > 0:
		parsed, err := resolveTargets(ctx, c, add)
		if err != nil {
			return fail(err)
		}
		existing := client.TargetsToBody(user.RawTargets)
		targets = append(existing, parsed...)
	default:
		if *jsonOut {
			return printJSON(map[string]any{"targets": user.RawTargets})
		}
		if len(user.RawTargets) == 0 {
			fmt.Println("该账号还没有设置目标。")
			return 0
		}
		for i, t := range user.RawTargets {
			desc := t.Node
			if t.All {
				desc = "整条订阅"
			}
			fmt.Printf("%d. %s · %s\n", i+1, t.Sub, desc)
		}
		return 0
	}

	updated, err := c.UpdateUser(ctx, name, map[string]any{"targets": targets})
	if err != nil {
		return fail(err)
	}
	if *jsonOut {
		return printJSON(updated)
	}
	fmt.Printf("账号 %s 的目标已更新，当前出口：%s\n", updated.Name, client.UserStatusSummary(*updated))
	return 0
}

// resolveTargets turns "订阅:节点" strings into API target objects, mapping
// subscription names onto their ids.
func resolveTargets(ctx context.Context, c *client.Client, specs []string) ([]map[string]any, error) {
	if len(specs) == 0 {
		return []map[string]any{}, nil
	}
	subs, err := c.Subs(ctx)
	if err != nil {
		return nil, err
	}

	lookup := func(name string) (string, string) {
		for _, s := range subs {
			if s.ID == name || s.Name == name {
				return s.ID, s.Name
			}
		}
		return "", ""
	}

	out := make([]map[string]any, 0, len(specs))
	for _, spec := range specs {
		subName, nodeName, found := strings.Cut(spec, ":")
		if !found {
			return nil, fmt.Errorf("目标 %q 格式不正确，应为 \"订阅名:节点名\" 或 \"订阅名:*\"", spec)
		}
		subID, display := lookup(strings.TrimSpace(subName))
		if subID == "" {
			return nil, fmt.Errorf("找不到订阅 %q", subName)
		}
		node := strings.TrimSpace(nodeName)
		if node == "*" || node == "" || strings.EqualFold(node, "all") {
			out = append(out, map[string]any{"sub": subID, "all": true})
			continue
		}
		out = append(out, map[string]any{"sub": subID, "node": node})
		_ = display
	}
	return out, nil
}

// multiFlag collects a repeatable flag.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }

func (m *multiFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}
