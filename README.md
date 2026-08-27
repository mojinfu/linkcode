<div align="center">
  <img src="assets/logo.png" alt="LinkCode" width="120">
</div>

<div align="center">
  <img src="assets/title.svg" alt="LinkCode" width="720">
</div>

<div align="center">

[![Go](https://img.shields.io/badge/Go-1.21+-00ADD8?style=for-the-badge&logo=go&logoColor=white)]()
[![WeCom](https://img.shields.io/badge/WeCom-Work_WX-07C160?style=for-the-badge&logo=wechat&logoColor=white)]()
[![License](https://img.shields.io/badge/License-MIT-yellow?style=for-the-badge)]()

</div>

<br>

<div align="center">
  <em>Say a word in IM. A dedicated bot reaches out. An AI agent runs behind it — on your machine.</em>
</div>

<br>

---

## What LinkCode Is

LinkCode bridges **企业微信 (WeCom)** to **Claude Code**, letting you drive an AI coding agent from your phone's IM app. The agent process runs on your machine with full access to your repo — no code leaves your machine.

```
Your phone (WeCom)  ←→  LinkCode (your machine)  ←→  Claude Code (subprocess)
                              ↕
                          MySQL (local)
```

- **No public IP or port forwarding needed** — LinkCode connects outbound to WeCom via WebSocket
- **One bot per session** — each Claude Code conversation gets its own WeCom bot
- **Streaming replies** — see Claude's output in real time with a live spinner
- **Session persistence** — conversations survive process restarts via Claude's `--resume`

---

## Quick Start

```bash
# Build
go build -o bin/linkcode ./cmd/linkcode/

# Edit config
cp configs/linkcode.yaml.example configs/linkcode.yaml
vim configs/linkcode.yaml

# Run
./bin/linkcode -config configs/linkcode.yaml
```

Then open WeCom, send `/start` to your control bot, and follow the menu to create your first agent.

**Prerequisites**: Go 1.21+, MySQL, Claude Code CLI, a WeCom account with permission to create AI Bots.

---

## Architecture

```
┌──────────┐    WebSocket     ┌──────────────┐    stdin/stdout    ┌─────────────┐
│  WeCom   │ ←──────────────→ │   LinkCode   │ ←───────────────→ │  Claude Code │
│  (IM)    │   wss://...      │              │   stream-json     │  (subprocess)│
└──────────┘                  └──────┬───────┘                   └─────────────┘
                                     │
                              ┌──────┴───────┐
                              │    MySQL      │
                              └──────────────┘
```

### Modules

| Module | Responsibility |
|--------|---------------|
| **gateway** | WebSocket connection management — one connection per bot, heartbeat, auto-reconnect |
| **controller** | Control bot — menu-driven interaction (`/start`, `/list`, `/addbot`) |
| **botpool** | Bot credential store — AES-256-GCM encrypted secrets, idle/bound/unavailable lifecycle |
| **session** | Session CRUD — Claude session ID tracking, waked/sleeped process states |
| **router** | Bidirectional message forwarding — stream replies, command dispatch, message queuing |
| **procman** | Claude Code subprocess management — stdin/stdout pipes, stream-json parsing |
| **pricing** | Per-model cost calculation — configurable pricing, per-session accumulation |
| **admin** | Local web dashboard at `127.0.0.1:18980` |

---

## Configuration

```yaml
# configs/linkcode.yaml
db:
  dsn: "root:@tcp(127.0.0.1:3306)/linkcode?parseTime=true"
  max_open_conns: 10
  max_idle_conns: 5

control_bot:
  bot_id: "your_bot_id"
  secret: "your_secret"

agent:
  default_type: "claude-code"
  claude_code_path: "claude"
  default_work_dir: ""        # empty = current directory
  deepseek_proxy_addr: "127.0.0.1:18981"
  pricing:
    deepseek-v4-flash:
      cache_hit_input: 0.02   # ¥ per 1M tokens
      cache_miss_input: 1.0
      output: 2.0
      symbol: "¥"
    deepseek-v4-pro:
      cache_hit_input: 0.025
      cache_miss_input: 3.0
      output: 6.0
      symbol: "¥"

admin:
  enabled: true
  bind_addr: "127.0.0.1:18980"

encrypt_key: ""               # AES-256 key for bot secrets (auto-generated if empty)
```

### Environment Variable Overrides

| Variable | Overrides |
|----------|-----------|
| `LINKCODE_DEEPSEEK_PROXY_ADDR` | DeepSeek proxy listen address |
| `ANTHROPIC_BASE_URL` | If contains "deepseek", starts a local compatibility proxy |

### DeepSeek Proxy

When `ANTHROPIC_BASE_URL` points to a DeepSeek endpoint, LinkCode automatically starts a local HTTP proxy that rewrites Anthropic-format requests (converts `role: system` to `role: user`) for DeepSeek API compatibility.

---

## Commands

### Control Bot

Send these to your control bot (the one configured as `control_bot`):

| Command | Description |
|---------|-------------|
| `/start` | Show the main menu (create agent, list agents, set work dir) |
| `/list` | List all agents with status, cost, queue depth, and work directory |
| `/addbot <name> <BotID> <Secret>` | Quick-create an agent in one command |
| `/restart` | Run `make.ps1 restart` on the host, then exit LinkCode |
| `/help` | Show available commands |

### Worker Bot

Send these to your worker bot (the one bound to a Claude Code session):

| Command | Description |
|---------|-------------|
| `/stop` | Interrupt the running Claude process (like Ctrl+C) |
| `/new` | End current conversation, start a fresh session (preserves old session for resume) |
| `/workdir` | Ask Claude for its current working directory |
| `/resetdefaultworkdir <path>` | Change the bot's default work directory (takes effect after `/new`) |
| `/help` | Show available commands |

---

## Features

### Streaming Replies

Claude's output streams to WeCom in real time:
- **Braille spinner** (`⣷⣯⣟⡿⢿⣻⣽⣾`) with adaptive speed — fast when tokens are flowing, slows when idle
- **Elapsed time** shown in the status bar
- **Queue indicator** — when messages pile up during a long thinking phase, shows `[waiting ₙ]`
- **Timeout warning** — at 9.5 minutes, warns that the stream window is closing
- **Stream fallback** — if the WebSocket drops mid-response, the full response is resent as a regular message

### Message Queuing

If you send a message while Claude is still thinking:
- The message goes into a queue (not lost)
- You get a notification: "Agent 正在思考（已过 X 秒），排队消息：N 条"
- Queued messages are processed one-by-one after the current turn finishes
- Use `/stop` to interrupt and skip the queue

### Cost Tracking

- Per-session token counting (input, output, cache read)
- Cost calculated from configurable model pricing
- Cumulative cost shown after each response
- Persisted to MySQL across restarts

### Session Persistence

- Claude sessions survive process restarts via `--resume`
- `/new` keeps the old session for future reference
- Worker bots reconnect automatically on startup

### Work Directory

Each bot can have its own working directory:
1. Bot-level `work_dir` (set via `/resetdefaultworkdir`)
2. Global `default_work_dir` (from config or DB settings)
3. Current process directory (fallback)

---

## Database

MySQL with InnoDB, utf8mb4. Four tables:

| Table | Purpose |
|-------|---------|
| `bots` | Bot credentials (secret AES-256-GCM encrypted), status, work_dir |
| `sessions` | Claude session ID, process status, bound bot, cost |
| `messages` | Chat history (role, content, content_type) |
| `settings` | Key-value store (e.g. `default_work_dir`) |

Migrations run automatically on startup from `internal/store/migrations/`.

---

## Development

```bash
# Build
go build -o bin/linkcode ./cmd/linkcode/

# Run tests
go test ./...

# Dev messaging tool (send test messages to WeCom users)
go build -o devmsg ./cmd/devmsg/
./devmsg -user XuDeYi "test message"
```

### Makefile

A `make.ps1` PowerShell script provides `build`, `run`, `stop`, `restart`, `status`, `clean` targets. Requires `pwsh` (PowerShell Core).

---

## License

MIT

---

<div align="center">

**[LinkCode](https://github.com/mojinfu/linkcode)**

</div>
