import { useCallback, useEffect, useRef, useState } from "preact/hooks";
import { ApiError, formatClock, formatBytes, recentEvents, type EventRecord } from "../api";
import { Empty, Window } from "../ui";
import { toast } from "../state";

const MAX_ROWS = 500;

interface Filters {
  self_id: string;
  bot: string;
  kind: string;
  post_type: string;
  group_id: string;
  q: string;
}

const EMPTY_FILTERS: Filters = { self_id: "", bot: "", kind: "", post_type: "", group_id: "", q: "" };

export function EventsPage() {
  const [filters, setFilters] = useState<Filters>(EMPTY_FILTERS);
  const [applied, setApplied] = useState<Filters>(EMPTY_FILTERS);
  const [rows, setRows] = useState<EventRecord[]>([]);
  const [paused, setPaused] = useState(false);
  const [connected, setConnected] = useState(false);
  const [selected, setSelected] = useState<EventRecord | null>(null);
  const [error, setError] = useState("");
  const sourceRef = useRef<EventSource | null>(null);
  const pausedRef = useRef(false);

  pausedRef.current = paused;

  const query = useCallback((f: Filters) => {
    const params = new URLSearchParams();
    for (const [key, value] of Object.entries(f)) {
      if (value.trim()) params.set(key, value.trim());
    }
    const text = params.toString();
    return text ? "?" + text : "";
  }, []);

  // (Re)connect whenever the applied filters change.
  useEffect(() => {
    setError("");
    void recentEvents(query(applied))
      .then((result) => setRows(result.events.slice(-MAX_ROWS)))
      .catch((err: unknown) => setError(err instanceof ApiError ? err.message : String(err)));

    const source = new EventSource("/api/v1/events/stream" + query(applied));
    sourceRef.current = source;

    source.onopen = () => setConnected(true);
    source.onerror = () => setConnected(false);
    source.onmessage = (event: MessageEvent<string>) => {
      if (pausedRef.current) return;
      append(event.data);
    };
    for (const kind of ["account", "upstream", "action", "response", "policy", "timeout"]) {
      source.addEventListener(kind, (event) => {
        if (pausedRef.current) return;
        append((event as MessageEvent<string>).data);
      });
    }

    function append(data: string) {
      try {
        const record = JSON.parse(data) as EventRecord;
        setRows((prev) => {
          const next = prev.length >= MAX_ROWS ? prev.slice(prev.length - MAX_ROWS + 1) : prev.slice();
          next.push(record);
          return next;
        });
      } catch {
        /* ignore malformed frames */
      }
    }

    return () => {
      source.close();
      sourceRef.current = null;
      setConnected(false);
    };
  }, [applied, query]);

  function describe(record: EventRecord): string {
    switch (record.kind) {
      case "account":
        return record.note ?? "";
      case "upstream":
        return [record.post_type, record.group_id ? "群 " + record.group_id : "", record.user_id ? "用户 " + record.user_id : ""]
          .filter(Boolean)
          .join(" · ");
      case "action":
      case "response":
      case "policy":
        return [record.action, record.note ?? "", record.retcode !== undefined ? "retcode " + record.retcode : ""].filter(Boolean).join(" · ");
      default:
        return "";
    }
  }

  return (
    <Window
      title="实时事件流 — OnebotNoa"
      tools={
        <>
          <button onClick={() => setPaused((p) => !p)}>{paused ? "继续" : "暂停"}</button>
          <button onClick={() => setRows([])}>清空</button>
          <button onClick={() => void recentEvents(query(applied)).then((r) => setRows(r.events.slice(-MAX_ROWS))).catch(() => toast("回填失败", "error"))}>
            回填最近记录
          </button>
          <span class="hub-spacer" />
          <span class={connected ? "hub-dot hub-dot--online" : "hub-dot hub-dot--offline"}>{connected ? "已连接" : "已断开"}</span>
          <span class="hub-muted">最多保留 {MAX_ROWS} 条</span>
        </>
      }
      footer={
        <>
          <p class="status-bar-field">{rows.length} 条记录</p>
          <p class="status-bar-field">{paused ? "已暂停" : "实时"}</p>
          <p class="status-bar-field">SSE /api/v1/events/stream</p>
        </>
      }
    >
      {error ? <p class="hub-error">{error}</p> : null}

      <form
        class="hub-filters"
        onSubmit={(event) => {
          event.preventDefault();
          setApplied(filters);
        }}
      >
        <input placeholder="self_id" value={filters.self_id} onInput={(e) => setFilters({ ...filters, self_id: (e.target as HTMLInputElement).value })} />
        <input placeholder="bot 名称" value={filters.bot} onInput={(e) => setFilters({ ...filters, bot: (e.target as HTMLInputElement).value })} />
        <select value={filters.kind} onChange={(e) => setFilters({ ...filters, kind: (e.target as HTMLSelectElement).value })}>
          <option value="">全部类型</option>
          <option value="account">账号/连接</option>
          <option value="upstream">上游事件</option>
          <option value="action">下行动作</option>
          <option value="response">上行响应</option>
          <option value="policy">策略拒绝</option>
          <option value="timeout">上游超时</option>
        </select>
        <input placeholder="post_type" value={filters.post_type} onInput={(e) => setFilters({ ...filters, post_type: (e.target as HTMLInputElement).value })} />
        <input placeholder="group_id" value={filters.group_id} onInput={(e) => setFilters({ ...filters, group_id: (e.target as HTMLInputElement).value })} />
        <input placeholder="关键字" value={filters.q} onInput={(e) => setFilters({ ...filters, q: (e.target as HTMLInputElement).value })} />
        <button type="submit">应用</button>
        <button
          type="button"
          onClick={() => {
            setFilters(EMPTY_FILTERS);
            setApplied(EMPTY_FILTERS);
          }}
        >
          重置
        </button>
      </form>

      <div class="hub-split">
        <div class="hub-split-main">
          {rows.length === 0 ? (
            <Empty text="还没有事件。让 QQ 实例发一条消息，或点「回填最近记录」。" />
          ) : (
            <table class="hub-table hub-table--compact">
              <thead>
                <tr>
                  <th>时间</th>
                  <th>类型</th>
                  <th>来源</th>
                  <th>内容</th>
                  <th>大小</th>
                </tr>
              </thead>
              <tbody>
                {rows.slice().reverse().map((record) => (
                  <tr key={record.seq} onClick={() => setSelected(record)} class={selected?.seq === record.seq ? "hub-row--selected" : ""}>
                    <td class="hub-mono">{formatClock(record.at)}</td>
                    <td>
                      <span class={"hub-pill hub-pill--" + record.kind}>{record.kind}</span>
                    </td>
                    <td class="hub-mono">{record.self_id || record.bot || "—"}</td>
                    <td>{describe(record)}</td>
                    <td>{formatBytes(record.bytes)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
        <div class="hub-split-side">
          <h4>原始帧</h4>
          {selected ? (
            <>
              <div class="hub-muted">
                #{selected.seq} · {selected.kind}
                {selected.truncated ? " · 已截断" : ""}
              </div>
              <pre class="hub-event-raw">{JSON.stringify(selected.raw ?? {}, null, 2)}</pre>
            </>
          ) : (
            <Empty text="点击左侧任意一行查看原始帧" />
          )}
        </div>
      </div>
    </Window>
  );
}
