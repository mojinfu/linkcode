# LinkCode 开发进度

## 2026-05-16 — MVP 第一条消息通路打通

### 完成
- [x] 项目骨架、目录结构、Go module
- [x] 核心接口抽象 — `channel.Channel`（IM 平台）、`agent.Runner`/`agent.Session`（Agent 类型）
- [x] 配置加载 — YAML + 环境变量覆盖 + `.encrypt_key` 文件回退
- [x] MySQL 数据层 — `bots`、`sessions`、`messages` 三表 + 自动迁移
- [x] Bot 池管理 — AES-256-GCM 加密存储 bot secret、分配/回收/验证
- [x] Session 管理 — CRUD + waked/sleeped 状态
- [x] Agent 进程管理 — Claude Code CLI 子进程 + Go channel 桥接
- [x] 企微 WebSocket 实现 — `aibot_subscribe` 认证、消息收发、心跳保活、断线重连
- [x] 总控 Bot 菜单 — `/start`、`/addbot`、`/list`、`/end` 拨号菜单
- [x] 消息双向路由 — 上行（用户→Agent）、下行（Agent→用户）
- [x] Gateway 多 Bot 连接管理 — 总控 + 子 Bot 各自独立 WebSocket
- [x] Admin 面板 — `http://127.0.0.1:18980`
- [x] **端到端验证通过** — 企微中给总控 Bot 发 `/start`，收到拨号菜单回复

### 踩坑记录
1. 心跳必须用 `{"cmd":"ping"}` JSON 格式，WebSocket 协议级 Ping 不被企微识别
2. 回复 `aibot_respond_msg` 必须透传原始消息的 `req_id`（errcode=846605）
3. MySQL DSN 需加 `multiStatements=true` 支持多条 CREATE TABLE

### 下一步
- [ ] 端到端创建 Agent 流程 — 总控 /addbot → 池中分配子 Bot → 拉起 Claude Code → 子 Bot 主动对话
- [ ] 进程超时休眠/唤醒
- [ ] 消息历史持久化查询
- [ ] 清理调试日志、代码整理
