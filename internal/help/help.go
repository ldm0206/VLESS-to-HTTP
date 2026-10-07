// Package help is the single source of truth for the CLI help text, the TUI
// key bindings and the panel's help page.
package help

import (
	"fmt"
	"strings"
)

// Command documents one CLI subcommand.
type Command struct {
	Name     string   `json:"name"`
	Usage    string   `json:"usage"`
	Summary  string   `json:"summary"`
	Details  []string `json:"details,omitempty"`
	Examples []string `json:"examples,omitempty"`
}

// Key documents one TUI key binding.
type Key struct {
	Key    string `json:"key"`
	Action string `json:"action"`
}

// Commands is the full CLI surface.
var Commands = []Command{
	{
		Name:    "serve",
		Usage:   "v2h serve [--data DIR] [--listen ADDR] [--log-level LEVEL]",
		Summary: "启动代理内核和控制面板（容器里的默认命令）",
		Details: []string{
			"启动后 HTTP/SOCKS5 代理、Web 面板、后台订阅更新和健康检查都会同时运行。",
			"首次启动会自动生成 config.yaml，并在日志里打印一次管理员的随机密码。",
			"容器里用 docker exec -it <容器> v2h tui 可以打开同一个实例的界面。",
		},
		Examples: []string{
			"v2h serve",
			"v2h serve --data /data --listen 0.0.0.0:9080",
			"V2H_ADMIN_PASSWORD=my-secret v2h serve",
		},
	},
	{
		Name:    "tui",
		Usage:   "v2h tui [--api URL] [--token TOKEN]",
		Summary: "打开终端控制台，实时查看状态、账号、节点和日志",
		Details: []string{
			"TUI 通过本机 API 与运行中的服务通信，因此可以在容器外或 docker exec 里使用。",
			"默认读取 --data 目录下的 config.yaml 来获取面板地址和 API Token。",
		},
		Examples: []string{"v2h tui", "docker exec -it v2h v2h tui"},
	},
	{
		Name:    "status",
		Usage:   "v2h status [--json]",
		Summary: "显示内核状态、账号流量和节点健康概览",
		Examples: []string{"v2h status", "v2h status --json"},
	},
	{
		Name:    "user",
		Usage:   "v2h user <list|show|add|edit|rm|enable|disable|passwd|targets> [参数]",
		Summary: "管理代理账号，以及账号到服务器/订阅的映射",
		Details: []string{
			"每个账号是一个「用户名 + 密码」，客户端用这对凭据连接 SOCKS5 或 HTTP 代理。",
			"目标可以写成 订阅名:节点名，也可以写成 订阅名:* 表示整条订阅的全部节点。",
			"--mode 决定选路方式：",
			"  priority  按目标顺序选第一个可用节点（默认）",
			"  auto      交给内核自动测速，选延迟最低的可用节点",
			"  fixed     固定用第一个节点，不做健康切换",
			"--fallback 决定所有目标都不可用时怎么办：reject 拒绝连接（默认）或 direct 直连。",
		},
		Examples: []string{
			`v2h user add alice --password secret --target "机场A:香港01" --target "机场A:日本02"`,
			`v2h user add bob --password secret --target "机场A:*" --mode auto --fallback reject`,
			`v2h user targets alice --set "机场A:香港01,机场A:日本02"`,
			`v2h user passwd alice --generate`,
			`v2h user list`,
		},
	},
	{
		Name:    "sub",
		Usage:   "v2h sub <list|add|edit|rm|update|nodes|import> [参数]",
		Summary: "管理 Clash / v2ray 订阅",
		Details: []string{
			"支持 Clash(mihomo) YAML 和 base64 节点链接两种格式，--kind auto 会自动识别。",
			"更新时会自动跳过 Xray 内核不支持的节点（hysteria2、tuic、带插件的 ss 等），并在面板上标注原因。",
			"import 可以直接粘贴链接或本地文件，不会联网。",
		},
		Examples: []string{
			`v2h sub add "机场A" --url https://example.com/sub?token=xxx --interval 12h`,
			`v2h sub update 机场A`,
			`v2h sub nodes 机场A --type vless`,
			`v2h sub import "本地" --file ./my-nodes.yaml`,
		},
	},
	{
		Name:    "node",
		Usage:   "v2h node <list|test> [参数]",
		Summary: "查看节点列表并对节点测速",
		Details: []string{
			"test 会通过内核真实发起一次请求来测延迟（需要该节点已被某个账号使用），",
			"未接入内核的节点退化为 TCP 连接测试。",
		},
		Examples: []string{"v2h node list", "v2h node list --sub 机场A", "v2h node test 香港01"},
	},
	{
		Name:    "logs",
		Usage:   "v2h logs [--follow] [--level LEVEL] [--user NAME] [--lines N] [--query TEXT]",
		Summary: "查看运行日志，可实时跟随",
		Examples: []string{"v2h logs --lines 50", "v2h logs -f --user alice", "v2h logs --level error"},
	},
	{
		Name:    "config",
		Usage:   "v2h config <show|path|get|set> [键] [值]",
		Summary: "查看或修改面板、代理和日志配置",
		Details: []string{
			"常用键：panel.listen、proxy.http.listen、proxy.socks.listen、proxy.fallback、",
			"logs.level、logs.max_size_mb、logs.max_backups、logs.access_log、",
			"health.interval、health.probe_url、health.enabled。",
		},
		Examples: []string{
			"v2h config show",
			"v2h config set logs.max_size_mb 100",
			"v2h config set proxy.fallback direct",
		},
	},
	{
		Name:    "admin",
		Usage:   "v2h admin <passwd|token> [参数]",
		Summary: "修改面板管理员密码或轮换 API Token",
		Examples: []string{"v2h admin passwd", "v2h admin token"},
	},
	{
		Name:    "core",
		Usage:   "v2h core <restart|config>",
		Summary: "重启代理内核或打印当前生成的内核配置",
		Examples: []string{"v2h core restart", "v2h core config"},
	},
	{
		Name:    "help",
		Usage:   "v2h help [命令]",
		Summary: "显示帮助；带命令名时显示该命令的详细说明",
		Examples: []string{"v2h help", "v2h help user"},
	},
	{
		Name:    "version",
		Usage:   "v2h version",
		Summary: "显示版本信息",
	},
}

// TUIKeys is the key map shown in the TUI and on the help page.
var TUIKeys = []Key{
	{"1-6 / Tab", "在概览、账号、订阅、节点、日志、帮助之间切换"},
	{"↑ ↓ / j k", "上下移动光标"},
	{"g / G", "跳到首行 / 末行"},
	{"Enter", "查看选中项的详情"},
	{"t", "对选中的节点或该订阅全部节点测速"},
	{"r", "刷新订阅（订阅页）或重启内核（概览页）"},
	{"f", "日志页：切换实时跟随"},
	{"a", "日志页：切换等级过滤"},
	{"/", "日志页：输入关键字过滤"},
	{"?", "显示/隐藏帮助"},
	{"q", "退出（不会停止服务）"},
}

// GlobalFlags documents the flags every command accepts.
var GlobalFlags = []Key{
	{"--data DIR", "数据目录，默认为 /data（不可写时回落到 ./data）"},
	{"--api URL", "面板地址，默认从配置文件里读取"},
	{"--token TOKEN", "API Token，默认从配置文件里读取"},
	{"--json", "以 JSON 输出，便于脚本处理"},
	{"-h, --help", "显示帮助"},
}

// Text renders the full CLI help.
func Text() string {
	var b strings.Builder
	b.WriteString("v2h —— Clash 订阅驱动的主机代理控制面板\n\n")
	b.WriteString("用法：\n  v2h <命令> [参数]\n\n命令：\n")
	for _, c := range Commands {
		fmt.Fprintf(&b, "  %-10s %s\n", c.Name, c.Summary)
	}
	b.WriteString("\n全局参数：\n")
	for _, f := range GlobalFlags {
		fmt.Fprintf(&b, "  %-16s %s\n", f.Key, f.Action)
	}
	b.WriteString("\n用 v2h help <命令> 查看单个命令的详细说明和示例。\n")
	return b.String()
}

// CommandText renders the help for one command.
func CommandText(name string) string {
	for _, c := range Commands {
		if c.Name != name {
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s\n\n用法：\n  %s\n\n", c.Summary, c.Usage)
		if len(c.Details) > 0 {
			b.WriteString("说明：\n")
			for _, d := range c.Details {
				fmt.Fprintf(&b, "  %s\n", d)
			}
			b.WriteString("\n")
		}
		if len(c.Examples) > 0 {
			b.WriteString("示例：\n")
			for _, e := range c.Examples {
				fmt.Fprintf(&b, "  %s\n", e)
			}
			b.WriteString("\n")
		}
		return b.String()
	}
	return fmt.Sprintf("没有名为 %q 的命令。用 v2h help 查看全部命令。\n", name)
}

// TUIText renders the key map for display.
func TUIText() string {
	var b strings.Builder
	b.WriteString("按键说明：\n")
	for _, k := range TUIKeys {
		fmt.Fprintf(&b, "  %-14s %s\n", k.Key, k.Action)
	}
	return b.String()
}
