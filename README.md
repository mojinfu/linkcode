<div align="center">
  <img src="assets/logo.png" alt="LinkCode" width="120">
</div>

<div align="center">

```
    ██╗     ██╗███╗   ██╗██╗  ██╗ ██████╗ ██████╗ ██████╗ ███████╗
    ██║     ██║████╗  ██║██║ ██╔╝██╔════╝██╔═══██╗██╔══██╗██╔════╝
    ██║     ██║██╔██╗ ██║█████╔╝ ██║     ██║   ██║██║  ██║█████╗
    ██║     ██║██║╚██╗██║██╔═██╗ ██║     ██║   ██║██║  ██║██╔══╝
    ███████╗██║██║ ╚████║██║  ██╗╚██████╗╚██████╔╝██████╔╝███████╗
    ╚══════╝╚═╝╚═╝  ╚═══╝╚═╝  ╚═╝ ╚═════╝ ╚═════╝ ╚═════╝ ╚══════╝
```

[![Go](https://img.shields.io/badge/Go-1.21+-00ADD8?style=for-the-badge&logo=go&logoColor=white)]()
[![WeCom](https://img.shields.io/badge/WeCom-企业微信-07C160?style=for-the-badge&logo=wechat&logoColor=white)]()
[![License](https://img.shields.io/badge/License-MIT-yellow?style=for-the-badge)]()

</div>

<br>

<div align="center">
  <em>在 IM 里说句话，专属 Bot 主动来找你，背后就是你的 AI Agent。</em>
  <br>
  <em>Say a word in IM. A dedicated bot reaches out. An AI agent runs behind it.</em>
</div>

<br>

---

## 🎬 总控唤起：从一句话到一个专属 Bot

<div align="center">
  <img src="assets/demo-control.svg" alt="LinkCode 总控唤起流程">
</div>

<br>

## 🎬 任务执行：让 Bot 动手操作你的电脑

<div align="center">
  <img src="assets/demo-task.svg" alt="LinkCode 任务执行流程">
</div>

---

## ⚡ 开始使用

```bash
# 交互式安装 (自动检测依赖)
bash scripts/install.sh

# 编辑配置
vim configs/linkcode.yaml

# 启动
./bin/linkcode -config configs/linkcode.yaml
```

然后打开企业微信，给总控 Bot 发 `/start`。

---

## 🗺️ Roadmap

| IM 平台 | 状态 | Agent 工具 | 状态 |
|---------|:----:|-----------|:----:|
| 企业微信 | ✅ | Claude Code | ✅ |
| Telegram | 🔲 | Kimi Code | 🔲 |
| Teams | 🔲 | | |

> 扩展只需实现 `channel.Channel` 或 `agent.Runner` 接口，其余模块零改动。

---

## 💬 交互方式

### 总控 Bot：拨号菜单

给总控 Bot 发送 `/start` 进入菜单，回复数字选择操作：

```
欢迎使用 LinkCode！请选择操作：
1. 创建新 Agent
2. 查看我的 Agent 列表
3. 添加新 Bot
4. 查看 Bot 池状态
5. 结束 Agent

请回复数字 1-5
💡 引用本条消息后回复数字，选择更准确
```

**推荐做法**：长按总控 Bot 的菜单消息 →「引用」→ 回复数字。引用消歧 100% 准确，不依赖对话顺序。

### Worker Bot：自由对话 + 可选引用

与 Worker Bot（Claude Code）自由对话。引用 Worker Bot 的消息回复时，引用内容会作为上下文传递给 Claude：

```
[用户引用了以下消息]
Claude 之前的回复内容
[用户的新消息]
用户的新消息内容
```

Sessions are persisted via `--resume`. Reply to a dormant bot and it wakes up with full context.

---

## 📂 项目结构

```
linkcode/
├── cmd/
│   ├── linkcode/        # 主程序入口
│   └── devmsg/          # 开发用消息发送工具
├── internal/
│   ├── gateway/         # 企微 WebSocket 连接管理
│   ├── controller/      # 总控 Bot 菜单状态机
│   ├── botpool/         # Bot 池（录入/分配/回收）
│   ├── session/         # Session CRUD + 绑定关系
│   ├── router/          # 消息双向路由（引用上下文传递）
│   ├── procman/         # Claude Code 子进程管理
│   ├── agent/claude/    # Claude Code Runner
│   ├── channel/wecom/   # 企微 WebSocket 实现（含引用解析）
│   ├── store/           # MySQL 数据层
│   ├── crypto/          # AES 加密
│   └── admin/           # Web 管理面板
├── scripts/
│   └── install.sh       # 一键安装脚本
├── migrations/          # SQL DDL
└── configs/             # 配置模板
```

---

<div align="center">

**[LinkCode](https://github.com/mojinfu/linkcode)** · MIT License

</div>
