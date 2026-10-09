import { useState } from "preact/hooks";
import { api, Account } from "../api";
import { useInterval, Window } from "../ui";

const PRESETS: { label: string; action: string; params: string }[] = [
  { label: "get_login_info", action: "get_login_info", params: "{}" },
  { label: "get_status", action: "get_status", params: "{}" },
  { label: "get_version_info", action: "get_version_info", params: "{}" },
  { label: "send_group_msg", action: "send_group_msg", params: '{"group_id":123456,"message":"hello"}' },
];

export function ConsolePage() {
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [selfId, setSelfId] = useState("");
  const [action, setAction] = useState("get_login_info");
  const [params, setParams] = useState("{}");
  const [response, setResponse] = useState("");
  const [error, setError] = useState("");

  useInterval(() => {
    api.accounts().then((r) => setAccounts(r.accounts || [])).catch(() => undefined);
  }, 6000);

  const invoke = () => {
    if (!selfId) {
      setError("请选择账号");
      return;
    }
    let parsed: unknown = {};
    try {
      parsed = JSON.parse(params);
    } catch {
      setError("params 不是合法 JSON");
      return;
    }
    setError("");
    api
      .invoke(selfId, action, parsed)
      .then((r) => setResponse(JSON.stringify(r, null, 2)))
      .catch((e) => setResponse(String(e.message || e)));
  };

  return (
    <Window title="API 调试台">
      <div class="field-row" style={{ display: "flex", gap: "8px", flexWrap: "wrap", alignItems: "flex-end" }}>
        <div>
          <label>账号</label>
          <br />
          <select value={selfId} onChange={(e) => setSelfId((e.target as HTMLSelectElement).value)}>
            <option value="">选择账号</option>
            {accounts.map((a) => (
              <option key={a.id} value={a.self_id}>
                {a.self_id} ({a.status})
              </option>
            ))}
          </select>
        </div>
        <div>
          <label>预设</label>
          <br />
          <select
            onChange={(e) => {
              const p = PRESETS[Number((e.target as HTMLSelectElement).value)];
              if (p) {
                setAction(p.action);
                setParams(p.params);
              }
            }}
          >
            <option value="">自定义</option>
            {PRESETS.map((p, i) => (
              <option key={i} value={i}>
                {p.label}
              </option>
            ))}
          </select>
        </div>
        <div>
          <label>action</label>
          <br />
          <input value={action} onInput={(e) => setAction((e.target as HTMLInputElement).value)} />
        </div>
        <button onClick={invoke}>发送</button>
      </div>
      <div class="field-row">
        <label>params (JSON)</label>
        <br />
        <textarea
          style={{ width: "100%", height: "90px", fontFamily: "Consolas, monospace" }}
          value={params}
          onInput={(e) => setParams((e.target as HTMLTextAreaElement).value)}
        />
      </div>
      {error ? <p style={{ color: "#a00" }}>{error}</p> : null}
      <label>响应</label>
      <pre class="raw">{response || "（无）"}</pre>
    </Window>
  );
}
