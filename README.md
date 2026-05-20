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

## What LinkCode Is

LinkCode 不是 Claude 的替代品或竞品，而是 **Claude Code 的 IM 消息代理与安全网关**。

```
你的手机 IM  ──→  LinkCode  ──→  Claude Code（本地终端）  ──→  你的代码仓库
```

- **Claude Code 是引擎** — 负责代码分析、生成与修改。
- **LinkCode 是遥控器** — 让你通过企业微信、飞书、Discord 等 IM 远程操控 Claude Code。

代码从不出本地。不注入额外 Prompt。不暴露 Web 面板。

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
