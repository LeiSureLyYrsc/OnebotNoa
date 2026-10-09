import { useState } from "preact/hooks";
import { api } from "../api";
import { useInterval, Window } from "../ui";

type System = Awaited<ReturnType<typeof api.system>>;
type Connections = Awaited<ReturnType<typeof api.connections>>;

export function DashboardPage() {
  const [system, setSystem] = useState<System | null>(null);
  const [conns, setConns] = useState<Connections | null>(null);
  const [error, setError] = useState("");

  useInterval(() => {
    api.system().then(setSystem).catch((e) => setError(String(e.message || e)));
    api.connections().then(setConns).catch(() => undefined);
  }, 3000);

  const metrics = system?.metrics || {};

  return (
    <div class="stack">
      <Window title="概览">
        {error ? <p style={{ color: "#a00" }}>{error}</p> : null}
        <table class="grid">
          <tbody>
            <tr>
              <th>版本</th>
              <td>{system?.version || "-"}</td>
              <th>运行时长</th>
              <td>{system ? `${system.uptime_seconds}s` : "-"}</td>
            </tr>
            <tr>
              <th>QQ 实例</th>
              <td>
                {system ? `${system.accounts_online} / ${system.accounts_total} 在线` : "-"}
              </td>
              <th>Bot 实例</th>
              <td>{system ? `${system.bots_online} / ${system.bots_total} 在线` : "-"}</td>
            </tr>
          </tbody>
        </table>
        {system?.pending_accounts?.length ? (
          <p style={{ color: "#a60" }}>待审批的 QQ 实例：{system.pending_accounts.join(", ")}</p>
        ) : null}
      </Window>

      <Window title="数据面指标">
        <table class="grid">
          <thead>
            <tr>
              {Object.keys(metrics).map((k) => (
                <th key={k}>{k}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            <tr>
              {Object.keys(metrics).map((k) => (
                <td key={k}>{metrics[k]}</td>
              ))}
            </tr>
          </tbody>
        </table>
      </Window>

      <Window title="当前连接">
        <div style={{ display: "flex", gap: "12px", flexWrap: "wrap" }}>
          <div style={{ flex: "1 1 320px" }}>
            <h4>QQ 实例物理连接</h4>
            <div class="sunken">
              <table class="grid">
                <thead>
                  <tr>
                    <th>self_id</th>
                    <th>角色</th>
                    <th>连接 ID</th>
                  </tr>
                </thead>
                <tbody>
                  {(conns?.upstream || []).map((c) => (
                    <tr key={c.peer_id}>
                      <td>{c.self_id}</td>
                      <td>{c.role}</td>
                      <td class="muted">{c.peer_id}</td>
                    </tr>
                  ))}
                  {conns && conns.upstream.length === 0 ? (
                    <tr>
                      <td colSpan={3} class="muted">
                        无
                      </td>
                    </tr>
                  ) : null}
                </tbody>
              </table>
            </div>
          </div>
          <div style={{ flex: "1 1 320px" }}>
            <h4>Bot 连接</h4>
            <div class="sunken">
              <table class="grid">
                <thead>
                  <tr>
                    <th>Bot</th>
                    <th>在线连接</th>
                  </tr>
                </thead>
                <tbody>
                  {(conns?.downstream || []).map((b) => (
                    <tr key={b.id}>
                      <td>{b.name}</td>
                      <td>{b.online}</td>
                    </tr>
                  ))}
                  {conns && conns.downstream.length === 0 ? (
                    <tr>
                      <td colSpan={2} class="muted">
                        无
                      </td>
                    </tr>
                  ) : null}
                </tbody>
              </table>
            </div>
          </div>
        </div>
      </Window>
    </div>
  );
}
