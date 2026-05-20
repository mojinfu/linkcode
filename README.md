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

## How LinkCode Compares / LinkCode 与其他方案的对比

### LinkCode & Claude / LinkCode 与 Claude 的关系

LinkCode is not a Claude alternative or competitor. It is an IM message proxy and security gateway for **Claude Code**.

> Claude Code is the engine — it analyzes, generates, and modifies code.
> LinkCode is the steering wheel — it lets you drive Claude Code remotely from WeCom, Lark, Discord, or any IM.

```
Your phone IM  →  LinkCode Server  →  Claude Code (your local terminal)  →  Your repo
```

- **Code never leaves your machine.** All file I/O and command execution happen locally. LinkCode only forwards messages.
- **No extra prompt injection.** LinkCode does not modify system prompts. Claude Code behaves exactly as it does in your terminal.
- **Least privilege.** No web admin panel exposed. Only SSH / local process communication.

---

### LinkCode vs. Clawdbot / LinkCode 与 Clawdbot 的异同

> Shared goal: control Claude Code from IM to write code remotely.
> 共同目标：在 IM 中遥控 Claude Code 写代码。

|  | LinkCode | Clawdbot |
|---|---|---|
| **Language / 语言生态** | Go — single static binary | Node.js / TypeScript — npm + Docker |
| **Deploy / 部署** | One binary, one config file | Requires Node.js runtime, npm install, Docker |
| **Prompt purity / Prompt 纯净度** | No injected system prompts. Claude behaves identically to terminal use. | Injects system-level prompts that alter Claude's behavior. |
| **Token efficiency / Token 效率** | Zero overhead — every token is yours | Extra system prompts consume tens to hundreds of tokens per message |
| **Model fidelity / 模型可控性** | Same as native Claude Code | Prompt conflicts may cause degraded reasoning |
| **Management / 管理** | CLI + YAML config | Web admin panel |
| **Multi-IM / 多平台** | Multi-IM by design (channel.Channel interface) | Single platform native; others via plugins |
| **Attack surface / 攻击面** | Minimal — no web panel, no plugin marketplace | Larger — web panel, plugin marketplace, npm dependency chain |

**Which one? / 怎么选？**

- **Choose LinkCode** if you value simplicity, transparency, security, and zero token waste —— 追求极简、透明、安全、零 Token 浪费
- **Choose Clawdbot** if you want a graphical admin panel, rich plugins, and an out-of-the-box experience —— 追求图形化管理、插件丰富、开箱即用

---

### LinkCode vs. Claude App Dispatch / 与 Claude App 内置 Dispatch 的区别

|  | LinkCode | Claude Dispatch |
|---|---|---|
| **Positioning / 定位** | Developer's remote control for coding | Consumer's cloud assistant for daily tasks |
| **Execution / 执行环境** | Your local machine | Remote execution environment |
| **Typical use / 典型场景** | Fix CI builds, deploy, check logs, debug production | Book tickets, shop online, fill forms |
| **Code security / 代码安全** | Code never leaves your machine | Not designed for code repo access |
| **Permission / 权限模型** | Your Linux file permissions | Web/app account credentials |

> Dispatch automates your life. LinkCode mobilizes your coding. Complementary, not competitive.
> Dispatch 帮你生活自动化，LinkCode 帮你编程移动化。两者互补，无竞争。

---

**LinkCode is the purest Claude Code IM gateway — built for developers who demand security and control.**
**LinkCode 是为对安全与可控性有极致要求的开发者打造的，最纯粹的 Claude Code IM 网关。**

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
