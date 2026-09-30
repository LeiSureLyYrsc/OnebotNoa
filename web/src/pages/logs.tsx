import { useCallback, useEffect, useState } from "preact/hooks";
import { ApiError, formatTime, listAudit, listLogs, type AuditEntry, type EventRecord } from "../api";
import { Empty, Window } from "../ui";

type Tab = "traffic" | "audit";

export function LogsPage() {
  const [tab, setTab] = useState<Tab>("traffic");
  const [logs, setLogs] = useState<EventRecord[]>([]);
  const [audit, setAudit] = useState<AuditEntry[]>([]);
  const [query, setQuery] = useState("");
  const [actionFilter, setActionFilter] = useState("");
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    try {
      const [logResult, auditResult] = await Promise.all([
        listLogs({ q: query }),
        listAudit({ action: actionFilter, q: query }),
      ]);
      setLogs(logResult.logs ?? []);
      setAudit(auditResult.entries ?? []);
      setError("");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    }
  }, [query, actionFilter]);

  useEffect(() => {
    void load();
    const timer = window.setInterval(load, 4000);
    return () => window.clearInterval(timer);
  }, [load]);

  return (
    <Window
      title="日志与审计 — OnebotNoa"
      tools={
        <>
          <button class={tab === "traffic" ? "hub-menubar-item--active" : ""} onClick={() => setTab("traffic")}>
            实时流量日志
          </button>
          <button class={tab === "audit" ? "hub-menubar-item--active" : ""} onClick={() => setTab("audit")}>
            审计日志
          </button>
          <span class="hub-spacer" />
          <input
            placeholder={tab === "audit" ? "搜索操作/目标/来源" : "关键字"}
            value={query}
            onInput={(e) => setQuery((e.target as HTMLInputElement).value)}
          />
          {tab === "audit" ? (
            <input
              placeholder="动作前缀，如 account."
              value={actionFilter}
              onInput={(e) => setActionFilter((e.target as HTMLInputElement).value)}
            />
          ) : null}
          <button onClick={() => void load()}>刷新</button>
        </>
      }
      footer={
        <>
          <p class="status-bar-field">{tab === "traffic" ? logs.length + " 条流量记录" : audit.length + " 条审计记录"}</p>
          <p class="status-bar-field">自动刷新 4s</p>
        </>
      }
    >
      {error ? <p class="hub-error">{error}</p> : null}

      {tab === "traffic" ? (
        logs.length === 0 ? (
          <Empty text="环形缓冲里还没有记录。让 QQ 实例发一条消息，或到「API 调试台」发一次调用。" />
        ) : (
          <div class="hub-logpane">
            <table class="hub-table hub-table--compact">
              <thead>
                <tr>
                  <th>时间</th>
                  <th>类型</th>
                  <th>self_id</th>
                  <th>Bot</th>
                  <th>动作 / 事件</th>
                  <th>说明</th>
                </tr>
              </thead>
              <tbody>
                {logs.map((record) => (
                  <tr key={record.seq}>
                    <td class="hub-mono">{formatTime(record.at)}</td>
                    <td>
                      <span class={"hub-pill hub-pill--" + record.kind}>{record.kind}</span>
                    </td>
                    <td class="hub-mono">{record.self_id || "—"}</td>
                    <td class="hub-mono">{record.bot || "—"}</td>
                    <td class="hub-mono">{record.action || record.post_type || "—"}</td>
                    <td>{record.note || (record.retcode !== undefined ? "retcode " + record.retcode : "")}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )
      ) : audit.length === 0 ? (
        <Empty text="还没有审计记录。" />
      ) : (
        <div class="hub-logpane">
          <table class="hub-table hub-table--compact">
            <thead>
              <tr>
                <th>时间</th>
                <th>操作者</th>
                <th>动作</th>
                <th>目标</th>
                <th>详情</th>
                <th>来源 IP</th>
              </tr>
            </thead>
            <tbody>
              {audit.map((entry) => (
                <tr key={entry.id}>
                  <td class="hub-mono">{formatTime(entry.at)}</td>
                  <td>{entry.actor}</td>
                  <td class="hub-mono">{entry.action}</td>
                  <td class="hub-mono">{entry.target || "—"}</td>
                  <td>{entry.detail || "—"}</td>
                  <td class="hub-mono">{entry.ip || "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Window>
  );
}
