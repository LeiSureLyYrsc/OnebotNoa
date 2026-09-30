# OnebotNoa

**OneBot V11 中继管理端** —— 单二进制、内置 WebUI（Windows XP 风格）。

让**一个 QQ 号同时服务多个 Bot**，也让**多个 QQ 实例共用一个接入地址**；鉴权、按 Bot 分配账号、账号级风控限速与在线调试都在这一层完成。

> 生态现状：OneBot 侧长期缺少成熟的中继实现，社区通常靠「每个 Bot 直连一个 QQ 实例」硬扛。

---

## 特性

- **四种接入方式**（按「谁拨号」命名，避免正/反向歧义）
  | 名称 | 谁拨号 | 端点 | 典型对端 |
  |---|---|---|---|
  | 上游监听 `upstream_listen` | QQ 实例 → 中继 | `/onebot/v11/ws` | NapCat / SnowLuma 配 reverse-WS |
  | 上游拨号 `upstream_dial` | 中继 → QQ 实现端 | `ws://qq:6700/` | 老 go-cqhttp 的正向 WS |
  | 下游监听 `downstream_listen` | Bot → 中继 | `/onebot/v11/bot/ws` | 支持拨出的 Bot 框架 |
  | 下游拨号 `downstream_dial` | 中继 → Bot | `ws://bot:8080/onebot/v11/ws` | NoneBot2 / Koishi 的反向 WS |
- **单个 WS 接入多个 QQ 实例**：用 OneBot 规范的 `X-Self-ID` + `X-Client-Role` 区分，单端口即可承载全部实例；也可按实例另开端口/独立路径。
- **按 Bot 分配 QQ 实例**：多对多绑定 + 过滤范围（post_type、群/好友黑白名单、排除自聊、元事件档位）。
- **事件零改写多播**：事件帧按原始字节投递（不做 map 往返，int64 精度/未知字段/字段顺序全部保留）。
- **动作单播 + echo 重写**：`echo` 全局唯一化后还原，多个 Bot 使用同一个 echo 也不会串；params 原样透传。
- **流式动作支持**：识别 SnowLuma 的 `type=stream` 多帧响应，不会把流截断。
- **可靠性**：账号级令牌桶（防风控）、Bot 配额、慢消费者隔离、挂起表上限、超时明确失败而非静默挂起。
- **可观测**：结构化日志、审计、实时事件流（SSE）、API 调试台、Prometheus 文本指标。
- **单二进制**：WebUI 通过 `go:embed` 打包，`CGO_ENABLED=0` 跨平台编译（纯 Go SQLite）。

---

## 快速开始

### 1. Docker Compose（推荐）

```bash
git clone https://github.com/LeiSureLyYrsc/OnebotNoa.git
cd OnebotNoa
docker compose up -d --build     # 本地构建镜像，不依赖 GHCR 可见性

# 首次启动会生成管理员密码，从日志里取
docker compose logs onebotnoa | grep -i password
```

也可以直接拉取 CI 构建好的镜像（需先把 GHCR 包设为 Public，见下文）：

```bash
docker compose up -d             # 使用 ghcr.io/leisurelyyrsc/onebotnoa:latest
```

打开 <http://127.0.0.1:8080> 登录。默认只绑定 `127.0.0.1`；要让局域网内其它机器接入，设置
```bash
ONEBOTNOA_BIND=0.0.0.0 docker compose up -d
```
（公网部署请置于 Nginx/Caddy 之后，或自行配置 TLS。）

### 2. 下载二进制

从 [Releases](https://github.com/LeiSureLyYrsc/OnebotNoa/releases) 取 `onebotnoa-<os>-<arch>`：

```bash
./onebotnoa serve -config config.yaml
```

### 3. 从源码构建（需要 Go 1.26+ 与 Node 24+）

```powershell
# Windows
scripts\build.cmd -Web        # 构建 WebUI + 单二进制到 bin/
scripts\dev.cmd -Web          # 开发模式（Vite :5173 代理到后端）
````
```bash
# Linux / macOS
(cd web && npm ci && npm run build)
CGO_ENABLED=0 go build -o bin/onebotnoa ./cmd/onebotnoa
```

---

## 接入示例

### QQ 侧（NapCat / SnowLuma）

反向 WebSocket 指向中继，并在 WebUI 里把该实例的 token 绑定到账号：

```text
ws://<hub-host>:8080/onebot/v11/ws
Access token: <该实例的 token（WebUI 生成）>
```

一个端口即可接多个 QQ 实例：每个实例用不同 token，中继靠 token 或 `X-Self-ID` 识别身份
（都缺失时用连接后 3 秒内的首帧 `self_id` 兜底）。

### Bot 侧（NoneBot2 / Koishi）

两种视图：

```text
# 聚合视图：一条连接看到该 Bot 绑定的全部账号（事件自带 self_id）
ws://<hub-host>:8080/onebot/v11/bot/ws/<bot-token>

# 透明单账号视图：行为与直连该 QQ 实例一致，动作无需指定 self_id
ws://<hub-host>:8080/onebot/v11/bot/ws/<bot-token>/<self_id>
```

聚合视图下动作的目标账号解析优先级：
① 帧顶层 `self_id` → ② `params.self_id` → ③ 绑定里标记为默认的账号 → ④ 该 Bot 只绑定了一个账号 → ⑤ 均不成立返回 `retcode 1404`。

---

## 端点一览

| 端点 | 方法 | 用途 |
|---|---|---|
| `/onebot/v11/ws` | WS | 上游共享端点（QQ 实例接入，`X-Self-ID` 区分） |
| `/onebot/v11/ws/api`、`/onebot/v11/ws/event` | WS | 按角色分离的上游端点 |
| `/onebot/v11/ws/{token}` | WS | 路径 token 预绑定实例 |
| `/onebot/v11/bot/ws[/{token}[/{self_id}]]` | WS | 下游 Bot 接入（聚合 / 透明单账号） |
| `/api/v1/...` | REST | 管理 API（WebUI 使用） |
| `/api/v1/events/stream` | SSE | 实时事件流 |
| `/healthz` | GET | 健康检查（容器 healthcheck 使用） |
| `/metrics` | GET | Prometheus 文本指标 |
| `/` | GET | 内置 WebUI |

---

## 配置

进程级静态配置走 YAML（见 [config.example.yaml](config.example.yaml)），动态实体（账号 / Bot / 绑定 / 监听端口）在 WebUI 里管理并存于 SQLite。

常用环境变量（优先级高于 YAML）：

| 变量 | 说明 | 默认 |
|---|---|---|
| `ONEBOTNOA_LISTEN` | 管理面与数据面监听地址 | `127.0.0.1:8080` |
| `ONEBOTNOA_SQLITE` | SQLite 文件路径 | `./data/onebotnoa.db` |
| `ONEBOTNOA_ADMIN_PASSWORD` | 首启管理员密码（留空则随机生成并打印日志） | 空 |
| `ONEBOTNOA_LOG_LEVEL` | `debug|info|warn|error` | `info` |
| `ONEBOTNOA_LOG_FORMAT` | `json|text` | `json` |
| `ONEBOTNOA_TRUST_PROXY` | 置于反代之后时设为 `true`，才会信任 `X-Forwarded-*` | `false` |

CLI：

```bash
onebotnoa serve -config config.yaml      # 启动
onebotnoa reset-password -user admin     # 重置密码（随机生成并打印，踢掉旧会话）
onebotnoa version
```

---

## 架构

```text
QQ 实例(NapCat/SnowLuma) ──┐                        ┌── Bot(NoneBot2/Koishi)
  单端口多实例 X-Self-ID    │   ┌──────────────┐    │   聚合视图 / 透明单账号视图
                           ├──▶│  OnebotNoa   │◀───┤
  正向 WS（中继拨号）       │   │ Account 注册表│    │   反向 WS（中继拨号）
                           └──▶│ Binding 过滤  │◀───┘
                               │ echo 挂起表   │
                               │ 账号级限速    │──────▶ SQLite / WebUI(SSE)
                               └──────────────┘
```

目录：

```text
cmd/onebotnoa/      CLI 入口（serve / reset-password / version）
internal/onebot/    协议类型、retcode、动作/响应信封
internal/hub/       连接、账号会话、绑定路由、echo 挂起表、限速、事件总线
internal/transport/ 上游/下游 WS 端点、动态监听、HTTP 兼容层
internal/store/     SQLite + 内嵌迁移
internal/api/       管理 REST + SSE
internal/auth/      会话、CSRF、密码与 token
internal/webui/     go:embed 的 WebUI 产物
web/                Vite + Preact + XP.css 源码
```

---

## 开发

```bash
go test ./...              # 单元 + 集成测试（含假 OneBot 实现端/Bot 的端到端闭环）
go vet ./...
(cd web && npm run build)  # 类型检查 + 构建 WebUI
```

CI（[.github/workflows/ci.yml](.github/workflows/ci.yml)）在每次 push/PR 跑 vet + race 测试、WebUI 构建、单二进制冒烟与 Docker 镜像构建；
打标签（`v*`）时 [release.yml](.github/workflows/release.yml) 会交叉编译五个平台并推送到 GHCR：

```bash
docker pull ghcr.io/leisurelyyrsc/onebotnoa:latest
```
> GHCR 上的包首次推送后**默认私有**。发布工作流会尝试自动改为 public，若令牌权限不足则给出警告；
> 手动设置入口：<https://github.com/users/LeiSureLyYrsc/packages/container/onebotnoa/settings> → Change visibility → Public。
> 未设为 public 时请用 `docker compose up -d --build` 本地构建。

---

## 路线图

- [x] I0 骨架 / 脚本 / 单二进制 / embed 兜底
- [x] I1 SQLite + 迁移 + 管理登录（pbkdf2 / 会话 / CSRF / 审计）
- [x] I2 上游监听：多实例单端点、五级身份识别、账号状态机、串号检测、待接入队列
- [x] I3 下游监听 + 动作路由：两种视图、目标解析、echo 重写、超时、流式动作、事件多播
- [ ] I4 管理 REST（账号/Bot/绑定/监听）+ SSE + WebUI 页面
- [ ] I5 账号级令牌桶、Bot 配额、策略黑白名单、背压、/metrics
- [ ] I6 双向拨号（上游/下游）+ 退避重连 + 探活
- [ ] I7 元事件合成、本地应答、仪表盘、API 调试台
- [ ] I8 HTTP 兼容层（HTTP API + 上报）
- [ ] I9 动态监听端口/路径、TLS、部署产物

---

## 许可

暂未指定许可证，欢迎在 Issues 中提出建议。
