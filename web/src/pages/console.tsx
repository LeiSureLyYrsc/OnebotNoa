import { useCallback, useEffect, useState } from "preact/hooks";
import {
  ApiError,
  invokeConsole,
  listAccounts,
  listBots,
  type Account,
  type Bot,
  type ConsoleResult,
} from "../api";
import { JsonEditor, Window } from "../ui";
import { toast } from "../state";

interface HistoryEntry {
  at: string;
  action: string;
  selfID: string;
  status: "ok" | "failed";
  elapsed: number;
  response: string;
}

const PRESETS: Array<{ label: string; action: string; params: string }> = [
  { label: "get_status", action: "get_status", params: "{}" },
  { label: "get_login_info", action: "get_login_info", params: "{}" },
  { label: "get_version_info", action: "get_version_info", params: "{}" },
  {
    label: "send_private_msg",
    action: "send_private_msg",
    params: '{"user_id":10001,"message":"来自 OnebotNoa 调试台"}',
  },
  {
    label: "send_group_msg",
    action: "send_group_msg",
    params: '{"group_id":123456,"message":[{"type":"text","data":{"text":"hi"}}]}',
  },
  { label: "get_friend_list", action: "get_friend_list", params: "{}" },
  { label: "get_group_list", action: "get_group_list", params: "{}" },
];

export function ConsolePage() {
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [bots, setBots] = useState<Bot[]>([]);
  const [selfID, setSelfID] = useState("");
  const [action, setAction] = useState("get_status");
  const [params, setParams] = useState("{}");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<ConsoleResult | null>(null);
  const [history, setHistory] = useState<HistoryEntry[]>([]);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    try {
      const [accountList, botList] = await Promise.all([listAccounts(), listBots()]);
      setAccounts(accountList.accounts);
      setBots(botList.bots);
      setError("");
      setSelfID((current) => {
        if (current) return current;
        const online = accountList.accounts.find((a) => a.live_state === "online");
        return online?.self_id ?? accountList.accounts[0]?.self_id ?? "";
      });
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    }
  }, []);

  useEffect(() => {
    void load();
    const timer = window.setInterval(load, 5000);
    return () => window.clearInterval(timer);
  }, [load]);

  async function send(event?: Event) {
    if (event) event.preventDefault();
    if (busy) return;
    if (!selfID) {
      toast("请先选择一个账号", "error");
      return;
    }
    let parsedParams: unknown;
    try {
      parsedParams = params.trim() ? JSON.parse(params) : {};
    } catch {
      toast("params 不是合法的 JSON", "error");
      return;
    }

    setBusy(true);
    try {
      const value = await invokeConsole({
        self_id: selfID,
        action: action.trim(),
        params: parsedParams,
      });
      setResult(value);
      const responded = value.response as { status?: string } | undefined;
      const ok = !value.error && (!responded || responded.status === "ok" || responded.status === "async");
      setHistory((prev) =>
        [
          {
            at: new Date().toLocaleTimeString("zh-CN", { hour12: false }),
            action: action.trim(),
            selfID,
            status: (ok ? "ok" : "failed") as HistoryEntry["status"],
            elapsed: value.elapsed_ms,
            response: value.response ? JSON.stringify(value.response) : value.error ?? "",
          },
          ...prev,
        ].slice(0, 20),
      );
    } catch (err) {
      const message = err instanceof ApiError ? err.message : String(err);
      toast(message, "error");
      setResult({ sent: false, self_id: selfID, action, elapsed_ms: 0, error: message });
    } finally {
      setBusy(false);
    }
  }

  const selected = accounts.find((a) => a.self_id === selfID);

  return (
    <Window
      title="API 调试台 — OnebotNoa"
      tools={
        <>
          <button onClick={() => void load()}>刷新账号</button>
          <span class="hub-spacer" />
          <span class="hub-muted">调用会走完整链路：权限 → 限速 → echo 重写 → 上游</span>
        </>
      }
      footer={
        <>
          <p class="status-bar-field">{accounts.length} 个账号</p>
          <p class="status-bar-field">
            {selected ? "选中 " + selected.self_id + " · " + selected.live_state : "未选择账号"}
          </p>
        </>
      }
    >
      {error ? <p class="hub-error">{error}</p> : null}

      <form class="hub-console" onSubmit={(event) => void send(event)}>
        <div class="hub-console-row">
          <label class="hub-field">
            <span class="hub-field-label">账号</span>
            <select value={selfID} onChange={(e) => setSelfID((e.target as HTMLSelectElement).value)}>
              <option value="">请选择</option>
              {accounts.map((account) => (
                <option key={account.id} value={account.self_id}>
                  {account.self_id}
                  {account.name ? " (" + account.name + ")" : ""} — {account.live_state}
                </option>
              ))}
            </select>
          </label>
          <label class="hub-field">
            <span class="hub-field-label">动作</span>
            <input value={action} onInput={(e) => setAction((e.target as HTMLInputElement).value)} />
          </label>
        </div>

        <div class="hub-presets">
          {PRESETS.map((preset) => (
            <button
              key={preset.label}
              type="button"
              onClick={() => {
                setAction(preset.action);
                setParams(preset.params);
              }}
            >
              {preset.label}
            </button>
          ))}
        </div>

        <div class="hub-field">
          <span class="hub-field-label">params</span>
          <JsonEditor value={params} onChange={setParams} rows={6} />
        </div>

        <div class="hub-row hub-row--end">
          <button type="submit" disabled={busy}>
            {busy ? "等待上游响应…" : "发送"}
          </button>
        </div>
      </form>

      <div class="hub-split">
        <div class="hub-split-main">
          <h4>响应</h4>
          {result ? (
            <>
              <p class="hub-muted">
                {result.error ? "失败" : "成功"} · 耗时 {result.elapsed_ms} ms · self_id {result.self_id}
              </p>
              {result.error ? <p class="hub-error">{result.error}</p> : null}
              <pre class="hub-event-raw">{JSON.stringify(result.response ?? result.request ?? {}, null, 2)}</pre>
            </>
          ) : (
            <p class="hub-muted hub-empty">发送一次调用后，这里显示上游的原始响应。</p>
          )}
        </div>
        <div class="hub-split-side">
          <h4>最近调用</h4>
          {history.length === 0 ? (
            <p class="hub-muted hub-empty">还没有调用记录。</p>
          ) : (
            <table class="hub-table hub-table--compact">
              <tbody>
                {history.map((entry, index) => (
                  <tr key={index} class={entry.status === "ok" ? "" : "hub-row--disabled"}>
                    <td class="hub-mono">{entry.at}</td>
                    <td class="hub-mono">{entry.action}</td>
                    <td>{entry.elapsed} ms</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
          <p class="hub-muted">Bot 数量：{bots.length}</p>
        </div>
      </div>
    </Window>
  );
}
