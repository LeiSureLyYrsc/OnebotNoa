# OnebotNoa

[![CI](https://github.com/LeiSureLyYrsc/OnebotNoa/actions/workflows/ci.yml/badge.svg)](https://github.com/LeiSureLyYrsc/OnebotNoa/actions/workflows/ci.yml)
[![Release](https://github.com/LeiSureLyYrsc/OnebotNoa/actions/workflows/release.yml/badge.svg)](https://github.com/LeiSureLyYrsc/OnebotNoa/actions/workflows/release.yml)
[![Container](https://img.shields.io/badge/image-ghcr.io%2Fleisurelyyrsc%2Fonebotnoa-blue)](https://github.com/LeiSureLyYrsc/OnebotNoa/pkgs/container/onebotnoa)

**OneBot V11 中继管理端** —— 单二进制、内嵌 WebUI（Windows XP 风格）。

让**一个 QQ 号同时服务多个 Bot**，也让**多个 QQ 实例共用一个接入地址**；鉴权、按 Bot 分配账号、
账号级风控限速与在线调试都在这一层完成。

---

## 特性

- **四种接入方式**（按「谁拨号」命名，避免正/反歧义）：

  | 名称 | 谁拨号 | 中继角色 | 端点 | 典型对端 |
  |---|---|---|---|---|
  | 上游监听 | QQ 实例 → 中继 | WS 服务端 | `/onebot/v11/ws` | NapCat / Lagrange 反向 WS |
  | 上游拨号 | 中继 → QQ 实现端 | WS 客户端 | `ws://qq:6700` | go-cqhttp 正向 WS |
  | 下游监听 | Bot → 中继 | WS 服务端 | `/onebot/v11/bot/ws/{token}` | NoneBot2 / Koishi 反向 WS |
  | 下游拨号 | 中继 → Bot | WS 客户端 | `ws://bot:8080/onebot/v11/ws` | Bot 自开的 WS 服务端 |

- **单个 WS 接入多个 QQ 实例**：用 OneBot 规范字段 `X-Self-ID` + `X-Client-Role` 区分，
  单端点即可承载全部实例；也可在 WebUI 为某实例**另开端口 / 独立路径**。
- **给 Bot 分配 QQ 实例**：多对多绑定 + 事件过滤范围（post_type、群/好友黑白名单、排除自聊、元事件档位）。
- **事件零改写**：事件帧按原始字节投递（不做 map 往返，int64 精度 / 未知字段 / 字段顺序全部保留）。
- **动作单播 + echo 重写**：`echo` 全局唯一化后还原，多个 Bot 撞 echo 也不会串。
- **可靠性**：账号级令牌桶（防风控）、慢消费者隔离、挂起表上限、超时明确失败而非静默挂起。
- **可观性**：结构化日志、审计、SSE 实时事件流、API 调试台、Prometheus 文本指标。
- **HTTP 兼容层**：上游 HTTP 上报 + 下游 HTTP API（`/onebot/v11/http/...`）。
- **单二进制**：WebUI `go:embed` 打包，`CGO_ENABLED=0` 纯 Go SQLite 跨平台编译。
- **配置分离**：`config.yaml` 静态配置（改后重启）；账号/Bot/绑定/token 由 WebUI 管理并持久化，
  连接与密钥可整体导出为 `connect.json`。

---

## 快速开始

### 1. Docker Compose（推荐）

```bash
docker compose up -d           # 使用 ghcr.io/leisurelyyrsc/onebotnoa:latest
# 或从本地源码构建：
docker compose up -d --build
docker compose logs onebotnoa | grep -i password   # 首次启动的管理员密码
```

打开 <http://127.0.0.1:8080> 登录。默认只绑定 `127.0.0.1`；要让局域网接入：
```bash
ONEBOTNOA_BIND=0.0.0.0 docker compose up -d
```
公网部署请置于 Nginx/Caddy 之后或自行配置 TLS。

### 2. 下载二进制

从 [Releases](https://github.com/LeiSureLyYrsc/OnebotNoa/releases) 下载 `onebotnoa-<os>-<arch>`：

```bash
./onebotnoa serve -config config.yaml
```

### 3. 从源码构建（需要 Go 1.27+ 与 Node 24+）

```powershell
# Windows
scripts\build.cmd -Web      # 构建 WebUI + 单二进制 -> test\bin\
scripts\dev.cmd  -Web        # 开发模式（Vite :5173 代理到后端）
```
```bash
# Linux / macOS
(cd web && npm ci && npm run build)
CGO_ENABLED=0 go build -o bin/onebotnoa ./cmd/onebotnoa
```

---

## 接入示例

### QQ 侧（NapCat / Lagrange / LLOneBot）

反向 WebSocket 指向中继，并把该实例的 token 作为访问令牌：

```text
ws://<hub-host>:8080/onebot/v11/ws
Access token: <该实例的 token（WebUI「QQ 实例」页生成）>
```

一个端点即可接多个 QQ 实例：每个实例用不同 token（或携带 `X-Self-ID` 头）；
都没有时中继会用连接 5 秒内的首帧 `self_id` 兜底。

### Bot 侧（NoneBot2 / Koishi）

```text
# 聚合视图：一条连接看到该 Bot 绑定的全部账号（事件自带 self_id）
ws://<hub-host>:8080/onebot/v11/bot/ws/<bot-token>

# 透明单账号视图：行为与直连该 QQ 实例一致，动作无需指定 self_id
ws://<hub-host>:8080/onebot/v11/bot/ws/<bot-token>/<self_id>
```

聚合视图下动作目标账号解析优先级：
① 帧顶 `self_id` → ② `params.self_id` → ③ 绑定里标记为默认的账号 → ④ 唯一绑定 → ⑤ 返回 `retcode 1404`。

---

## 端点一览

| 端点 | 方法 | 用途 |
|---|---|---|
| `/onebot/v11/ws` | WS | 上游共享端点（QQ 实例接入，`X-Self-ID` 区分） |
| `/onebot/v11/ws/api`、`/onebot/v11/ws/event` | WS | 按角色分离的上游端点 |
| `/onebot/v11/ws/{token}` | WS | 路径 token 预绑定实例 |
| `/onebot/v11/bot/ws/{token}[/{self_id}]` | WS | 下游 Bot 接入（聚合 / 透明单账号） |
| `/onebot/v11/http/{token}` | POST | 上游 HTTP 事件上报 |
| `/onebot/v11/http/{token}/{action}` | POST | 下游 HTTP API |
| `/api/v1/...` | REST | 管理 API（WebUI 使用） |
| `/api/v1/events/stream` | SSE | 实时事件流 |
| `/api/v1/connect` | GET/PUT | 导出 / 导入 connect.json |
| `/healthz` | GET | 健康检查（容器 healthcheck 使用） |
| `/metrics` | GET | Prometheus 文本指标 |
| `/` | GET | 内置 WebUI |

---

## 本地试跑（不用真的 QQ 端和 Bot）

`test/` 目录里有一键环境（产物与运行数据在 `.gitignore` 中）：

```powershell
node test\trial.js
```

它会构建 WebUI + 二进制、启动中继、经 API 建好示例账号 / Bot / 绑定，并打印 WebUI 地址、
登录口令、账号 token 与 Bot 地址。再开两个终端模拟两端：

```bash
node test/env/fake-peers.js qq  10001 <account_token>   # 假装 NapCat
node test/env/fake-peers.js bot <bot_token>             # 假装 NoneBot2
```

在 WebUI「实时事件」页即可看到消息在两端口之间流动。`Ctrl+C` 停止。

自动化端到端校验：

```bash
node test/env/e2e.js 10001 <account_token> <bot_token>
```

---

## 源码调试

- **VS Code + Delve**：仓库内置 `.vscode/launch.json`，直接 F5 以 `serve` 模式断点调试后端。
- **前后端联调**：`scripts\dev.cmd -Web` 启动后端 + Vite dev server（`:5173` 代理到 `:8080`，前端热更新）。
- **单元 / 集成测试**：`scripts\test.cmd`（`-Race` 需 C 工具链）。

常用环境变量（优先级高于 YAML）：

| 变量 | 说明 | 默认 |
|---|---|---|
| `ONEBOTNOA_LISTEN` | 管理面与数据面监听地址 | `127.0.0.1:8080` |
| `ONEBOTNOA_SQLITE` | 管理数据库路径 | `./data/onebotnoa.db` |
| `ONEBOTNOA_CONNECT_FILE` | 连接文档路径 | `./data/connect.json` |
| `ONEBOTNOA_CONNECT_KEY` | 连接文档加密密钥路径 | `./data/connect.json.key` |
| `ONEBOTNOA_ADMIN_PASSWORD` | 首启管理员密码（留空则随机生成并打印日志） | — |
| `ONEBOTNOA_LOG_LEVEL` | `debug`/`info`/`warn`/`error` | `info` |
| `ONEBOTNOA_LOG_FORMAT` | `json`/`text` | `json` |
| `ONEBOTNOA_TRUST_PROXY` | 置于反代之后时设 `true`，才会信任 `X-Forwarded-*` | `false` |

CLI：

```bash
onebotnoa serve -config config.yaml      # 启动
onebotnoa reset-password -user admin     # 重置密码（随机生成并打印，踢掉旧会话）
onebotnoa version
```

---

## 架构

```text
QQ 实例(NapCat/Lagrange) ──┐                          ┌── Bot(NoneBot2/Koishi)
  单端点多实例 X-Self-ID    │   ┌────────────────┐     │   聚合视图 / 透明单账号视图
                           ├──▶│    OnebotNoa   │◀────┤   正向/反向 WS
  上游拨号（中继拨出）       │   │ Account 注册表 │     │   下游拨号（中继拨出）
                           └──▶│ Binding 过滤   │◀────┘
                               │ echo 挂起表     │
                               │ 账号级令牌桶    │────── SQLite / WebUI(SSE)
                               └────────────────┘
```

目录：

```text
cmd/onebotnoa/      CLI 入口（serve / reset-password / version）
internal/config/    静态配置解析（YAML + 环境变量）
internal/model/     持久化实体
internal/store/     SQLite（modernc 纯 Go）+ 内嵌迁移
internal/connect/   connect.json 密钥库（AES-256-GCM）
internal/onebot/    协议类型、retcode、帧解析
internal/hub/       账号注册表、绑定路由、echo 挂起表、限速、事件总线、本地应答
internal/transport/ 四种 WS 接入、动态监听、HTTP 兼容层
internal/api/       管理 REST + SSE + 认证
internal/auth/      口令哈希、token、会话
internal/webui/     go:embed 的 WebUI 产物
web/                Vite + Preact + XP.css 源码
scripts/            构建 / 开发 / 测试脚本
test/               本地试跑环境与端到端校验（产物 gitignore）
```

---

## CI / CD

- **CI**（`.github/workflows/ci.yml`）：每次 push/PR 跑 `go vet` + `go test -race`、构建 WebUI、
  单二进制冒烟（`/healthz`）与 Docker 镜像构建（不推送）。
- **Release**（`.github/workflows/release.yml`）：打 `v*` tag 时交叉编译五个平台 + `SHA256SUMS`
  并上传 Release 资产，同时推送镜像到 GHCR（`latest` + semver + sha）。

```bash
docker pull ghcr.io/leisurelyyrsc/onebotnoa:latest
```

> GHCR 包首次推送后默认私有。发布工作流会尝试自动改为 public；若令牌权限不足会给出警告，
> 手动设置入口：`https://github.com/users/LeiSureLyYrsc/packages/container/onebotnoa/settings`。

---

## 文档

- [设计建议](docs/DESIGN.md)：为什么这样设计（角色、端口方案、数据模型、兼容性红线）
- [实现笔记](docs/IMPLEMENTATION.md)：实际行为、关键不变式与已知边界
- [配置说明](config.example.yaml)：每个配置项的含义与默认值

## 许可

暂未指定许可证，欢迎在 Issues 中提出建议。
