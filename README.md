[![Xray Core](https://img.shields.io/badge/Xray-Core-00BFFF?style=for-the-badge)](https://github.com/XTLS/Xray-core)
[![Docker](https://img.shields.io/badge/Docker-2496ED?style=for-the-badge&logo=docker&logoColor=white)](https://www.docker.com/)
[![SOCKS5](https://img.shields.io/badge/Proxy-SOCKS5-ff6b35?style=for-the-badge)](https://en.wikipedia.org/wiki/SOCKS)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?style=for-the-badge&logo=go&logoColor=white)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-2ea44f?style=for-the-badge)](./LICENSE)

# v2h —— 把订阅变成带账号的 HTTP / SOCKS5 代理

把 Clash / v2ray 订阅（机场订阅、自建节点列表）变成一台可以分账号使用的 HTTP / SOCKS5 代理服务器：
一个二进制里内置 Xray 内核，配上网页面板、终端控制台和 CLI，一个容器就能跑。

## 特性

- **多账号**：每个账号一对「用户名 + 密码」，客户端用标准 HTTP 代理或 SOCKS5 认证接入，互不影响，流量分别统计。
- **订阅驱动**：直接填 Clash(mihomo) YAML 或 base64 节点链接的订阅地址，定时自动更新；也可以粘贴本地节点文件离线导入。
- **按用户分流**：一个账号可以绑定「某个订阅的某个节点」「一条订阅的全部节点」「多条订阅混合」，顺序即优先级。
- **优先级 / 自动切换**：三种选路模式（priority / auto / fixed），节点挂了自动换下一个。
- **兜底策略**：所有目标节点都不可用时，按配置选择「拒绝连接」或「直连」，不会静默把流量漏出去。
- **健康检查**：后台定时通过节点真实发起请求测活性和延迟，驱动 priority 模式的切换。
- **三种入口**：网页控制面板、终端控制台（TUI）、命令行（CLI），三者操作的是同一个实例。
- **日志轮转**：按大小切分、按份数保留，面板/TUI 实时跟随，支持按账号和级别过滤。
- **Cloudflare Turnstile**：给公网暴露的登录页加人机验证，可选。
- **单二进制**：内嵌内核与面板前端，运行镜像只需要 CA 证书，没有 Xray 子进程、没有内核配置文件。

---

## 快速开始

```bash
# 1) 准备数据目录（容器内以 uid 1000 运行，宿主目录必须归它所有）
mkdir -p data && sudo chown -R 1000:1000 data

# 2) 拉起容器（首次会自动生成 data/config.yaml）
docker compose up -d

# 3) 看启动日志：管理员密码只在这里打印一次，请立刻记下
docker logs -f v2h
```

日志长这样：

```
v2h v1.0.0 (abc1234)  ·  Xray 1.260327.0

  数据目录     /data
  配置文件     /data/config.yaml
  HTTP 代理    0.0.0.0:8080
  SOCKS5 代理  0.0.0.0:1080（含 UDP）
  控制面板     http://127.0.0.1:9080
  管理员账号   admin
  管理员密码   xxxxxxxxxxxxxxxx
               ↑ 只显示这一次，请立即保存
```

### 1. 首次登录

浏览器打开 `http://<服务器IP>:9080`，用户名 `admin`，密码用日志里那串。
嫌麻烦的话可以在 `docker-compose.yml` 里先用 `V2H_ADMIN_PASSWORD` 指定一个（**只在 config.yaml 还不存在时生效**），登录后再到「设置」里改掉。

### 2. 建订阅

面板「订阅」页 → 新建，填名称和订阅链接即可。对应的 CLI 写法（在容器里执行）：

```bash
docker exec -it v2h v2h sub add "机场A" --url "https://example.com/sub?token=xxx" --interval 12h
docker exec -it v2h v2h sub list
docker exec -it v2h v2h sub update 机场A        # 手动立刻更新一次
```

手上有节点文件（Clash YAML / base64 链接）不想联网拉取，就直接导入：

```bash
docker exec -i v2h v2h sub import "本地节点" < nodes.yaml
# 或者：docker exec -i v2h v2h sub import "本地节点" --file /tmp/nodes.yaml
```

### 3. 建账号

面板「账号」页 → 新建，填用户名/密码，然后勾选要用的节点（也可以直接选「整条订阅」）。
CLI 等价写法：

```bash
# 两个节点按顺序做优先级
docker exec -it v2h v2h user add alice --password 'secret123' \
  --target "机场A:香港01" --target "机场A:日本02"

# 整条订阅 + 自动选延迟最低
docker exec -it v2h v2h user add bob --password 'secret456' --target "机场A:*" --mode auto

docker exec -it v2h v2h user list
docker exec -it v2h v2h status
```

### 4. 客户端连上来

```bash
# HTTP 代理（宿主 9000 → 容器 8080）
curl -x http://alice:secret123@127.0.0.1:9000 https://api.ipify.org

# SOCKS5（宿主 1080 → 容器 1080）
curl --socks5-hostname alice:secret123@127.0.0.1:1080 https://api.ipify.org
```

能返回节点出口的 IP 就说明整条链路通了。

---

## 核心概念

| 概念 | 说明 |
| --- | --- |
| **订阅** | 一个远程地址（Clash YAML 或 base64 节点链接）或一次本地导入，里面有若干节点。可以设置自动更新间隔。 |
| **节点** | 订阅里的一个服务器。v2h 会把它转成 Xray 的 outbound；内核不支持的协议会被跳过并在面板上标注原因（见下文）。 |
| **账号** | 一对用户名/密码，客户端用它连 HTTP/SOCKS5。账号之间完全隔离，各有独立的出口和流量统计。 |
| **目标（target）** | 账号可以用哪些节点，写成「订阅:节点」；`订阅:*` 表示整条订阅，还可以加 limit 限制取前 N 个。目标的顺序就是优先级顺序。 |
| **模式（mode）** | 账号怎么在这些目标里挑节点，三种：priority / auto / fixed。 |
| **兜底策略（fallback）** | 所有目标都不可用时怎么办：`reject`（拒绝连接，默认）或 `direct`（直连）。账号可以单独设置，也可以跟随全局 `proxy.fallback`。 |
| **健康检查** | 后台按 `health.interval` 通过每个节点请求一次 `health.probe_url`，连续失败 `health.failures` 次标记为不可用，连续成功 `health.successes` 次恢复。priority 模式的切换由它驱动。 |

### 三种选路模式

| 模式 | 怎么选节点 | 切换代价 | 适合什么场景 |
| --- | --- | --- | --- |
| `priority`（默认） | 按你给的 target 顺序，选第一个健康的节点 | **换节点时会重启内核（约 0.2 秒，视机器而定），已建立的连接会断开** | 想自己控制用哪个节点；节点少、稳定、有明确优先级 |
| `auto` | 把该账号的全部目标节点交给内核的 balancer，内核自动测速并选延迟最低的健康节点 | **内核平滑切换，不重启、不断连接** | 节点多、想让内核自动挑最快的；对连接稳定性要求高 |
| `fixed` | 固定用目标里的第一个节点，不做健康切换 | 不切换 | 只想固定走某个节点，不想被自动改掉 |

> 一句话：`priority` 的切换是「重启内核换出口」，会让在跑的连接断一下；`auto` 的切换在内核内部完成，连接不受影响。

在面板「账号」页或 `v2h user edit <账号> --mode auto` 里可以随时改。

---

## 客户端怎么连

v2h 对外提供两个标准代理入口，都用**账号名 + 密码**认证（就是面板里建的那个账号）：

| 入口 | 容器内端口 | 默认宿主端口 | 地址示例 |
| --- | --- | --- | --- |
| HTTP 代理（支持 CONNECT，HTTPS 走这里） | 8080 | **9000** | `http://alice:secret123@127.0.0.1:9000` |
| SOCKS5 代理（含 UDP ASSOCIATE） | 1080 | **1080** | `socks5://alice:secret123@127.0.0.1:1080` |

> 宿主 9000 是历史遗留下来的端口号，对应容器里的 8080；两个数字都可以在 `docker-compose.yml` 和 `data/config.yaml` 里改。

```bash
# curl：HTTP 代理访问 HTTPS 站点
curl -x http://alice:secret123@127.0.0.1:9000 https://api.ipify.org

# curl：SOCKS5（用 --socks5-hostname 让域名在代理侧解析）
curl --socks5-hostname alice:secret123@127.0.0.1:1080 https://api.ipify.org

# git 走 SOCKS5
git -c http.proxy=socks5h://alice:secret123@127.0.0.1:1080 clone https://github.com/XTLS/Xray-core

# 浏览器 / 系统代理：填 HTTP 代理 127.0.0.1:9000，或 SOCKS5 127.0.0.1:1080，
# 然后在弹出的框里输入账号密码。
```

同一台机器上给不同程序用不同账号时，就把每个程序各自指向自己的账号即可。

---

## 支持的节点协议

**可以用**（会被转成 Xray outbound）：

- 协议：`vless`、`vmess`、`trojan`、`ss`、`socks`、`http`
- 传输层：`tcp`、`ws`、`grpc`、`h2`（Clash 里写作 `http`）、`httpupgrade`、`xhttp`
- 安全层：TLS（含 SNI / ALPN / fingerprint）、REALITY（public key / short id / spiderX）、vmess 的 alterId 与各种加密
- Shadowsocks 加密方式：`aes-128-gcm`、`aes-256-gcm`、`chacha20-ietf-poly1305`、`xchacha20-ietf-poly1305`、`none`、`2022-blake3-aes-128-gcm`、`2022-blake3-aes-256-gcm`、`2022-blake3-chacha20-poly1305`

**会被跳过**（订阅依然能更新，只是这些节点不参与选路；面板「节点」页会写明原因）：

| 节点 | 跳过原因 |
| --- | --- |
| `hysteria` / `hysteria2` | Xray 内核不支持该协议 |
| `tuic` | 同上 |
| `anytls`、`ssh`、`wireguard`、`snell`、`mieru` | 同上 |
| 带 `plugin` 的 `ss`（obfs、v2ray-plugin） | Xray 没有对应的插件实现 |
| 用了上表以外加密方式的 `ss`（如旧的 `aes-128-cfb` 流加密） | 加密方式不受支持 |
| 未知协议的节点 | 识别不出来 |
| Clash 里的 `direct` 策略组 | 那是直连策略，不是真实节点 |

健康检查对不支持的节点不会去测，`v2h node list --usable` 可以把它们过滤掉。

订阅里要求跳过证书校验的节点（`allowInsecure=1`、`insecure=1`、`skip-cert-verify: true`）不属于被跳过的一类：Xray 内核
已经删掉了 `allowInsecure`，改为 `pinnedPeerCertSha256`。这类节点会在后台被探测一次证书，能正常校验的什么都不做（免得
证书续期后就失联），确实校验不了的才把叶子证书的指纹固定下来写进内核配置。证书换新后最多一小时会自动重新探测，期间
该节点会失效并由健康检查切走。

---

## 面板 / TUI / CLI

三个入口操作的是同一个实例、同一份配置；改完立即生效（按需重建内核）。

### 网页面板

`http://<服务器IP>:9080`，用管理员账号登录。里面能管理账号、订阅、节点（含一键测速）、日志，以及设置页里的代理端口、日志、健康检查、Cloudflare Turnstile 等。登录页支持可选的 Turnstile 人机验证。

### 终端控制台（TUI）

```bash
docker exec -it v2h v2h tui
```

也可以在本机装了 v2h 的机器上连远程实例：

```bash
v2h --api http://<服务器IP>:9080 --token <api_token> tui
```

按键：`1`-`6` / `Tab` 切换页面，`↑↓`（或 `j k`）移动，`Enter` 看详情，`t` 对节点或整条订阅测速，`r` 刷新订阅或重启内核，`f` 日志实时跟随，`a` 切换日志等级，`/` 关键字过滤，`?` 帮助，`q` 退出（**只退出界面，不会停掉服务**）。

### 命令行

```bash
v2h help                 # 全部命令
v2h help user            # 单个命令的详细说明和示例
v2h status               # 内核状态 + 账号流量 + 订阅概览
v2h version              # 版本、构建时间、内核版本、Go 版本
```

| 命令 | 用途 |
| --- | --- |
| `v2h serve [--data DIR] [--listen ADDR] [--log-level LEVEL] [--tui]` | 启动代理内核 + 面板 + 后台任务（容器里的默认命令） |
| `v2h tui` | 打开终端控制台 |
| `v2h status [--json]` | 状态、流量、订阅概览 |
| `v2h user <list\|show\|add\|edit\|rm\|enable\|disable\|passwd\|targets>` | 账号与目标管理 |
| `v2h sub <list\|add\|edit\|rm\|update\|nodes\|import>` | 订阅管理 |
| `v2h node <list\|test>` | 节点列表与测速 |
| `v2h logs [-f] [--level LEVEL] [--user NAME] [--lines N] [--query TEXT]` | 看日志、实时跟随 |
| `v2h config <show\|path\|get\|set>` | 查看/修改配置 |
| `v2h admin <passwd\|token>` | 改管理员密码、轮换 API Token |
| `v2h core <restart\|config>` | 重启内核、打印当前生成的内核配置 |
| `v2h help [命令]` / `v2h version` | 帮助 / 版本 |

全局参数（任何命令都能用）：`--data DIR`、`--api URL`、`--token TOKEN`、`--json`。
除了 `serve`、`help`、`version`，其他命令都是**连到正在运行的实例**上执行——容器里直接 `docker exec -it v2h v2h <命令>` 就行（CLI 会读 `/data/config.yaml` 里的面板地址和 API Token，默认只允许回环地址使用 Token，所以容器内可用）。

例子：

```bash
v2h config show                          # 打印当前设置
v2h config set logs.max_size_mb 100      # 改日志单文件上限
v2h config set proxy.fallback direct     # 改全局兜底策略
v2h user passwd alice --generate         # 给账号生成新密码
v2h node test 香港01 --json              # 测单节点
v2h node test --sub 机场A                # 测整条订阅
v2h logs -f --user alice --level error   # 只看 alice 的错误日志
v2h core config                          # 看内核实际拿到的配置（排障用）
```

---

## 日志与日志大小

日志文件在数据目录的 `logs/` 下（默认 `/data/logs/v2h.log`），按大小轮转，保留若干份历史文件（`v2h.log.1`、`v2h.log.2` …）。

| 配置项 | 默认 | 说明 |
| --- | --- | --- |
| `logs.level` | `info` | `debug` / `info` / `warning` / `error` / `none` |
| `logs.access_log` | `true` | 是否记录每条连接 |
| `logs.dir` | `logs` | 相对路径相对于数据目录 |
| `logs.max_size_mb` | `50` | 单个文件超过这个大小就轮转 |
| `logs.max_backups` | `3` | 保留多少份历史文件 |
| `logs.ring_size` | `2000` | 内存里留给面板/TUI 实时查看的条数 |
| `logs.console` | `true` | 是否同时写容器标准输出（`docker logs` 看得到） |

三种改法都一样：面板「设置」页、`v2h config set logs.max_size_mb 100`、或直接编辑 `config.yaml`。
磁盘紧张就调小 `max_size_mb` / `max_backups`，或者在容器里手动清理 `data/logs/`。

---

## 公网部署与安全

默认配置是把面板和代理都监听在 `0.0.0.0` 上的，直接暴露到公网之前请至少做完下面几件事：

1. **给面板加一道门**：用 Cloudflare Tunnel 或反向代理（Nginx / Caddy）对外，最好再加 Cloudflare Access 之类的访问控制。
2. **打开 Turnstile**：面板「设置 → Cloudflare Turnstile」，填 Site Key 和 Secret Key 并启用。`fail_open` 保持关闭（默认），这样 Cloudflare 不可用时会拒绝登录而不是放行。
   > Turnstile 需要容器能访问 `challenges.cloudflare.com`，镜像里已经带了 CA 证书。
3. **收紧 `panel.token_ips`**：它决定哪些来源可以用 API Token 免登录调用接口（默认只有 `127.0.0.1/8` 和 `::1/128`）。**不要**改成 `0.0.0.0/0`，那等于把控制权完全开放。想从本机用 CLI 连远程实例，就把你那台机器的出口 IP（段）加进去。
4. **设置 `panel.trusted_proxies`**：只有来自这些网段的请求，`X-Forwarded-For` / `X-Real-IP` 才会被采信。用反代时填代理所在的网段，否则日志里的客户端 IP 会不对。
5. **保护好 `config.yaml`**：里面存着所有代理账号的**明文密码**（Xray 认证需要原文）和 API Token，落盘权限是 `0600`。别提交到 git，别共享数据目录。
6. **主机的防火墙**：只放行你真正需要的端口。面板 9080 建议只对反代/隧道开放，HTTP 代理 9000 和 SOCKS5 1080 才是给客户端用的。

> 顺带一提：镜像里的健康检查用的是 `v2h status`，它走 `config.yaml` 里的 API Token。所以如果 `panel.token_ips` 里没有回环地址，容器会被 Docker 标记为 `unhealthy`——看到这个状态先检查这一项。

---

## 配置项参考

配置在数据目录下的 `config.yaml`（容器里是 `/data/config.yaml`），首次启动自动生成，权限 `0600`。
**面板和 CLI 保存配置时会整体重写这个文件，注释不会保留。** 想逐项看带注释的完整说明，见仓库里的 [`config.example.yaml`](./config.example.yaml)。

### `panel` —— 面板与 API

| 字段 | 默认 | 说明 |
| --- | --- | --- |
| `panel.listen` | `0.0.0.0:9080` | 面板监听地址，只给本机用就写 `127.0.0.1:9080` |
| `panel.public_url` | 空 | 面板对外地址（Cloudflare Tunnel / 反代域名），留空表示不指定 |
| `panel.admin.username` | `admin` | 管理员用户名（面板里改不了，需要手改文件） |
| `panel.admin.password_hash` | 首启生成 | bcrypt 哈希，不是明文。用 `v2h admin passwd` / 面板设置改 |
| `panel.session_hours` | `12` | 登录会话有效期（小时） |
| `panel.token_ips` | `127.0.0.1/8`、`::1/128` | 哪些来源可以用 `Authorization: Bearer <api_token>` 调 API |
| `panel.trusted_proxies` | `127.0.0.1/8`、`::1/128` | 信任的反向代理网段（用于取真实客户端 IP） |
| `panel.api_token` | 首启生成 | CLI/TUI 用的 API Token，`v2h admin token` 可轮换 |
| `panel.theme` | `auto` | 面板主题：`auto` / `light` / `dark` |
| `panel.admin.turnstile.enabled` | `false` | 是否在登录页启用 Cloudflare Turnstile |
| `panel.admin.turnstile.site_key` / `secret_key` | 空 | Cloudflare 后台拿到的两个 Key |
| `panel.admin.turnstile.fail_open` | `false` | Cloudflare 不可用时是否放行登录（默认拒绝） |

### `proxy` —— 代理入口

| 字段 | 默认 | 说明 |
| --- | --- | --- |
| `proxy.http.enabled` / `listen` | `true` / `0.0.0.0:8080` | HTTP 代理入口（宿主映射到 9000） |
| `proxy.socks.enabled` / `listen` | `true` / `0.0.0.0:1080` | SOCKS5 入口 |
| `proxy.socks.udp` | `true` | SOCKS5 是否允许 UDP |
| `proxy.sniffing` | `true` | 嗅探连接的域名（http/tls），供自定义路由规则使用 |
| `proxy.fallback` | `reject` | 全局兜底：`reject` 拒绝连接，`direct` 直连 |
| `proxy.dns_servers` | 空 | 内核使用的 DNS 服务器，留空用系统 DNS |
| `proxy.custom_rules` | 空 | 原样插入 Xray 路由规则（排在按账号分流之前），只能手改文件 |
| `proxy.timeout` | `300s` | 连接超时，Go duration 写法 |

### `logs` / `health`

`logs` 见上一节；`health` 的字段含义：

| 字段 | 默认 | 说明 |
| --- | --- | --- |
| `health.enabled` | `true` | 是否做健康检查 |
| `health.probe_url` | `https://www.gstatic.com/generate_204` | 探测地址，应返回 204/200 |
| `health.interval` | `30s` | 探测间隔（auto 模式的测速间隔也取它，最长按 60s 算） |
| `health.timeout` | `5s` | 单次探测超时 |
| `health.failures` | `2` | 连续失败几次算不可用 |
| `health.successes` | `1` | 连续成功几次算恢复 |
| `health.switch_cooldown` | `15s` | 两次健康检查触发的切换之间至少间隔多久（防止抖动导致内核反复重启） |
| `health.max_probes` | `64` | 最多给多少个节点建探测出口 |

### `subscriptions` / `users` / `revision`

- `subscriptions[]`：`name`（唯一，账号里用它引用）、`url`、`kind`（`auto` / `clash` / `v2ray`）、`interval`（默认 `12h`）、`enabled`、`user_agent`；`id` / `last_update` / `last_status` / `last_error` / `node_count` 由程序维护。
- `users[]`：`name`（不能含空格、制表符、换行、冒号）、`password`（明文）、`enabled`、`mode`（`priority` / `auto` / `fixed`）、`fallback`（`inherit` / `reject` / `direct`）、`note`、`targets[]`（`sub` 写订阅名或订阅 id、`node`、`all`、`limit`）、`created_at`；`traffic` 由程序维护。
- `revision`：每次成功保存 +1，用来判断配置是否已经应用到内核；手改文件时一般不用动它。

---

## 从旧版本迁移

旧版（`vless.conf` + `entrypoint.sh` + 容器里现装的 Xray）只支持**一条 VLESS 链接**，这套东西已经彻底移除：现在没有 `vless.conf`，没有 `config.json`，也没有 Xray 可执行文件。

迁移只要一步：**把原来那条链接当成一条订阅导入**。

- 面板：「订阅」页 → 导入/新建，把链接粘进去；
- CLI：把链接存成文件后 `docker exec -i v2h v2h sub import "旧链接" < link.txt`，
  或 `docker exec -i v2h v2h sub import "旧链接" --file /tmp/link.txt`；
- 导入后到「账号」页建一个账号，目标选这条订阅（`旧链接:*`），客户端用这个账号连代理。

端口基本没变：HTTP 代理还是宿主的 **9000**（容器里的 8080），SOCKS5 还是 **1080**，新增了面板端口 **9080**。旧配置里的 `flow`、`pbk`、`sid`、`sni`、`fp`、`type=xhttp`、`path`、`host` 这些参数都能从链接里解析出来，不用手工填。

---

## 常见问题

**改完配置/升级镜像后容器一直重启？**
`docker logs v2h` 看最后几行。常见原因是数据目录权限不对（见下一条），或者订阅拉取失败——后者不会导致重启，只会在面板上标红。

**容器报 `permission denied` 写不了 /data？**
容器内以 uid 1000 运行。宿主目录要先归它：`sudo chown -R 1000:1000 data`。用命名卷（named volume）时 Docker 会自动处理权限，不用管。

**端口连不上？**
三个地方要一致：`data/config.yaml` 里的监听地址（容器内）、`docker-compose.yml` 的端口映射（宿主→容器）、宿主防火墙。特别注意 `config.yaml` 改了端口后，映射也要跟着改。测一下：`docker exec v2h v2h status` 能看到内核状态就说明服务本身是活的。

**节点全挂了会怎样？**
按兜底策略走：`reject` 直接拒绝连接（默认），`direct` 退回本机直连。日志和面板都会写明「没有可用的目标节点，走兜底策略」。常见原因是订阅过期、机场改了地址，或者本地到节点的网络不通；先 `v2h sub update` 再 `v2h node test --sub <订阅>` 看看。

**面板打不开 / 忘记管理员密码？**
面板默认在 `9080`。密码忘了：
```bash
docker exec -it v2h v2h admin passwd        # 交互式改密码
```
容器里没有终端可用时，先停容器，用 `V2H_ADMIN_PASSWORD` 环境变量重启（**只在 config.yaml 不存在时生效**），或者手工编辑 `config.yaml` 里的 `admin.password_hash` 后重启。注意面板密码是 bcrypt 哈希，手写明文不生效。

**切节点的时候连接会断一下？**
这是 `priority` 模式的设计：切换出口需要重建内核配置，约 0.2 秒，已建立的连接会断开。对连接稳定性要求高就把账号改成 `auto` 模式——内核在内部平滑切换，不会断：
```bash
docker exec -it v2h v2h user edit alice --mode auto
```

**订阅里的某些节点在面板上是灰的 / 带原因标签？**
那是 Xray 内核不支持的协议（hysteria2、tuic、带 plugin 的 ss 等），已按上文的规则跳过，不影响其他节点使用。

**`v2h` 命令报「未知命令」或提示连不上服务？**
带子命令的命令（`status`、`user`、`sub` …）必须先有实例在跑。容器里用 `docker exec -it v2h v2h …`；从容器外连，需要 `--api` 和 `--token`，并把来源 IP 加进 `panel.token_ips`。

**容器显示 `unhealthy`？**
健康检查走的是 `v2h status` → `/api/health` + 带鉴权的 `/api/status`。如果改过 `panel.token_ips`、把回环地址去掉了，检查会失败。改回来即可。

---

## 开发与构建

```bash
# 本地编译（产物完全静态，CGO_ENABLED=0）
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o v2h ./cmd/v2h

# 跑起来：数据落在 ./data，面板 http://127.0.0.1:9080
./v2h serve

# 跑测试
go vet ./... && go test ./... -race

# 本地构建镜像
docker build -t v2h:dev .
docker run -d --name v2h \
  -p 9000:8080 -p 1080:1080 -p 9080:9080 \
  -e TZ=Asia/Shanghai \
  -v "$PWD/data:/data" \
  v2h:dev
```

版本信息（`v2h version` / 面板页脚）来自链接期注入：

```bash
go build -trimpath -ldflags "-s -w \
  -X github.com/ldm0206/vless-to-http/internal/version.Version=v1.0.0 \
  -X github.com/ldm0206/vless-to-http/internal/version.Commit=$(git rev-parse --short HEAD) \
  -X github.com/ldm0206/vless-to-http/internal/version.Date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -o v2h ./cmd/v2h
```

**CI / 发版**（都在 `.github/workflows/`）：

- `ci.yml`：PR 和非 main 分支的 push 跑 `go vet ./...`、`go test ./... -race`、`go build ./...`，Go 版本读 `go.mod`，带模块缓存。
- `release.yml`：**每次 push 到 main 就发一版**，不需要打 tag——
  - 先跑一遍 vet / test -race / build，测试不过就不发；
  - 构建并推送多架构镜像到 **`ghcr.io/ldm0206/vless-to-http`**，架构 `linux/amd64`、`linux/arm64`、`linux/arm/v7`。镜像里的二进制是在 runner 上用 Go 原生交叉编译好的，镜像构建阶段只做打包，不碰 QEMU 模拟编译（在 arm/v7 上那会慢到几十分钟）。标签：
    - `latest` / `main`：始终指向 main 上最新一次提交，`docker compose pull` 拉到的就是它；
    - `sha-<短提交号>`：某次提交的固定镜像，想回滚就 `docker compose` 里把 image 改成 `:sha-1a2b3c4`；
  - 交叉编译 linux（amd64/arm64/armv7）、darwin（amd64/arm64）、windows（amd64/arm64）的裸二进制，连同 `SHA256SUMS` 一起作为这次运行的下拉产物提供（保留 90 天）。

> 版本号就是提交的短哈希（例如 `v2h 1a2b3c4`），面板底部和 `v2h version` 里都能看到，用它去对应 `sha-` 镜像标签。
>
> 需要重发某次提交时，用 Actions → Publish → Run workflow 手动触发。

需要一个 VLESS + Reality 服务端做对照测试的话，可以用 [simple-xray-core](https://github.com/thejohnd0e/simple-xray-core)。

---

## License

[MIT](./LICENSE)
