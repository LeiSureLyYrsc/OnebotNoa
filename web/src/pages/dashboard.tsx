import { useEffect, useState } from "preact/hooks";
import { ApiError, listAccounts, systemStatus, type SystemStatus } from "../api";
import { Empty, Window } from "../ui";
import { go } from "../state";

interface Snapshot {
  status: SystemStatus | null;
  accounts: number;
  error: string;
}

export function DashboardPage() {
  const [snap, setSnap] = useState<Snapshot>({ status: null, accounts: 0, error: "" });

  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      try {
        const [status, accounts] = await Promise.all([systemStatus(), listAccounts()]);
        if (cancelled) return;
        setSnap({ status, accounts: accounts.accounts.length, error: "" });
      } catch (err) {
        if (cancelled) return;
        setSnap((prev) => ({ ...prev, error: err instanceof ApiError ? err.message : String(err) }));
      }
    };
    void load();
    const timer = window.setInterval(load, 5000);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, []);

  const relay = snap.status?.relay;
  const events = snap.status?.events;
  const traffic = snap.status?.traffic;

  return (
    <Window
      title="仪表盘 — OnebotNoa"
      tools={
        <>
          <button onClick={() => void go("accounts")}>QQ 实例</button>
          <button onClick={() => void go("bots")}>Bot 实例</button>
          <button onClick={() => void go("events")}>实时事件</button>
          <span class="hub-spacer" />
          <span class="hub-muted">每 5 秒刷新</span>
        </>
      }
      footer={
        <>
          <p class="status-bar-field">版本 {snap.status?.version ?? "…"}</p>
          <p class="status-bar-field">监听 {snap.status?.listen ?? "…"}</p>
          <p class="status-bar-field">数据库 {snap.status?.database ?? "…"}</p>
        </>
      }
    >
      {snap.error ? <p class="hub-error">{snap.error}</p> : null}

      <div class="hub-cards">
        <Card title="QQ 实例" value={String(relay?.accounts ?? snap.accounts)} hint={"在线 " + (relay?.online ?? 0) + " · 降级 " + (relay?.degraded ?? 0)} />
        <Card title="上游连接" value={String(relay?.upstream_conns ?? 0)} hint="QQ 侧物理连接数（一个实例可能多条）" />
        <Card title="下游连接" value={String(relay?.downstream_conns ?? 0)} hint="Bot 侧物理连接数" />
        <Card title="待接入" value={String(relay?.pending_accounts ?? 0)} hint="未批准的 self_id 尝试" />
        <Card title="在途动作" value={String(relay?.pending_actions ?? 0)} hint="等待上游响应的 API 调用" />
        <Card title="事件环形缓冲" value={String(events?.ring_size ?? 0)} hint={"订阅 " + (events?.subscribers ?? 0) + " · 丢弃 " + (events?.dropped ?? 0)} />
        <Card title="已转发动作" value={String(traffic?.actions_forwarded ?? 0)} hint={"上游帧 " + (traffic?.upstream_frames ?? 0) + " · 回投 " + (traffic?.responses_forwarded ?? 0)} />
        <Card
          title="限速拒绝"
          value={String((traffic?.rate_limited_account ?? 0) + (traffic?.rate_limited_bot ?? 0) + (traffic?.rate_limited_inflight ?? 0))}
          hint={"账号 " + (traffic?.rate_limited_account ?? 0) + " · Bot " + (traffic?.rate_limited_bot ?? 0) + " · 并发 " + (traffic?.rate_limited_inflight ?? 0)}
        />
        <Card title="超时 / 策略拒绝" value={String(traffic?.actions_timed_out ?? 0)} hint={"策略拒绝 " + (traffic?.actions_rejected ?? 0) + " · 离线排队 " + (traffic?.offline_queued ?? 0)} />
      </div>

      <h3>接入地址</h3>
      <table class="hub-kv">
        <tbody>
          <tr>
            <th>QQ 侧（上游）</th>
            <td>
              <span class="hub-mono">ws://&lt;host&gt;:&lt;管理面端口&gt;/onebot/v11/ws</span>
              <div class="hub-muted">NapCat / SnowLuma 反向 WS；实例级 token 在「QQ 实例」页生成</div>
            </td>
          </tr>
          <tr>
            <th>Bot 侧（下游）</th>
            <td>
              <span class="hub-mono">ws://&lt;host&gt;:&lt;管理面端口&gt;/onebot/v11/bot/ws/&lt;bot-token&gt;</span>
              <div class="hub-muted">聚合视图；追加 <span class="hub-mono">/&lt;self_id&gt;</span> 得到透明单账号视图</div>
            </td>
          </tr>
        </tbody>
      </table>

      {snap.status ? null : <Empty text="正在读取系统状态…" />}
    </Window>
  );
}

function Card(props: { title: string; value: string; hint: string }) {
  return (
    <div class="hub-card">
      <div class="hub-card-title">{props.title}</div>
      <div class="hub-card-value">{props.value}</div>
      <div class="hub-card-hint hub-muted">{props.hint}</div>
    </div>
  );
}
