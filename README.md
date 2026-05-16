<p align="center">
  <img src="assets/logo.png" alt="LinkCode Logo" width="150">
</p>

<h1 align="center">LinkCode</h1>

<p align="center">
  <em>IM 里说句话，专属 Bot 主动来找你，背后绑定你的 AI Agent 进程。</em>
  <br>
  <em>A word in IM. A dedicated bot reaches out. An AI agent process runs behind it.</em>
</p>

---

## What is LinkCode / 这是什么

LinkCode runs on your local machine. You talk to a WeCom (企业微信) bot, and behind the scenes a **Claude Code** process is spawned, bound to that bot. The chat window = the agent session. Chat history = agent logs.

LinkCode 部署在你本机。你在企业微信跟 Bot 聊天，背后就是一个 Claude Code 进程在帮你干活。聊天窗即会话，聊天记录即日志。

```
User ←→ WeCom Platform ←→ [WebSocket] ←→ LinkCode (your machine) ←→ Claude Code (subprocess)
                                ↑
                   wss://openws.work.weixin.qq.com
```

No public IP, no port forwarding, no tunneling. Your machine just needs outbound internet access.

不需要公网 IP、不需要端口映射、不需要内网穿透。你的机器只要能访问外网即可。

## Key Features / 核心特性

- **Menu-driven control bot** / **拨号菜单式总控 Bot** — `/start` to create agents, `/end` to stop them, stable and predictable
- **Bot pool with encrypted storage** / **Bot 池化 + 加密存储** — Pre-register WeCom bot credentials via `/addbot`, AES-256-GCM encrypted in MySQL
- **1:1 session isolation** / **1:1 会话隔离** — Each agent session gets its own WeCom bot identity, naturally isolating conversations
- **Streaming output** / **流式输出** — Claude Code output streams chunk-by-chunk through Go channels to WeCom
- **Session continuity** / **会话连续性** — Sessions persist in MySQL; process can sleep and resume via `--resume`
- **Local admin panel** / **本地管理面板** — `http://127.0.0.1:18980` shows sessions, bots, and process status

## Prerequisites / 依赖

| What | Why |
|------|-----|
| Go 1.21+ | Runtime |
| MySQL 8.0+ | Data storage |
| Claude Code (CLI) | Agent engine (`claude` binary) |
| WeCom AI Bots | IM channel (created manually in WeCom admin panel) |

## Quick Start / 快速开始

```bash
# 1. Clone & build
cd linkcode
go build ./cmd/linkcode/

# 2. Create MySQL database
mysql -u root -e "CREATE DATABASE linkcode CHARACTER SET utf8mb4"

# 3. Edit configs/linkcode.yaml with your DB DSN and control bot credentials

# 4. Run
./linkcode -config configs/linkcode.yaml
```

## Setup Flow / 首次使用流程

```
1. Create several AI Bots in WeCom admin panel, note each botId + secret
   在企微管理后台创建若干 AI Bot，记录 botId 和 secret

2. Open WeCom, send /start to your control bot
   在企微给总控 Bot 发 /start

3. Select "Add Bot" → /addbot <name> <botId> <secret>
   选择"添加新 Bot"，录入凭证

4. Select "Create Agent" → name it → done!
   选择"创建新 Agent" → 命名 → 完成

5. The new worker bot will proactively message you
   新的 Worker Bot 会主动找你说话
```

## Daily Use / 日常使用

```
You → /start to control bot
Bot → Menu: 1.Create  2.List  3.Add Bot  4.Bot Pool  5.End

You → 1
Bot → Agent type? (1.Claude Code)
You → 1
Bot → Name your agent:
You → 代码助手
Bot → ✅ "代码助手" is ready. Bot "备用1" will message you shortly.

Worker Bot → "你好，我是你的 Claude Code「代码助手」"
You → (talk to the worker bot freely)
Worker Bot → (Claude Code responses stream back)

You → /end  → select session → done.
```

## Commands / 指令

| Command | Description |
|---------|-------------|
| `/start` | Open main menu / 打开主菜单 |
| `/end` | End an agent session / 结束 Agent 会话 |
| `/list` | List active sessions / 列出活跃会话 |
| `/addbot <name> <id> <secret>` | Register a bot into the pool / 录入 Bot 到池中 |

## Project Structure / 项目结构

```
linkcode/
├── cmd/
│   ├── linkcode/main.go     # Main binary / 主程序
│   └── devmsg/main.go       # Dev contact tool / 开发者联络工具
├── internal/
│   ├── gateway/             # WebSocket connection manager
│   ├── controller/          # Control bot menu logic
│   ├── botpool/             # Bot pool (allocate/recycle)
│   ├── session/             # Session CRUD + binding
│   ├── router/              # Bidirectional message routing
│   ├── procman/             # Agent process manager (Claude Code subprocess)
│   ├── admin/               # Web admin panel
│   ├── store/               # MySQL data layer
│   ├── channel/wecom/       # WeCom WebSocket implementation
│   ├── agent/claude/        # Claude Code agent implementation
│   └── crypto/              # AES encryption utilities
├── assets/                  # Images, logos
├── migrations/              # SQL DDL
└── configs/                 # Configuration templates
```

## MVP Scope / MVP 范围

| In scope ✅ | Out of scope ❌ |
|-------------|----------------|
| WeCom IM platform | Telegram / Discord / Slack |
| Claude Code agent | Other agent types |
| Menu-based control | Natural language parsing |
| Text messages | Images / files |
| Single-user deployment | Multi-tenant |
| Manual bot registration | Dynamic bot creation API |

## License / 协议

MIT
