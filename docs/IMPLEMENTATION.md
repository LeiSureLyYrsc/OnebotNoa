# 实现笔记 · OnebotNoa

记录实际行为、关键不变式，以及相对设计文档的取舍。

## 关键不变式

- **事件零改写**：`hub.routeEvent` 直接投递上游原始字节给命中的 Bot 连接；只在消息里做路由判断。
- **动作 echo 重写**：`rewriteEcho` 把 `echo` 换成 `hub#<peerID>#<seq>` 后发往上游；
  响应回来时按重写值反查挂起表，恢复原始 `echo`（原本没有则删除该字段）再回投对应 Bot。
- **物理连接 / 逻辑账号分离**：`wsPeer` 是物理连接，`accountRuntime` 聚合同一 self_id 的连接。
- **限速在账号维度**：`bucketSet` 以 self_id 为键，多个 Bot 共用一个号时统一限流。

## 数据面流程

```
上游事件 → HandleUpstreamFrame → routeEvent →（Binding 过滤/scope）→ 各 Bot 连接
下游动作 → HandleBotFrame → 目标账号解析 → echo 重写 → 上游 API 连接 → 挂起
上游响应 → handleResponse → 还原 echo → 回投 Bot
```

## HTTP 兼容层

- 上游上报：`POST /onebot/v11/http/{token}`，body 为事件帧，按 token 识别账号后进入事件路由。
- 下游 API：`POST /onebot/v11/http/{token}/{action}`，用一个一次性 `httpPeer` 承接响应并同步返回。

## 元事件

上游 `meta_event` 默认不发往 Bot（避免下游账号状态错乱）。Bot 连接建立时由
`hub.synthesizeFor` 合成 `lifecycle{connect}`，并每 30 秒发送 `heartbeat`。
配置 `policy.meta_events` 可切到 `passthrough`/`drop`。

## 本地应答的 action

`get_status`、`get_version_info`、`get_login_info`、`can_send_image`、`can_send_record`、
以及扩展 `hub_list_accounts` 由中继本地应答，无需上游在线。

## 已知边界

- 上游拨号（`onebot.dial`）为静态配置，暂不支持在 WebUI 增删（监听端点支持动态增删）。
- 每次管理操作会触发 `hub.Reload()` 全量刷新绑定快照；数据量很大时可改为增量。
- WebUI 未做细粒度权限（当前仅 admin/非 admin 二分，且只内置 admin）。
