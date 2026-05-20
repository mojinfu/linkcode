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

## 🎬 Control: One Command, One Bot

<div align="center">
  <img src="assets/demo-control.svg" alt="LinkCode control flow">
</div>

<br>

## 🎬 Execute: Your Bot Takes Action

<div align="center">
  <img src="assets/demo-task.svg" alt="LinkCode task execution">
</div>

---

## Why LinkCode?

| Pain Point | LinkCode |
|---|---|
| **Chained to your desk** — CI fails during dinner? Can't fix it from your phone. | **IM is your remote control.** Trigger builds, check logs, deploy from anywhere. No SSH, no VPN. |
| **General-purpose bots lose context** — one bot juggling everything, conversations overlap. | **One bot, one task, one session.** Dedicated bots with full context per task. Switch tasks by switching chats. |
| **Cloud relays are slow** — IM-to-AI bridges bounce through cloud APIs, adding seconds of latency. | **Runs locally, sub-second response.** Agent processes live on your machine. No cloud hop. |
| **Chatbots only *talk*. They don't *do*.** — ChatGPT tells you *how*. It won't open the file or run the command. | **Your agent operates your computer.** It reads files, runs commands, manages processes — then reports back. |
| **Your code leaves your machine** — cloud AI uploads your context to someone else's infrastructure. | **Everything stays local.** Agent runs as a child process on your own hardware. Nothing leaks. |

---

## LinkCode vs. Clawdbot

Both let you control Claude Code from IM. The difference is how directly they do it.

|  | LinkCode | Clawdbot |
|---|---|---|
| **Prompt purity** | Passes messages through as-is. Claude behaves exactly like terminal use. | Injects system-level prompts that alter Claude's behavior. |
| **Token efficiency** | Zero overhead — every token is your conversation. | System prompts and meta-instructions consume tokens on every message. |
| **Response speed** | Sub-second — direct local process communication via Go channels. | Proxy gateway adds relay latency; browser control operations can hit 20s timeouts. |
| **Security** | No web panel, no plugin marketplace, no npm dependency chain. | Web admin panel, plugin marketplace, npm supply chain. |
| **Controllability** | Identical to native Claude Code — no prompt conflicts, no degraded reasoning. | Prompt layering can cause model behavior drift or degraded reasoning. |
| **Permission model** | Your local filesystem permissions. No extra credentials needed. | Requires browser/web account credentials for certain features. |
| **Multi-bot** | Multi-bot by design — each bot gets an independent WebSocket connection. | Primarily single-platform with extensions for additional channels. |
| **Session isolation** | One bot = one session. Full context isolation. Switch tasks by switching chats. | Sessions managed internally; isolation depends on configuration. |
| **Session persistence** | Sessions persist via `--resume`. Dormant bots wake up with full context. | Session state tied to Node.js process lifecycle. |

**Pick LinkCode** if you want minimal, transparent, zero-overhead control of Claude Code from IM.
**Pick Clawdbot** if you prefer a web admin panel and plugin ecosystem.

---

## Quick Start

```bash
# Interactive installer (auto-detects dependencies)
bash scripts/install.sh

# Edit config
vim configs/linkcode.yaml

# Run
./bin/linkcode -config configs/linkcode.yaml
```

Then open WeCom, send `/start` to your control bot. Done.

---

## Roadmap

**IM Platforms**

| Platform | Status |
|----------|:------:|
| WeCom (企业微信) | Done |
| Telegram | Planned |
| Teams | Planned |

> Adding a platform = implementing `channel.Channel`.

**Agent Tools**

| Agent | Status |
|-------|:------:|
| Claude Code | Done |
| Kimi Code | Planned |

> Adding an agent = implementing `agent.Runner`. Everything else stays.

---

## How It Works

### Control Bot — menu-driven

Send `/start` to the control bot and reply with numbers:

```
Welcome to LinkCode! Choose:
1. New Agent
2. My Agents
3. Add Bot
4. End Agent

Reply with number 1–4
💡 Quote this message before replying for reliable routing
```

**Tip**: long-press the control bot's menu → "Quote" → reply with a number. Accuracy guaranteed.

### Worker Bot — free conversation

Each worker bot is a Claude Code session. Talk naturally. Quote-reply a bot message to pass that context back to Claude:

```
[Quoted message]
Claude's previous output
[New message]
Your follow-up
```

Sessions persist via `--resume`. Reply to a dormant bot and it wakes up with full context.

---

<div align="center">

**[LinkCode](https://github.com/mojinfu/linkcode)** · MIT License

</div>
