package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/ldm0206/vless-to-http/internal/api"
	"github.com/ldm0206/vless-to-http/internal/client"
	"github.com/ldm0206/vless-to-http/internal/config"
	"github.com/ldm0206/vless-to-http/internal/engine"
	"github.com/ldm0206/vless-to-http/internal/health"
	"github.com/ldm0206/vless-to-http/internal/help"
	"github.com/ldm0206/vless-to-http/internal/logs"
	"github.com/ldm0206/vless-to-http/internal/subscription"
	"github.com/ldm0206/vless-to-http/internal/tui"
	"github.com/ldm0206/vless-to-http/internal/version"
)

// Short aliases keep the command bodies readable.
type (
	engineNodeStatus = engine.NodeStatus
	healthState      = health.State
)

// runServe boots the proxy core, the panel and every background loop.
func runServe(g globals, args []string) int {
	fs := newFlagSet("serve", os.Stdout)
	listen := fs.String("listen", "", "覆盖面板监听地址，例如 0.0.0.0:9080")
	logLevel := fs.String("log-level", "", "覆盖日志级别：debug/info/warning/error/none")
	openTUI := fs.Bool("tui", false, "启动后在当前终端打开控制台")
	if err := fs.Parse(reorder(fs, args)); err != nil {
		return 2
	}

	dataDir := g.dataDirResolved()
	store := config.NewStore(dataDir)
	boot, err := store.Load()
	if err != nil {
		return fail(err)
	}

	if *listen != "" || *logLevel != "" {
		if err := store.Update(func(c *config.Config) error {
			if *listen != "" {
				c.Panel.Listen = *listen
			}
			if *logLevel != "" {
				c.Logs.Level = *logLevel
			}
			return nil
		}); err != nil {
			return fail(err)
		}
	}

	cfg := store.Get()
	logger, err := logs.New(logs.Options{
		Level:      logs.ParseLevel(cfg.Logs.Level),
		AccessLog:  cfg.Logs.AccessLog,
		Dir:        cfg.LogDir(dataDir),
		MaxSizeMB:  cfg.Logs.MaxSizeMB,
		MaxBackups: cfg.Logs.MaxBackups,
		RingSize:   cfg.Logs.RingSize,
		Console:    cfg.Logs.Console,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "警告："+err.Error())
	}
	defer logger.Close()

	cache := subscription.NewCache(dataDir)
	if err := cache.Load(); err != nil {
		logger.Warnf("读取订阅缓存失败：%v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	eng := engine.New(store, cache, logger, dataDir)
	server, err := api.New(eng, logger)
	if err != nil {
		return fail(err)
	}

	printBanner(store, boot, logger)

	go func() {
		if err := server.ListenAndServe(ctx, cfg.Panel.Listen); err != nil && !errors.Is(err, context.Canceled) {
			logger.Errorf("面板服务退出：%v", err)
			stop()
		}
	}()

	if *openTUI {
		go func() {
			if err := tui.Run(ctx, tui.Options{
				BaseURL: cfg.Panel.Listen,
				Token:   cfg.Panel.APIToken,
			}); err != nil && !errors.Is(err, context.Canceled) {
				logger.Warnf("控制台退出：%v", err)
			}
		}()
	}

	if err := eng.Run(ctx); err != nil {
		return fail(err)
	}
	return 0
}

func printBanner(store *config.Store, boot *config.Bootstrap, logger *logs.Logger) {
	cfg := store.Get()

	fmt.Println()
	fmt.Printf("v2h %s  ·  Xray %s\n\n", version.Full(), version.Xray())
	fmt.Printf("  数据目录     %s\n", store.DataDir())
	fmt.Printf("  配置文件     %s\n", store.Path())
	if cfg.Proxy.HTTP.Enabled {
		fmt.Printf("  HTTP 代理    %s\n", cfg.Proxy.HTTP.Listen)
	}
	if cfg.Proxy.SOCKS.Enabled {
		udp := ""
		if cfg.Proxy.SOCKS.UDP {
			udp = "（含 UDP）"
		}
		fmt.Printf("  SOCKS5 代理  %s%s\n", cfg.Proxy.SOCKS.Listen, udp)
	}
	fmt.Printf("  控制面板     http://%s\n", displayListen(cfg.Panel.Listen))
	fmt.Printf("  管理员账号   %s\n", cfg.Panel.Admin.Username)

	if boot.AdminPassword != "" {
		fmt.Printf("  管理员密码   %s\n", boot.AdminPassword)
		fmt.Println("               ↑ 只显示这一次，请立即保存")
	} else if boot.Created {
		fmt.Println("  管理员密码   已通过 V2H_ADMIN_PASSWORD 设置")
	} else {
		fmt.Println("  管理员密码   沿用配置文件里的旧密码")
	}

	if cfg.Panel.Listen == "0.0.0.0:9080" || strings.HasPrefix(cfg.Panel.Listen, "0.0.0.0") {
		if !cfg.Panel.Admin.Turnstile.Enabled {
			fmt.Println()
			fmt.Println("  提示：面板监听在所有网卡上，建议在「设置」里开启 Cloudflare Turnstile，")
			fmt.Println("        或改为只监听 127.0.0.1 并通过反向代理/隧道访问。")
		}
	}
	fmt.Println()

	if boot.Created {
		logger.Infof("已生成默认配置：%s", store.Path())
	}
}

func displayListen(listen string) string {
	if strings.HasPrefix(listen, "0.0.0.0:") {
		return "127.0.0.1:" + strings.TrimPrefix(listen, "0.0.0.0:")
	}
	return listen
}

// --- status --------------------------------------------------------------

func runStatus(g globals, args []string) int {
	fs := newFlagSet("status", os.Stdout)
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

	resp, err := c.Status(ctx)
	if err != nil {
		return fail(err)
	}
	if *jsonOut {
		return printJSON(resp)
	}

	st := resp.Status
	state := "运行中"
	if !st.Running {
		state = "未运行"
	}
	fmt.Printf("内核状态    %s（已重启 %d 次，运行 %s）\n", state, st.Restarts, humanDuration(time.Duration(st.UptimeSec)*time.Second))
	if st.LastError != "" {
		fmt.Printf("最近错误    %s\n", st.LastError)
	}
	fmt.Printf("版本        v2h %v · Xray %v\n", resp.Version["version"], resp.Version["xray"])
	fmt.Printf("HTTP 代理   %v\n", resp.Proxy["http"])
	fmt.Printf("SOCKS5      %v\n", resp.Proxy["socks"])
	fmt.Printf("兜底策略    %v\n", fallbackLabel(fmt.Sprint(resp.Proxy["fallback"])))
	fmt.Printf("累计流量    上行 %s / 下行 %s\n", client.FormatBytes(st.Totals.Up), client.FormatBytes(st.Totals.Down))
	fmt.Println()

	if len(st.Users) == 0 {
		fmt.Println("还没有账号。用 v2h user add <名字> --password <密码> --target \"订阅:节点\" 创建。")
	} else {
		t := newTable("账号", "状态", "出口", "上行", "下行", "在线")
		for _, u := range st.Users {
			status := "启用"
			if !u.Enabled {
				status = "停用"
			}
			t.add(u.Name, status, client.UserStatusSummary(u),
				client.FormatBytes(u.Traffic.Up), client.FormatBytes(u.Traffic.Down),
				fmt.Sprintf("%d", u.Online))
		}
		t.render(os.Stdout)
	}

	if len(st.Subs) > 0 {
		fmt.Println()
		t := newTable("订阅", "格式", "节点", "可用", "上次更新", "状态")
		for _, s := range st.Subs {
			updated := "从未"
			if !s.LastUpdate.IsZero() {
				updated = s.LastUpdate.Format("01-02 15:04")
			}
			status := s.LastStatus
			if s.Error != "" {
				status = "失败：" + s.Error
			} else if status == "ok" {
				status = "正常"
			}
			t.add(s.Name, client.SubModeLabel(s.Kind), fmt.Sprint(s.CachedNodes),
				fmt.Sprint(s.CachedUsable), updated, status)
		}
		t.render(os.Stdout)
	}
	return 0
}

func fallbackLabel(v string) string {
	switch v {
	case "direct":
		return "直连"
	case "reject":
		return "拒绝连接"
	default:
		return v
	}
}

func humanDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%d 分 %d 秒", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%d 小时 %d 分", int(d.Hours()), int(d.Minutes())%60)
}

func printJSON(v any) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fail(err)
	}
	return 0
}

// --- logs ----------------------------------------------------------------

func runLogs(g globals, args []string) int {
	fs := newFlagSet("logs", os.Stdout)
	follow := fs.Bool("follow", false, "持续输出新日志")
	fs.BoolVar(follow, "f", *follow, "持续输出新日志（简写）")
	level := fs.String("level", "", "只显示该级别及以上：debug/info/warning/error")
	user := fs.String("user", "", "只看某个账号的连接")
	query := fs.String("query", "", "按关键字过滤")
	lines := fs.Int("lines", 50, "显示最近多少行")
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

	entries, last, err := c.Logs(ctx, 0, *lines, *level, *user, *query)
	if err != nil {
		return fail(err)
	}
	for _, e := range entries {
		printLogEntry(e, *jsonOut)
	}
	if !*follow {
		return 0
	}

	streamCtx, streamCancel := context.WithCancel(ctx)
	defer streamCancel()
	events, err := c.Watch(streamCtx)
	if err != nil {
		return fail(err)
	}
	since := last
	fmt.Fprintln(os.Stderr, "-- 实时日志，按 Ctrl+C 退出 --")
	for event := range events {
		if event.Name != "log" {
			continue
		}
		var entry logs.Entry
		if err := json.Unmarshal(event.Data, &entry); err != nil {
			continue
		}
		if entry.Seq <= since {
			continue
		}
		since = entry.Seq
		if *level != "" && levelRank(entry.Level) < levelRank(*level) {
			continue
		}
		if *user != "" && entry.User != *user {
			continue
		}
		printLogEntry(entry, *jsonOut)
	}
	return 0
}

func printLogEntry(e logs.Entry, asJSON bool) {
	if asJSON {
		raw, _ := json.Marshal(e)
		fmt.Println(string(raw))
		return
	}
	user := ""
	if e.User != "" {
		user = " [" + e.User + "]"
	}
	fmt.Printf("%s %-7s %-6s%s %s\n",
		e.Time.Format("01-02 15:04:05"), e.Level, e.Source, user, e.Msg)
}

func levelRank(level string) int {
	switch level {
	case "debug":
		return 0
	case "info":
		return 1
	case "warning", "warn":
		return 2
	case "error":
		return 3
	default:
		return 1
	}
}

// --- node ----------------------------------------------------------------

func runNode(g globals, args []string) int {
	if len(args) == 0 {
		fmt.Print(help.CommandText("node"))
		return 0
	}
	switch args[0] {
	case "list", "ls":
		return runNodeList(g, args[1:])
	case "test":
		return runNodeTest(g, args[1:])
	default:
		fmt.Print(help.CommandText("node"))
		return 2
	}
}

func runNodeList(g globals, args []string) int {
	fs := newFlagSet("node list", os.Stdout)
	sub := fs.String("sub", "", "只看某个订阅")
	typ := fs.String("type", "", "只看某种协议，例如 vless")
	onlyUsable := fs.Bool("usable", false, "隐藏 Xray 不支持的节点")
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

	nodes, err := c.Nodes(ctx, *sub)
	if err != nil {
		return fail(err)
	}
	if *jsonOut {
		return printJSON(map[string]any{"nodes": nodes})
	}

	t := newTable("节点", "协议", "地址", "订阅", "状态", "延迟", "使用者")
	shown := 0
	for _, n := range nodes {
		if *typ != "" && !strings.EqualFold(n.Type, *typ) {
			continue
		}
		if *onlyUsable && n.Unsupported != "" {
			continue
		}
		status, latency := nodeState(n)
		users := "-"
		if len(n.UsedBy) > 0 {
			users = strings.Join(n.UsedBy, ",")
		}
		t.add(n.Name, n.Type, fmt.Sprintf("%s:%d", n.Server, n.Port), n.SubName, status, latency, users)
		shown++
	}
	if shown == 0 {
		fmt.Println("没有匹配的节点。用 v2h sub add 添加订阅，或 v2h sub update 立即更新。")
		return 0
	}
	t.render(os.Stdout)
	return 0
}

func nodeState(n engineNodeStatus) (string, string) {
	if n.Unsupported != "" {
		return n.Unsupported, "-"
	}
	if !n.Health.Checked {
		return "未测试", "-"
	}
	if !n.Health.Alive {
		return "不可用", "-"
	}
	return "正常", fmt.Sprintf("%d ms", n.Health.Latency)
}

func runNodeTest(g globals, args []string) int {
	fs := newFlagSet("node test", os.Stdout)
	sub := fs.String("sub", "", "测试整个订阅")
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

	var results map[string]healthState
	if *sub != "" {
		results, err = c.TestSub(ctx, *sub)
	} else {
		names := fs.Args()
		if len(names) == 0 {
			return fail(errors.New("请指定节点名，或用 --sub 测试整个订阅"))
		}
		nodes, err := c.Nodes(ctx, "")
		if err != nil {
			return fail(err)
		}
		ids := make([]string, 0, len(names))
		for _, want := range names {
			found := false
			for _, n := range nodes {
				if n.Name == want || n.ID == want {
					ids = append(ids, n.ID)
					found = true
					break
				}
			}
			if !found {
				return fail(fmt.Errorf("找不到节点 %q", want))
			}
		}
		results, err = c.TestNodes(ctx, ids)
		if err != nil {
			return fail(err)
		}
		if *jsonOut {
			return printJSON(results)
		}
		printTestResults(results, nodes)
		return 0
	}
	if err != nil {
		return fail(err)
	}
	if *jsonOut {
		return printJSON(results)
	}
	nodes, _ := c.Nodes(ctx, *sub)
	printTestResults(results, nodes)
	return 0
}

// truncateText shortens a message to roughly n display columns.
func truncateText(s string, n int) string {
	if displayWidth(s) <= n {
		return s
	}
	out := make([]rune, 0, n)
	width := 0
	for _, r := range s {
		w := runeWidth(r)
		if width+w > n-1 {
			break
		}
		out = append(out, r)
		width += w
	}
	return string(out) + "…"
}

func printTestResults(results map[string]healthState, nodes []engineNodeStatus) {
	names := map[string]string{}
	for _, n := range nodes {
		names[n.ID] = n.Name
	}
	t := newTable("节点", "结果", "延迟")
	ids := make([]string, 0, len(results))
	for id := range results {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		r := results[id]
		name := names[id]
		if name == "" {
			name = id
		}
		if r.Alive {
			t.add(name, "可用", fmt.Sprintf("%d ms", r.Latency))
		} else {
			reason := r.LastError
			if reason == "" {
				reason = "不可用"
			}
			// Transport errors run to hundreds of characters; keep the row
			// readable and leave the full text in the panel.
			t.add(name, "失败", truncateText(reason, 96))
		}
	}
	t.render(os.Stdout)
}
