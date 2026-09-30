import { useEffect, useState } from "preact/hooks";
import { ApiError, getHealth, type HealthStatus } from "./api";

type Loadable<T> = { state: "loading" } | { state: "ready"; value: T } | { state: "error"; message: string };

export function App() {
  const [health, setHealth] = useState<Loadable<HealthStatus>>({ state: "loading" });
  const [now, setNow] = useState(() => new Date());

  useEffect(() => {
    let cancelled = false;
    getHealth()
      .then((value) => {
        if (!cancelled) setHealth({ state: "ready", value });
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        const message = err instanceof ApiError ? err.message : String(err);
        setHealth({ state: "error", message });
      });
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    const timer = window.setInterval(() => setNow(new Date()), 1000);
    return () => window.clearInterval(timer);
  }, []);

  return (
    <div class="hub-desktop">
      <div class="hub-workarea">
        <div class="window hub-window">
          <div class="title-bar">
            <div class="title-bar-text">OnebotNoa — 中继管理端</div>
            <div class="title-bar-controls">
              <button aria-label="Minimize" />
              <button aria-label="Maximize" />
              <button aria-label="Close" />
            </div>
          </div>
          <div class="window-body">
            <p>
              后端骨架已就绪（I0）。QQ 实例 / Bot 实例 / 绑定关系 / 实时事件流等页面按增量逐步接入。
            </p>
            <table class="hub-kv">
              <tbody>
                <tr>
                  <th>服务状态</th>
                  <td>{renderHealth(health)}</td>
                </tr>
                <tr>
                  <th>数据面端点</th>
                  <td>
                    <span class="hub-code">/onebot/v11/ws</span>（QQ 侧接入，单端点多实例） ·{" "}
                    <span class="hub-code">/onebot/v11/bot/ws</span>（Bot 侧接入）
                  </td>
                </tr>
                <tr>
                  <th>管理 API</th>
                  <td>
                    <span class="hub-code">/api/v1</span>（I1 起提供登录与资源接口）
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
          <div class="status-bar">
            <p class="status-bar-field">就绪</p>
            <p class="status-bar-field">OneBot V11</p>
            <p class="status-bar-field">单二进制</p>
          </div>
        </div>
      </div>

      <div class="hub-taskbar">
        <div class="hub-start">⊞ 开始</div>
        <div class="hub-tray">
          <span class="hub-muted">OnebotNoa</span>
          <span class="hub-clock">{now.toLocaleTimeString("zh-CN", { hour12: false })}</span>
        </div>
      </div>
    </div>
  );
}

function renderHealth(health: Loadable<HealthStatus>) {
  switch (health.state) {
    case "loading":
      return <span class="hub-muted">检测中…</span>;
    case "error":
      return <span class="hub-error">不可用：{health.message}</span>;
    case "ready":
      return (
        <span>
          正常 · 版本 <span class="hub-code">{health.value.version}</span> · 运行{" "}
          {formatUptime(health.value.uptime_sec)}
        </span>
      );
  }
}

function formatUptime(totalSeconds: number): string {
  const seconds = Math.max(0, Math.floor(totalSeconds));
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor((seconds % 86400) / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  const rest = seconds % 60;
  if (days > 0) return days + " 天 " + hours + " 小时";
  if (hours > 0) return hours + " 小时 " + minutes + " 分";
  if (minutes > 0) return minutes + " 分 " + rest + " 秒";
  return rest + " 秒";
}
