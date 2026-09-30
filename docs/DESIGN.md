# OnebotNoa — OneBot V11 中继管理端 设计建议

> 状态：设计提案 v0.1 ｜ 结论先行：见 §1
> 关键词：Go · WebUI · 反向/正向 WebSocket · 多 QQ 实例复用 · 绑定路由 · 全局限速

---

## 1. 核心结论

1. **两侧都是 OneBot V11，中继要“双面扮演”**：对 QQ 侧（OneBot 实现端）扮演应用端，对 Bot 侧（OneBot 应用端）扮演实现端。内部模型不要按“端口”建，要按 **Account（账号/上游逻辑会话）/ Bot（下游客户端）/ Binding（绑定 + 过滤）** 三层建。
2. **“单个 WS 接入多个 QQ 实例”用标准做法实现**：一个共享上游 WS 端点，靠规范里的 `X-Self-ID` + `X-Client-Role` 请求头区分实例与角色；**不要自造“一条连接多 self_id”的复用协议**，那会破坏与所有现成实现的兼容性。
3. **同时保留“独立端口 / 独立路径”作为可选能力**（方案 B/C），用于物理隔离、端口映射，以及给只认单账号的 Bot 提供“透明单账号视图”。
4. **协议层零改写透传**：`json.RawMessage` 直通原始帧，只在必要处改写 `echo`；绝不做“解析成 map 再序列化”。
5. **事件是多播，动作是单播**：事件按 Binding 过滤后广播给所有订阅者；API 调用必须唯一解析到一个上游账号。
6. **中继的产品价值 = 统一接入 + 鉴权 + 过滤 + 账号级全局限速 + 可观测 + 在线调试**，而不是单纯转发。
7. 技术栈：Go + chi/标准库 + gorilla/websocket + SQLite（modernc，免 cgo）+ `go:embed` 打包 Vue3 → **单二进制交付**。

---

## 2. 术语与角色（先把话说清楚）

| 本文用词 | 含义 | 生态代表 | 在 OneBot V11 中的角色 |
| --- | --- | --- | --- |
| **QQ 侧 / 上游 / OneBot 实现端** | 真正连 QQ 的程序 | NapCat、Lagrange.Core、LLOneBot、go-cqhttp | 事件**生产者**、API **执行者**；可开正向 WS 服务端，也可做反向 WS 客户端 |
| **Bot 侧 / 下游 / OneBot 应用端** | 业务机器人框架 | NoneBot2、Koishi、ZeroBot、自研 | 事件**消费者**、API **调用者**；可做正向 WS 客户端，也可开反向 WS 服务端 |
| **中继 / Hub** | 本项目 | — | 对上游当应用端，对下游当实现端 |

用户提的两种 WS 方式，落到实现就是：

- **“给 QQ 实例接入”** → 中继开 **上游反向 WS 服务端**（NapCat/Lagrange 配 `ws_reverse.url` 指过来）；以及/或者中继做 **上游正向 WS 客户端**（主动拨号连 go-cqhttp 的 `:6700`）。
- **“给 Bot 实例接入”** → 中继开 **下游正向 WS 服务端**（NoneBot 配反向连接到这里）；以及/或者中继做 **下游反向 WS 客户端**（连 Bot 自己开的 WS 服务端）。

---

## 3. 总体架构

```
        ┌───────────────────────────┐
        │  WebUI (Vue3, go:embed)   │  REST + SSE
        └────────────┬──────────────┘
                     │
┌──────────────┐     ▼              ┌─────────────────────────┐      ┌──────────────┐
│ QQ 实例       │  ①反向WS(共享)     │        OnebotNoa         │ ③正向WS(共享) │ Bot 实例      │
│ NapCat       │ ─────────────────▶ │                          │ ────────────▶│ NoneBot2     │
│ Lagrange     │  ②正向WS(拨出)     │  ┌────────────────────┐  │ ④反向WS(拨出) │ Koishi       │
│ LLOneBot     │ ◀───────────────── │  │ Account Registry   │  │ ◀──────────── │ 自研 Bot     │
│ go-cqhttp    │  ⑤HTTP 双向        │  │ Binding / Policy   │  │ ⑥HTTP 双向    │              │
└──────────────┘                    │  │ Router / RateLimit │  │              └──────────────┘
                                    │  │ EventLog / Metrics │  │
                                    │  └────────────────────┘  │
                                    │   SQLite (单文件持久化)   │
                                    └─────────────────────────┘
```

内部数据流：

```
上游事件帧 ─▶ Account 会话 ─▶ 读取元数据(载荷不动) ─▶ Binding 匹配/过滤 ─▶ 每 Bot 有界队列 ─▶ 下游连接
下游动作帧 ─▶ 解析(action/params/echo/self_id) ─▶ 解析目标 Account ─▶ 重写 echo ─▶ 上游连接 ─▶ 挂起表
上游响应帧 ─▶ 按重写后的 echo 反查挂起表 ─▶ 还原 echo ─▶ 回投对应 Bot 连接
```

---

## 4. 接入模式矩阵与实现优先级

| 优先级 | 模式 | 端点 | 说明 |
| --- | --- | --- | --- |
| **P0** | 上游反向 WS 服务端 | `GET /onebot/v11/ws` | QQ 实例主动连入。最常用，NAT 友好，一条 URL 服务所有实例 |
| **P0** | 下游正向 WS 服务端 | `GET /onebot/v11/ws/bot` | Bot 主动连入。最常用 |
| P1 | 上游正向 WS 客户端 | 中继拨号 `ws://qq-host:6700` | 兼容老 go-cqhttp 部署、Docker 内网 |
| P1 | 下游反向 WS 客户端 | 中继拨号 `ws://bot:8080/onebot/v11/ws` | Bot 侧不方便改配置时 |
| P2 | HTTP API + HTTP POST 上报 | `/onebot/v11/http/...` | 兼容性兜底；“快速操作”在多 Bot 下语义含混，见 §8 坑 12 |

建议：**WS 先做，HTTP 后做**。WS 覆盖 95% 场景。

---

## 5. 端口与路径方案（“单 WS 多 QQ 实例”怎么落地）

### 5.1 四种方案对比

| 方案 | 兼容性 | 身份识别 | 端口占用 | 适用 |
| --- | --- | --- | --- | --- |
| **A. 共享端点 + `X-Self-ID` 头** | 完全标准 | 请求头 → 首帧 `self_id` 兜底 | 1 | **默认推荐** |
| **B. 每实例独立路径** `/ws/qq/{token}` | 完全标准 | 路径 token | 1 | 强隔离；或实现端不发 `X-Self-ID` |
| **C. 每实例独立端口** `0.0.0.0:6710` | 完全标准 | 端口 → 账号映射 | N | 跨防火墙/端口映射、按实例限流 |
| D. 单连接自定义多路复用 | 破坏标准 | 自定义协议字段 | 1 | 仅当 QQ 侧接入器由本项目提供（不建议一上来就做） |

**推荐：A 为默认，B/C 做成 WebUI 上“一键开独立端点”的能力，D 不做或推迟到 v2。**

理由：A 已是社区事实标准——主流实现（NapCat、Lagrange、go-cqhttp、LLOneBot、Chronocat）反向 WS 时都会带 `X-Self-ID`，因此“一个端口接 N 个 QQ 实例”天然成立，无需发明新协议。用户要的“单个 WS 可接入多个 QQ 实例”，在标准语义下就是 **同一个监听端点承载多条连接，每条连接由一个 self_id 标识**。

### 5.2 端点规划

| 端点 | 方法 | 用途 |
| --- | --- | --- |
| `/onebot/v11/ws` | WS | **上游共享端点**：QQ 实例反向连接（靠 `X-Self-ID` 区分） |
| `/onebot/v11/ws/qq/{token}` | WS | 上游专用端点：token 直接映射到某账号（方案 B） |
| `/onebot/v11/ws/bot` | WS | **下游聚合视图**：一条连接看到所有绑定账号 |
| `/onebot/v11/ws/bot/{token}` | WS | 下游专用端点（token 决定 Bot 身份，推荐） |
| `/onebot/v11/ws/bot/{token}/{self_id}` | WS | **下游单账号透明视图**：行为与直连 QQ 实例一致 |
| `/onebot/v11/http/{token}` | POST/GET | 下游 HTTP API（+ 可选上游 HTTP 上报入口） |
| `/api/v1/...` | REST | 管理 API（WebUI 用） |
| `/api/v1/stream` | SSE | WebUI 实时事件流 |
| `/metrics` | GET | Prometheus（可选） |

**“另开端口”实现方式**：WebUI 中为账号或 Bot 新建 Listener 记录（`bind_addr + path + 归属对象 + 可选 TLS`），运行时为该记录单独起一个 `http.Server`。配置变更时平滑起停：先起新监听、再关旧监听，给 1~2 秒 drain。

### 5.3 下游两种视图（关键设计决策）

| 视图 | 路径 | 行为 | 适用 |
| --- | --- | --- | --- |
| **聚合视图** | `/ws/bot/{token}` | 一条连接接收所有已绑定账号事件（靠事件自带 `self_id` 区分）；动作需指明目标账号 | 支持多账号的框架（NoneBot2 多 adapter、自研） |
| **透明单账号视图** | `/ws/bot/{token}/{self_id}` | 只转发该账号事件；动作**无需任何额外字段**即可正确路由 | 只认单账号的框架；需要“像直连一样”的兼容场景 |

动作目标账号解析优先级（聚合视图）：

1. 帧顶层 `self_id`（中继扩展字段，部分客户端库支持）
2. `params.self_id`（宽松兼容）
3. Binding 中标记 `is_default` 的账号
4. 该 Bot 只绑定了一个账号 → 直接用（**最常见且完全兼容**）
5. 都不成立 → 返回 `{"status":"failed","retcode":1404,"data":null,"echo":...}`，并 WebUI 告警

---

## 6. 连接身份识别（三级兜底）

```
1) Authorization: Bearer <token> / ?access_token= / 路径 token
   → 若 token 已预绑定到某 Account，身份确定（推荐：一个 QQ 实例一个 token）
2) X-Self-ID 请求头（规范字段） → self_id
   X-Client-Role 请求头 → API | Event | Universal，决定该连接收事件还是发 API
3) 首帧兜底：连接后 3 秒内仍无身份，读第一条 meta_event/lifecycle|heartbeat 的 self_id 推断
4) 仍未识别 → 进入「待接入审批」队列，WebUI 提示，管理员一键绑定/拒绝
```

**极其重要**：同一个 QQ 实例在反向 WS 模式下**可能同时建立 2~3 条连接**（Event 客户端 + API 客户端，或一条 Universal）。所以：

- **物理连接键 = `(self_id, role, connID)`**，绝不能用 self_id 做唯一键。
- **逻辑账号 = Account 对象**，聚合名下所有物理连接：事件只从 `Event|Universal` 收，API 只往 `API|Universal` 发。
- 若只有 Event 连接而 API 连接掉线，账号状态标为 `degraded`（能收不能发），WebUI 必须显式展示，否则极难排查。

安全上做 **self_id 串号检测**：同一 self_id 被两个不同 token/IP 连入 → 拒绝新连接并告警。

---

## 7. 数据模型（SQLite）

```sql
-- 账号（QQ 实例）。self_id 用 TEXT 存，规避任何精度/类型争议
CREATE TABLE accounts (
  id            INTEGER PRIMARY KEY,
  self_id       TEXT    NOT NULL UNIQUE,
  name          TEXT,                       -- 管理别名
  nickname      TEXT,                       -- 来自 get_login_info
  avatar        TEXT,
  enabled       INTEGER NOT NULL DEFAULT 1,
  status        TEXT    NOT NULL DEFAULT 'offline', -- offline|online|degraded
  tags          TEXT    DEFAULT '[]',       -- JSON 数组
  last_seen_at  INTEGER,
  created_at    INTEGER NOT NULL
);

-- 下游 Bot 客户端
CREATE TABLE bots (
  id            INTEGER PRIMARY KEY,
  name          TEXT    NOT NULL UNIQUE,
  token_hash    TEXT    NOT NULL,           -- token 只存哈希
  enabled       INTEGER NOT NULL DEFAULT 1,
  rate_limit    TEXT    DEFAULT '{}',       -- JSON: 每秒/每分钟配额
  action_policy TEXT    DEFAULT '{}',       -- JSON: action 黑白名单
  note          TEXT,
  created_at    INTEGER NOT NULL
);

-- 绑定关系 + 过滤范围（核心表）
CREATE TABLE bindings (
  id          INTEGER PRIMARY KEY,
  bot_id      INTEGER NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  account_id  INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  priority    INTEGER NOT NULL DEFAULT 100,
  is_default  INTEGER NOT NULL DEFAULT 0,   -- 聚合视图下的兜底账号
  enabled     INTEGER NOT NULL DEFAULT 1,
  scope       TEXT NOT NULL DEFAULT '{}',
  UNIQUE (bot_id, account_id)
);

-- scope 示例：
-- {
--   "post_types": ["message","notice","request"],
--   "include_groups": ["123456"], "exclude_groups": [],
--   "include_users": [],          "exclude_users": [],
--   "exclude_self": true,          -- 丢弃机器人自己发的消息
--   "meta_events": "synthetic"     -- synthetic | passthrough | drop
-- }

-- 动态监听端点（“另开端口”）
CREATE TABLE listeners (
  id         INTEGER PRIMARY KEY,
  name       TEXT NOT NULL,
  role       TEXT NOT NULL,    -- upstream | downstream
  bind_addr  TEXT NOT NULL,    -- 0.0.0.0:6710
  path       TEXT NOT NULL,    -- /onebot/v11/ws
  account_id INTEGER REFERENCES accounts(id),  -- 固定归属（可空）
  bot_id     INTEGER REFERENCES bots(id),      -- 固定归属（可空）
  tls_cert   TEXT, tls_key TEXT,
  enabled    INTEGER NOT NULL DEFAULT 1
);

-- 管理用户 / 会话
CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT UNIQUE, password_hash TEXT,
                    role TEXT NOT NULL DEFAULT 'admin', created_at INTEGER);
CREATE TABLE sessions (token TEXT PRIMARY KEY, user_id INTEGER, expires_at INTEGER);

-- 审计
CREATE TABLE audit_log (id INTEGER PRIMARY KEY, at INTEGER, actor TEXT, action TEXT,
                        target TEXT, detail TEXT, ip TEXT);
```

存储建议：**Go 内部一律用 `int64` 处理 self_id/user_id/group_id，跨边界透传一律用 `json.RawMessage`**。只有前端展示时才转字符串（QQ 号 10 位远小于 2^53，但 `message_id` 可能很大，别赌精度）。

---

## 8. 路由核心

### 8.1 事件：多播

```go
for _, b := range bindingsOf(ev.AccountID) {
    if !b.Enabled || !matchScope(b.Scope, ev) { continue }
    enqueue(botConn(b.BotID), rawEventBytes)   // 原始字节，零改写
}
```

- 同一条事件**对每个命中的 Binding 各投递一次**（多个 Bot 可能都要处理同一群的消息）。
- 投递内容为**原始帧字节**，不注入任何字段：事件本身就带 `self_id`，聚合视图下 Bot 天然能区分。需要“来源标注”就只标在 WebUI/日志侧，不污染协议帧。确需注入时统一用保留命名空间 `x_hub`，且默认关闭。

### 8.2 动作：单播 + echo 映射

```go
// 下行帧（来自 Bot）
type actionFrame struct {
    Action string          `json:"action"`
    Params json.RawMessage `json:"params,omitempty"`
    Echo   json.RawMessage `json:"echo,omitempty"`
    SelfID json.Number     `json:"self_id,omitempty"` // 中继扩展：指定目标账号
}

// 上行响应帧（来自 QQ 实例）
type respFrame struct {
    Status  string          `json:"status"`
    Retcode int             `json:"retcode"`
    Data    json.RawMessage `json:"data"`
    Echo    json.RawMessage `json:"echo"`
}
```

关键点：

1. `echo` 由客户端任意指定，**两个不同 Bot 完全可能撞车**。中继必须重写为全局唯一值，如 `"hub#<botConnID>#<seq>"`，并维护挂起表反查：

```go
type pending struct {
    botConn  *BotConn
    origEcho json.RawMessage // 可为 nil（客户端没带 echo）
    deadline time.Time
    timer    *time.Timer
}
pendingByEcho map[string]pending // key = 重写后的 echo（带容量上限）
```

2. 响应回来：反查 → 把 `echo` 还原为 Bot 原值（若 Bot 原本没带 echo，则**删除该字段**）→ `Status/Retcode/Data` 原样回投。
3. 超时（默认 15s，可配）：向 Bot 返回失败响应并清理挂起项；挂起表必须有容量上限，防止恶意 Bot 打爆内存。
4. `_async` / `_rate_limited` 后缀**原样透传**，但 `_rate_limited` 的限速语义在多 Bot 下会被绕过 → 由中继在账号级做全局令牌桶（§9）。
5. 上游离线：按 Binding 策略 `queue（有界 + TTL 5~30s）` 或 `fail_fast（推荐默认）`，返回明确错误码而非静默挂起。

### 8.3 元事件处理（很影响下游体验）

- **不要**把上游 `meta_event/lifecycle`、`heartbeat` 原样广播给所有 Bot（会造成账号上下线状态错乱）。
- 推荐**在下游连接建立时合成**：先发 `lifecycle{"sub_type":"connect"}`；随后按固定周期合成 `heartbeat`，其中 `status.online` 反映账号真实状态（online/degraded/offline），`status.good` 反映健康度。这样 NoneBot2 等框架能正确显示“账号在线/离线”。
- `meta_events` 支持 `synthetic | passthrough | drop` 三档，绑定时可选。

### 8.4 中继可本地应答的 action

对下游而言中继本身就是“OneBot 实现端”，建议以下 action **本地应答**（上游可用时透传/合并，不可用时用缓存兜底）：

- `get_status` / `get_version_info` / `.get_version_info` / `get_login_info`
- `can_send_image` / `can_send_record`
- 自定义扩展（放 `x_hub` 命名空间，不污染标准 action）：`hub_list_accounts` / `hub_get_binding`，让 Bot 自查可用账号

---

## 9. 可靠性、背压与限速

| 项目 | 建议 |
| --- | --- |
| 每连接写模型 | 读循环 + 独立写循环（单写者）+ 有界 channel（如 1024 帧）。**禁止在事件分发路径上直接写 socket** |
| 慢 Bot 隔离 | 每 Bot 独立队列，策略 `drop_oldest`（默认）/ `drop_newest` / `disconnect` / `block(timeout)`。绝不能让一个慢 Bot 阻塞整个账号的事件循环 |
| 反压指标 | 每连接统计 `queued/dropped/last_lag_ms`，WebUI 高亮告警 |
| 上游掉线重连 | 指数退避 1s→60s + 抖动（按账号哈希错峰）；状态机 `connecting/online/degraded/offline` 全量展示 |
| 心跳探活 | WS ping/pong（30s/10s）+ OneBot `heartbeat` 事件双保险，阈值可配 |
| 账号级限速 | **per-account 全局令牌桶**（默认 1 msg/s，burst 5，可配）。多 Bot 共用一号时防风控的关键，也是相对直连的核心增值 |
| Bot 级配额 | per-bot per-account 令牌桶 + 并发上限，防止单 Bot 打满 |
| 队列/TTL | 动作队列有界（如 256）+ TTL；超限立即失败并计数 |
| 优雅重启 | 监听器热重建（先起后停 + drain 2s）；配置热加载不主动踢断现有连接 |
| 事件录制 | 内存 ring（默认 2000 条）+ 可选按天落盘 JSONL（滚动、可压缩），用于复现问题 |
| 可观测 | `/metrics`（连接数、事件速率、动作延迟 P50/P95/P99、丢帧、错误码分布）+ `log/slog` JSON + WebUI 仪表盘 |

---

## 10. 安全与权限

- **分层鉴权**：管理端（WebUI 用户会话）与数据端（Bot/账号 token）完全分离。Bot token **只存哈希**（bcrypt/argon2，或高强度随机串的 SHA-256）。
- **令牌策略**：一个 QQ 实例一个 token、一个 Bot 一个 token；token→身份预绑定，配合 `X-Self-ID` 双重校验。
- **能力收敛**：Binding 上可配 action 黑白名单（如禁止 `set_group_ban`、`delete_friend`、`.handle_quick_operation`）、群/好友范围、速率。Bot 只能操作绑定给它的账号，越权返回 1403。
- **传输**：内置 TLS（上传证书或 ACME）或置于 Nginx/Caddy 之后；正确处理 `X-Forwarded-For` / `X-Forwarded-Proto`。
- **基础防线**：WS 升级前 Origin 校验（可选）、请求体大小限制、并发连接上限、登录失败限速、CSRF token、会话有效期。
- **审计**：管理操作、权限变更、被拒 API 调用全部入 `audit_log`，WebUI 可检索。
- **部署**：默认只监听 `127.0.0.1` 或强制首启设置管理员密码；不要出现“公网 + 无 token”的组合。

---

## 11. WebUI 设计

技术：Vue 3 + Vite + TypeScript + Element Plus/Naive UI + ECharts，产物 `go:embed` 进二进制；开发期 Vite proxy 到后端。实时数据用 **SSE**（比 WS 简单、自动重连；过滤条件走查询参数）。

| 页面 | 内容 |
| --- | --- |
| **仪表盘** | 账号/连接/Bot 在线数、事件与动作速率曲线、错误码分布、丢帧与延迟、最近告警 |
| **QQ 实例（账号）** | 列表（self_id、昵称、状态、连接角色、延迟、绑定数）；详情：物理连接、事件流、API 调用历史、能力缓存 |
| **Bot 实例** | 列表 + token 生成/轮换/吊销；权限、配额、绑定管理 |
| **绑定关系** | 矩阵/拓扑视图（哪些 Bot 用哪些号）+ 过滤器编辑（群白名单等）+ 默认账号设置 |
| **实时事件流** | 全站/按账号/按 Bot 的实时帧流，关键字与 post_type/群号过滤、暂停、单帧“复现” |
| **API 调试台** | 选账号 → 选 action → 填 JSON → 发送 → 看原始响应。**最能提升口碑的功能** |
| **监听端点** | 共享端点开关、“另开端口”增删改、TLS、当前连接数 |
| **上游（拨出）配置** | 中继主动连出的 QQ 实例/Bot 目标与重连状态 |
| **日志 / 审计** | 结构化日志检索、审计导出 |
| **系统设置** | 管理员与密码、日志级别、心跳/超时/限速默认值、数据保留、备份导出 |

---

## 12. 技术选型与工程结构

| 关注点 | 推荐 | 理由 / 备选 |
| --- | --- | --- |
| HTTP 路由 | 标准库 `net/http`（Go 1.22 ServeMux 支持方法与通配）或 chi v5 | 依赖少；gin/echo 亦可但收益有限 |
| WebSocket | `github.com/gorilla/websocket` | 生态与示例最多、久经考验；备选 `github.com/coder/websocket`（context 友好、依赖少、内存占用低） |
| 数据库 | `modernc.org/sqlite`（纯 Go 免 cgo）+ `sqlc` | 交叉编译与单文件部署友好；避免 mattn/go-sqlite3 的 cgo 负担 |
| 迁移 | embed `.sql` + 30 行 runner（或 pressly/goose） | 简单可控 |
| 配置 | `gopkg.in/yaml.v3` + 环境变量覆盖 | 静态配置走文件，动态实体走 DB |
| 日志 | `log/slog`（标准库） | JSON、分级、零依赖 |
| 指标 | `prometheus/client_golang`（可选） | 也可先自研 /metrics 文本 |
| 密码 | `golang.org/x/crypto/bcrypt` / argon2id | — |
| CLI | cobra 或标准库 flag | 子命令：`serve` / `admin reset-password` / `version` |
| 前端 | Vue3 + Vite + TS + Element Plus + ECharts | 中文生态友好；React + AntD 亦可 |

```
cmd/onebotnoa/main.go
internal/
  config/       配置解析与默认值
  store/        sqlite、模型、迁移
  onebot/       协议类型、retcode、action 常量、元事件合成
  transport/    up_ws_server.go / up_ws_client.go / down_ws_server.go / down_ws_client.go / http_*.go
  account/      Account 会话聚合、物理连接管理、状态机
  bot/          Bot 注册表、连接、队列、配额
  binding/      Binding 匹配与 scope 求值
  router/       事件多播、动作单播、echo 挂起表、目标账号解析
  ratelimit/    令牌桶（账号级 / Bot 级）
  auth/         token、会话、权限
  api/          REST + SSE
  webui/        go:embed dist
web/            Vue3 源码
deploy/         Dockerfile、systemd、compose
docs/
```

两侧复用的统一接口：

```go
type Peer interface {
    ID() string
    Kind() PeerKind    // UpstreamQQ | DownstreamBot
    Role() Role        // Event | API | Universal
    SelfID() string    // 上游：账号；下游：空
    WriteFrame(ctx context.Context, raw []byte) error
    Close(code int, reason string) error
}
```

---

## 13. 配置样例

```yaml
server:
  listen: 127.0.0.1:8080
  admin_bootstrap_password: ""      # 为空则首启生成并打印到控制台
  # tls: { cert: /etc/onebotnoa/cert.pem, key: /etc/onebotnoa/key.pem }

onebot:
  upstream_ws:                      # ① QQ 实例反向接入（共享端点）
    enable: true
    path: /onebot/v11/ws
    require_token: true
    unknown_account_policy: pending # pending | reject | auto
  downstream_ws:                    # ③ Bot 接入（共享端点）
    enable: true
    path: /onebot/v11/ws/bot
  dedicated:                        # ⑤ 另开端口 / 独立路径（也可在 WebUI 动态增删）
    - name: qq-10001
      role: upstream
      addr: 0.0.0.0:6710
      path: /onebot/v11/ws
      account_self_id: "10001"
    - name: bot-legacy
      role: downstream
      addr: 0.0.0.0:6720
      path: /onebot/v11/ws
      bot_name: legacy-bot
      fixed_self_id: "10001"        # 透明单账号视图
  http:
    enable: false
    prefix: /onebot/v11/http

outbound:                           # ②④ 中继主动拨号
  - name: qq-10002
    role: upstream
    url: ws://10.0.0.5:6700
    token: "xxx"
    account_hint: "10002"
    reconnect: { min: 1s, max: 60s, jitter: 0.3 }
  - name: bot-nonebot
    role: downstream
    url: ws://10.0.0.9:8080/onebot/v11/ws
    token: "yyy"

policy:
  default_action_timeout: 15s
  offline_action_policy: fail_fast   # fail_fast | queue
  queue: { size: 256, ttl: 10s }
  bot_backpressure: drop_oldest
  meta_events: synthetic
  per_account_rate: { rate: 1, burst: 5 }

storage:
  sqlite: ./data/onebotnoa.db
  event_ring: 2000
  event_dump: { enable: false, dir: ./data/events, keep_days: 3 }

log: { level: info, format: json }
```

---

## 14. 兼容性红线（最容易踩的 14 个坑）

1. **不要“解析再序列化”**：`map[string]any` 往返会让 int64 变 float64、未知字段丢失、echo 与字段顺序改变，部分 Bot 会直接崩。全链路用 `json.RawMessage`。
2. **`X-Client-Role` 导致同一实例多条连接**：物理键必须是 `(self_id, role, connID)`，逻辑账号聚合多条连接。
3. **`echo` 必须重写并还原**；echo 可以是任意 JSON 类型（字符串/数字/对象/null），不是只有字符串。
4. **消息格式 string/array 原样透传**，绝不统一归一化（`message_format` 属于实现端的配置）。
5. **`_async` / `_rate_limited` 后缀透传**，但限速语义要由中继在账号级兜底。
6. **不要重编 `message_id`**：会破坏 `get_msg`、撤回、reply 语义。
7. **元事件合成而非透传**，否则下游框架账号状态错乱。
8. **`time` 等字段不要动**（透传自然满足）。
9. **上游缺 `X-Self-ID`** 时要有首帧兜底 + 待审批队列，不要直接拒。
10. **self_id 串号**：同一 self_id 两个来源 → 拒绝并告警。
11. **慢消费者隔离**：分发主循环不得被阻塞。
12. **HTTP POST 快速操作**在多 Bot 下语义含混：HTTP 模式建议先限定“单 Bot 直通”，多 Bot 时可选“第一个响应生效”或“合并”。
13. **上游离线时的动作语义**：明确失败（错误码 + 审计）优于静默挂起。
14. **分发循环里不做 IO**（DB 写、日志落盘、网络调用）——丢进 channel 由专职 goroutine 处理。

---

## 15. 路线图与验收标准

### M0 — 骨架 + 最小可用中继（1~2 周）
- Go 骨架、配置、SQLite 迁移、slog 日志
- 管理登录（单管理员 + 会话）
- 上游反向 WS 共享端点（`X-Self-ID` 识别）+ 账号注册表
- 下游正向 WS 端点 + Bot 注册（token）
- 1:1 绑定 + 原始帧透传 + echo 映射 + 动作超时
- WebUI：登录、账号列表、Bot 列表、绑定、实时事件流
- **验收**：NapCat 反向连入 → NoneBot2 正向连入 → 双向收发消息 + 图片/at 段正常；第三方框架不改代码可跑通

### M1 — 生产可用（2~3 周）
- 账号级限速、Bot 配额、action 黑白名单
- 背压策略与指标、上游正向 WS 客户端、下游反向 WS 客户端
- 元事件合成、`get_*` 本地应答与缓存
- 心跳探活、重连退避、状态机与告警
- WebUI：仪表盘、API 调试台、日志检索、审计
- **验收**：断网/杀进程后状态正确、限速生效、单 Bot 卡死不拖垮他人；`go test ./...` 全绿（含假 OneBot 实现端的集成测试）

### M2 — 多实例与隔离（2~3 周）
- 动态监听器（“另开端口/独立路径”）+ 热重建
- 下游透明单账号视图、动态端口
- 群/好友白名单、关键词与频率过滤
- HTTP 通信方式（API + 上报）
- TLS、Prometheus、事件录制导出、Docker/systemd
- **验收**：单端点接入 ≥20 个 QQ 实例 + ≥50 个 Bot 压测稳定；端口/路径隔离下互不可见

### M3 — 进阶（按需）
- 事件持久化与回放、离线动作队列
- 多实例集群（Redis/NATS 共享状态）
- 过滤器 DSL / 插件、消息审计与回溯检索
- 自建“QQ 侧接入器”（若要支持自定义多路复用方案 D）

---

## 16. 待确认的决策点

1. **下游默认视图**：默认“聚合视图”还是“透明单账号视图”？（建议聚合为默认，单账号作为路径选项）
2. **是否每实例独立 token**？公网部署强烈建议独立 token 而非共享 token。
3. **限速默认值**：1 msg/s + burst 5 是否合适（取决于目标号风控等级）？
4. **是否要 HTTP 通信方式**？（增量成本不低，尤其“快速操作”语义）
5. **多租户**：单管理员够用，还是多用户 + 每人只见部分 Bot/账号？
6. **命名**：`botuniverse/onebot-hub` 已存在（2021 年创建的空壳仓库，仅 81 字节 README、无任何代码、未更新），模块路径与项目名注意区分，避免与社区预期混淆。

---

## 附：生态现状

- 官方组织 botuniverse 下确有一个占位仓库 onebot-hub（描述 “OneBot, but many.”），但**只有 81 字节 README、无任何实现**，2021 年后未更新。GitHub 上也搜不到成熟的开源 OneBot 中继实现——社区通常靠“给每个 Bot 直连一个 QQ 实例”硬扛。
- 说明“中继”在 OneBot 生态里长期是空白，本项目的差异化价值应落在：**统一接入 + 多 Bot 复用同一 QQ 号 + 账号级风控限速 + 可观测与在线调试**，而不只是端口转发。
