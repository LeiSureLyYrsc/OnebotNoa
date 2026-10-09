import { useEffect, useRef, useState } from "preact/hooks";
import { api, FeedEvent, openEventStream } from "../api";
import { Window } from "../ui";

const MAX_ROWS = 400;

export function EventsPage() {
  const [events, setEvents] = useState<FeedEvent[]>([]);
  const [paused, setPaused] = useState(false);
  const [filter, setFilter] = useState("");
  const bufferRef = useRef<FeedEvent[]>([]);

  useEffect(() => {
    api.eventsRecent(100).then((r) => setEvents((r.events || []).slice().reverse()));
    const close = openEventStream((ev) => {
      if (paused) return;
      bufferRef.current = [ev, ...bufferRef.current].slice(0, MAX_ROWS);
      setEvents(bufferRef.current);
    });
    return close;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [paused]);

  const shown = filter
    ? events.filter(
        (e) =>
          (e.kind || "").includes(filter) ||
          (e.account || "").includes(filter) ||
          (e.bot || "").includes(filter) ||
          (e.summary || "").includes(filter),
      )
    : events;

  return (
    <Window title="实时事件">
      <div class="field-row" style={{ display: "flex", gap: "6px", alignItems: "center" }}>
        <button onClick={() => setPaused(!paused)}>{paused ? "▶ 继续" : "⏸ 暂停"}</button>
        <button onClick={() => setEvents([])}>清空</button>
        <input placeholder="过滤 (kind / 账号 / Bot / 摘要)" value={filter} onInput={(e) => setFilter((e.target as HTMLInputElement).value)} />
        <span class="muted">显示 {shown.length} 条</span>
      </div>
      <div class="feed">
        {shown.map((e, i) => (
          <div key={i}>
            <span class="t">{new Date(e.time).toLocaleTimeString()} </span>
            <span class={e.direction}>{e.direction}</span>{" "}
            <span>[{e.kind}]</span>{" "}
            {e.account ? <span>@{e.account} </span> : null}
            {e.bot ? <span>→{e.bot} </span> : null}
            <span>{e.summary}</span>
          </div>
        ))}
      </div>
    </Window>
  );
}
