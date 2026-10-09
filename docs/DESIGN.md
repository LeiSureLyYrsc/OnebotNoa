# 设计建议 · OnebotNoa（OneBot V11 中继管理端）

> 结论先行：本中继的核心不是「转发」，而是**统一接入 + 鉴权 + 按 Bot 分配账号 + 账号级风控 + 可观性**。

## 1. 术语与角色

| 用词 | 含义 | 生态代表 | OneBot 角色 |
|---|---|---|---|
| **QQ 实例 / 上游** | 真正连 QQ 的进程 | NapCat、Lagrange、LLOneBot、go-cqhttp | 事件生产者、API 执行者 |
| **Bot 实例 / 下游** | 业务机器人框架 | NoneBot2、Koishi、ZeroBot、自研 | 事件消费者、API 调用者 |
| **中继 / Hub** | 本项目 | — | 对上游扮应用端，对下游扮实现端 |

## 2. 四种接入（按「谁拨号」命名，消灭正/反歧义）

| 模式 | 谁拨号 | 中继角色 |
|---|---|---|
| 上游监听 | QQ → 中继 | WS 服务端 |
| 上游拨号 | 中继 → QQ | WS 客户端 |
| 下游监听 | Bot → 中继 | WS 服务端 |
| 下游拨号 | 中继 → Bot | WS 客户端 |

上游拨号 / 下游拨号在 `config.yaml` 的 `onebot.dial[]` 中静态声明，连接时指数退避重连。

## 3. 单 WS 接入多个 QQ 实例

OneBot 规范中，每条连接由请求头 `X-Self-ID` + `X-Client-Role`（`API`/`Event`/`Universal`）标识。
因此「单端点多实例」= **一个监听端点承载 N 条连接**，无需自造多路复用协议：

- **物理连接键 = `(self_id, role, connID)`**；逻辑账号 `Account` 聚合其名下所有连接。
- 同一实例可能同时开 2~3 条连接（Event + API，或 Universal）：只有 `Event|Universal` 收事件，
  只有 `API|Universal` 发动作；只掉一半时账号标 `degraded`。
- 身份识别三级兜底：`token/路径` → `X-Self-ID` → 连接 5 秒内首帧 `self_id`。
- 未知 self_id 按 `unknown_account`（`pending`/`reject`/`auto`）处理。

## 4. 端口策略

- **默认**：共享端点（`/onebot/v11/ws`、`/onebot/v11/bot/ws`）。
- **另开端口 / 独立路径**：WebUI 中为账号或 Bot 新建 Listener 记录
  （`bind_addr + path + 归属`），运行时热重建（先起后停 + drain）。

## 5. 给 Bot 分配 QQ 实例

多对多 `bindings` + 事件过滤 `scope`（post_type、群/好友黑白名单、排除自聊、元事件档位）。
下游两种视图：

- **聚合视图** `/bot/{token}`：一条连接看到所有绑定账号（事件自带 `self_id`）。
- **透明单账号视图** `/bot/{token}/{self_id}`：行为与直连一致，动作无需指定账号。

动作目标解析优先级：帧顶 `self_id` → `params.self_id` → 默认账号 → 唯一绑定 → `1404`。

## 6. 协议红线

1. 全链路 `json.RawMessage` 透传，**绝不 map 往返**（int64 精度 / 未知字段 / 字段顺序 / message 的 string|array）。
2. `echo` 全局唯一化（`hub#<conn>#<seq>`）后还原；挂起表有上限 + 超时明确失败。
3. 元事件**合成而非透传**（合成 lifecycle/heartbeat，反映真实账号状态）。
4. self_id / user_id / group_id 内部用字符串/整数透传，跨边界保持原始 JSON。

## 7. 数据模型（SQLite）

- `accounts`：self_id、名称、token 哈希、状态、时间戳
- `bots`：名称、token 哈希、限速/策略 JSON
- `bindings`：bot_id ↔ account_id、优先级、默认标记、scope
- `listeners`：role、bind_addr、path、归属
- `users`/`sessions`/`audit_log`：管理面

token 明文只存在于 `connect.json`（AES-256-GCM 加密，密钥在同目录 `.key` 文件），数据库只存哈希。

## 8. 可靠性

每连接「读循环 + 独立写循环 + 有界 channel」，禁止在分发路径上直接写 socket；
每 Bot 独立队列、账号级令牌桶、挂起表上限、上游掉线指数退避重连 + ping/pong 探活。
