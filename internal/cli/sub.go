package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ldm0206/vless-to-http/internal/client"
	"github.com/ldm0206/vless-to-http/internal/help"
)

func runSub(g globals, args []string) int {
	if len(args) == 0 {
		fmt.Print(help.CommandText("sub"))
		return 0
	}
	switch args[0] {
	case "list", "ls":
		return runSubList(g, args[1:])
	case "add":
		return runSubAdd(g, args[1:])
	case "edit":
		return runSubEdit(g, args[1:])
	case "rm", "remove", "delete":
		return runSubRemove(g, args[1:])
	case "update", "refresh":
		return runSubUpdate(g, args[1:])
	case "nodes":
		return runSubNodes(g, args[1:])
	case "import":
		return runSubImport(g, args[1:])
	default:
		fmt.Print(help.CommandText("sub"))
		return 2
	}
}

func runSubList(g globals, args []string) int {
	fs := newFlagSet("sub list", os.Stdout)
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

	subs, err := c.Subs(ctx)
	if err != nil {
		return fail(err)
	}
	if *jsonOut {
		return printJSON(map[string]any{"subs": subs})
	}
	if len(subs) == 0 {
		fmt.Println("还没有订阅。用 v2h sub add 添加，例如：")
		fmt.Println(`  v2h sub add "机场A" --url "https://example.com/sub?token=xxx"`)
		return 0
	}
	t := newTable("订阅", "格式", "状态", "节点", "可用", "更新间隔", "上次更新")
	for _, s := range subs {
		status := "正常"
		switch {
		case s.Error != "":
			status = "失败：" + s.Error
		case s.LastStatus == "" || s.LastStatus == "never":
			status = "尚未更新"
		case !s.Enabled:
			status = "已停用"
		}
		updated := "从未"
		if !s.LastUpdate.IsZero() {
			updated = s.LastUpdate.Format("2006-01-02 15:04")
		}
		interval := s.Interval.String()
		if s.URL == "" {
			interval = "仅本地"
		}
		t.add(s.Name, client.SubModeLabel(s.Kind), status, fmt.Sprint(s.CachedNodes),
			fmt.Sprint(s.CachedUsable), interval, updated)
	}
	t.render(os.Stdout)
	return 0
}

func runSubAdd(g globals, args []string) int {
	fs := newFlagSet("sub add", os.Stdout)
	url := fs.String("url", "", "订阅链接")
	kind := fs.String("kind", "auto", "格式：auto/clash/v2ray")
	interval := fs.String("interval", "12h", "自动更新间隔，例如 12h")
	userAgent := fs.String("user-agent", "", "自定义 User-Agent")
	noRefresh := fs.Bool("no-refresh", false, "添加后不立即拉取")
	jsonOut := fs.Bool("json", g.json, "以 JSON 输出")
	if err := fs.Parse(reorder(fs, args)); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		return fail(errors.New("用法：v2h sub add <名称> --url <链接>"))
	}
	if *url == "" {
		return fail(errors.New("必须用 --url 指定订阅链接；本地内容请用 v2h sub import"))
	}

	c, err := g.apiClient()
	if err != nil {
		return fail(err)
	}
	ctx, cancel := ctx()
	defer cancel()

	body := map[string]any{
		"name":       fs.Arg(0),
		"url":        *url,
		"kind":       *kind,
		"interval":   *interval,
		"user_agent": *userAgent,
		"refresh":    !*noRefresh,
	}
	sub, err := c.CreateSub(ctx, body)
	if err != nil {
		return fail(err)
	}
	if *jsonOut {
		return printJSON(sub)
	}
	fmt.Printf("已添加订阅 %s（%s）。\n", sub.Name, client.SubModeLabel(sub.Kind))
	if !*noRefresh {
		fmt.Println("正在后台拉取节点，稍后可用 v2h sub list 查看结果。")
	}
	return 0
}

func runSubEdit(g globals, args []string) int {
	fs := newFlagSet("sub edit", os.Stdout)
	name := fs.String("name", "", "改名")
	url := fs.String("url", "", "新的订阅链接")
	kind := fs.String("kind", "", "格式：auto/clash/v2ray")
	interval := fs.String("interval", "", "自动更新间隔")
	userAgent := fs.String("user-agent", "", "自定义 User-Agent")
	enable := fs.Bool("enable", false, "启用")
	disable := fs.Bool("disable", false, "停用")
	jsonOut := fs.Bool("json", g.json, "以 JSON 输出")
	if err := fs.Parse(reorder(fs, args)); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		return fail(errors.New("用法：v2h sub edit <订阅> [--url …] [--interval 6h]"))
	}

	body := map[string]any{}
	if *name != "" {
		body["name"] = *name
	}
	if *url != "" {
		body["url"] = *url
	}
	if *kind != "" {
		body["kind"] = *kind
	}
	if *interval != "" {
		body["interval"] = *interval
	}
	if *userAgent != "" {
		body["user_agent"] = *userAgent
	}
	if *enable {
		body["enabled"] = true
	}
	if *disable {
		body["enabled"] = false
	}
	if len(body) == 0 {
		return fail(errors.New("没有需要修改的内容"))
	}

	c, err := g.apiClient()
	if err != nil {
		return fail(err)
	}
	ctx, cancel := ctx()
	defer cancel()

	sub, err := c.UpdateSub(ctx, fs.Arg(0), body)
	if err != nil {
		return fail(err)
	}
	if *jsonOut {
		return printJSON(sub)
	}
	fmt.Printf("订阅 %s 已更新。\n", sub.Name)
	return 0
}

func runSubRemove(g globals, args []string) int {
	fs := newFlagSet("sub rm", os.Stdout)
	yes := fs.Bool("yes", false, "跳过确认")
	if err := fs.Parse(reorder(fs, args)); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		return fail(errors.New("用法：v2h sub rm <订阅>"))
	}

	target := fs.Arg(0)
	if !*yes {
		fmt.Printf("删除订阅 %s？引用它的账号会失去对应目标。[y/N] ", target)
		if !confirm() {
			fmt.Println("已取消。")
			return 0
		}
	}

	c, err := g.apiClient()
	if err != nil {
		return fail(err)
	}
	ctx, cancel := ctx()
	defer cancel()

	if err := c.DeleteSub(ctx, target); err != nil {
		return fail(err)
	}
	fmt.Printf("已删除订阅 %s。\n", target)
	return 0
}

func runSubUpdate(g globals, args []string) int {
	fs := newFlagSet("sub update", os.Stdout)
	all := fs.Bool("all", false, "更新全部订阅")
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

	targets := fs.Args()
	if *all || len(targets) == 0 {
		subs, err := c.Subs(ctx)
		if err != nil {
			return fail(err)
		}
		targets = targets[:0]
		for _, s := range subs {
			if s.Enabled && s.URL != "" {
				targets = append(targets, s.ID)
			}
		}
		if len(targets) == 0 {
			fmt.Println("没有可更新的订阅。")
			return 0
		}
	}

	type result struct {
		Name   string `json:"name"`
		Nodes  int    `json:"nodes"`
		Usable int    `json:"usable"`
		Error  string `json:"error,omitempty"`
	}
	results := make([]result, 0, len(targets))
	for _, target := range targets {
		sub, err := c.RefreshSub(ctx, target)
		item := result{}
		if sub != nil {
			item.Name = sub.Name
			item.Nodes = sub.CachedNodes
			item.Usable = sub.CachedUsable
		}
		if err != nil {
			item.Error = err.Error()
			if item.Name == "" {
				item.Name = target
			}
		}
		results = append(results, item)
	}

	if *jsonOut {
		return printJSON(map[string]any{"results": results})
	}
	for _, r := range results {
		if r.Error != "" {
			fmt.Printf("%s：失败（%s）\n", r.Name, r.Error)
			continue
		}
		fmt.Printf("%s：%d 个节点（可用 %d）\n", r.Name, r.Nodes, r.Usable)
	}
	return 0
}

func runSubNodes(g globals, args []string) int {
	fs := newFlagSet("sub nodes", os.Stdout)
	typ := fs.String("type", "", "只看某种协议")
	jsonOut := fs.Bool("json", g.json, "以 JSON 输出")
	if err := fs.Parse(reorder(fs, args)); err != nil {
		return 2
	}
	sub := ""
	if fs.NArg() > 0 {
		sub = fs.Arg(0)
	}

	return runNodeList(g, mergeNodeArgs(sub, *typ, *jsonOut))
}

func mergeNodeArgs(sub, typ string, jsonOut bool) []string {
	args := []string{}
	if sub != "" {
		args = append(args, "--sub", sub)
	}
	if typ != "" {
		args = append(args, "--type", typ)
	}
	if jsonOut {
		args = append(args, "--json")
	}
	return args
}

func runSubImport(g globals, args []string) int {
	fs := newFlagSet("sub import", os.Stdout)
	file := fs.String("file", "", "从文件读取；不填则读取标准输入")
	kind := fs.String("kind", "auto", "格式：auto/clash/v2ray")
	jsonOut := fs.Bool("json", g.json, "以 JSON 输出")
	if err := fs.Parse(reorder(fs, args)); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		return fail(errors.New("用法：v2h sub import <名称> --file ./nodes.yaml"))
	}

	var content []byte
	var err error
	if *file != "" {
		content, err = os.ReadFile(*file)
		if err != nil {
			return fail(err)
		}
	} else {
		content, err = readAllStdin()
		if err != nil {
			return fail(err)
		}
	}
	if len(strings.TrimSpace(string(content))) == 0 {
		return fail(errors.New("内容为空"))
	}

	c, err := g.apiClient()
	if err != nil {
		return fail(err)
	}
	ctx, cancel := ctx()
	defer cancel()

	sub, err := c.ImportSub(ctx, fs.Arg(0), *kind, string(content))
	if err != nil {
		return fail(err)
	}
	if *jsonOut {
		return printJSON(sub)
	}
	fmt.Printf("已导入订阅 %s：%d 个节点（可用 %d）。\n", sub.Name, sub.CachedNodes, sub.CachedUsable)
	return 0
}

func readAllStdin() ([]byte, error) {
	info, err := os.Stdin.Stat()
	if err == nil && info.Mode()&os.ModeCharDevice != 0 {
		return nil, errors.New("没有从标准输入读到内容，请用 --file 指定文件")
	}
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
		if len(buf) > 16<<20 {
			return nil, errors.New("内容过大")
		}
	}
	return buf, nil
}
