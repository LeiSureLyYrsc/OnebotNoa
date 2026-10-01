import { useCallback, useEffect, useState } from "preact/hooks";
import {
  ApiError,
  createEndpoint,
  deleteEndpoint,
  formatTime,
  listBots,
  listEndpoints,
  reconnectEndpoint,
  updateEndpoint,
  type Bot,
  type EndpointState,
} from "../api";
import { Empty, Modal, Window } from "../ui";
import { toast } from "../state";

interface FormState {
  name: string;
  kind: string;
  url: string;
  mode: string;
  token: string;
  account_hint: string;
  bot_id: number;
  fixed_self_id: string;
  enabled: boolean;
  min: string;
  max: string;
  jitter: string;
}

const EMPTY_FORM: FormState = {
  name: "",
  kind: "upstream_dial",
  url: "",
  mode: "universal",
  token: "",
  account_hint: "",
  bot_id: 0,
  fixed_self_id: "",
  enabled: true,
  min: "1s",
  max: "60s",
  jitter: "0.3",
};

export function EndpointsPage() {
  const [endpoints, setEndpoints] = useState<EndpointState[]>([]);
  const [bots, setBots] = useState<Bot[]>([]);
  const [note, setNote] = useState("");
  const [error, setError] = useState("");
  const [editing, setEditing] = useState<{ id: number; form: FormState } | null>(null);
  const [creating, setCreating] = useState(false);
  const [form, setForm] = useState<FormState>(EMPTY_FORM);

  const load = useCallback(async () => {
    try {
      const [list, botList] = await Promise.all([listEndpoints(), listBots()]);
      setEndpoints(list.endpoints ?? []);
      setNote(list.note ?? "");
      setBots(botList.bots);
      setError("");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    }
  }, []);

  useEffect(() => {
    void load();
    const timer = window.setInterval(load, 3000);
    return () => window.clearInterval(timer);
  }, [load]);

  function payloadFrom(state: FormState) {
    return {
      name: state.name.trim(),
      kind: state.kind,
      url: state.url.trim(),
      mode: state.mode,
      token: state.token,
      account_hint: state.account_hint.trim(),
      bot_id: state.kind === "downstream_dial" ? state.bot_id : null,
      fixed_self_id: state.fixed_self_id.trim(),
      enabled: state.enabled,
      reconnect: { min: state.min, max: state.max, jitter: Number(state.jitter) || 0 },
    };
  }

  async function submitCreate(event: Event) {
    event.preventDefault();
    try {
      await createEndpoint(payloadFrom(form));
      toast("已创建拨号目标 " + form.name);
      setCreating(false);
      setForm(EMPTY_FORM);
      await load();
    } catch (err) {
      toast(err instanceof ApiError ? err.message : String(err), "error");
    }
  }

  async function submitEdit(event: Event) {
    event.preventDefault();
    if (!editing) return;
    try {
      await updateEndpoint(editing.id, payloadFrom(editing.form));
      toast("已保存 " + editing.form.name);
      setEditing(null);
      await load();
    } catch (err) {
      toast(err instanceof ApiError ? err.message : String(err), "error");
    }
  }

  async function remove(item: EndpointState) {
    if (!item.db_id) return;
    if (!window.confirm("删除拨号目标 " + item.name + "？")) return;
    try {
      await deleteEndpoint(item.db_id);
      toast("已删除 " + item.name);
      await load();
    } catch (err) {
      toast(err instanceof ApiError ? err.message : String(err), "error");
    }
  }

  async function reconnect(item: EndpointState) {
    try {
      await reconnectEndpoint(item.name);
      toast("正在重连 " + item.name);
      await load();
    } catch (err) {
      toast(err instanceof ApiError ? err.message : String(err), "error");
    }
  }

  function startEdit(item: EndpointState) {
    setEditing({
      id: item.db_id ?? 0,
      form: {
        name: item.name,
        kind: item.kind,
        url: item.url,
        mode: item.mode || "universal",
        token: "",
        account_hint: item.account_hint ?? "",
        bot_id: item.bot_id ?? 0,
        fixed_self_id: item.fixed_self_id ?? "",
        enabled: item.enabled,
        min: "1s",
        max: "60s",
        jitter: "0.3",
      },
    });
  }

  return (
    <Window
      title="拨号目标（中继主动连接） — OnebotNoa"
      tools={
        <>
          <button onClick={() => setCreating(true)}>新增拨号目标</button>
          <button onClick={() => void load()}>刷新</button>
          <span class="hub-spacer" />
          <span class="hub-muted">上游拨号 = 中继连 QQ 实现端的正向 WS；下游拨号 = 中继连 Bot 的 WS 服务端</span>
        </>
      }
      footer={
        <>
          <p class="status-bar-field">{endpoints.length} 个目标</p>
          <p class="status-bar-field">{endpoints.filter((e) => e.state === "online").length} 个在线</p>
          <p class="status-bar-field">自动刷新 3s</p>
        </>
      }
    >
      {error ? <p class="hub-error">{error}</p> : null}
      {note ? <p class="hub-muted">{note}</p> : null}

      {endpoints.length === 0 ? (
        <Empty text="还没有拨号目标。用「新增」让中继主动去连 QQ 实现端或 Bot 服务端。" />
      ) : (
        <table class="hub-table">
          <thead>
            <tr>
              <th>名称</th>
              <th>方向</th>
              <th>地址</th>
              <th>来源</th>
              <th>状态</th>
              <th>账号 / Bot</th>
              <th>尝试</th>
              <th>最近错误</th>
              <th>下次重试</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {endpoints.map((item) => (
              <tr key={item.name} class={item.enabled ? "" : "hub-row--disabled"}>
                <td>{item.name}</td>
                <td>{item.kind === "upstream_dial" ? "上游（QQ）" : "下游（Bot）"}{item.kind === "upstream_dial" && item.mode === "split" ? " · split" : ""}</td>
                <td class="hub-mono">{item.url}</td>
                <td>{item.source === "connect.json" ? "connect.json" : item.source}</td>
                <td>
                  <span class={"hub-dot hub-dot--" + (item.state === "online" ? "online" : item.state === "backoff" ? "degraded" : "offline")}>
                    {stateLabel(item.state)}
                  </span>
                </td>
                <td class="hub-mono">{item.account_hint || item.bot_name || "—"}{item.fixed_self_id ? " / " + item.fixed_self_id : ""}</td>
                <td>{item.attempts}</td>
                <td class="hub-muted">{item.last_error || "—"}</td>
                <td class="hub-mono">{item.next_retry_at ? formatTime(item.next_retry_at) : "—"}</td>
                <td class="hub-row">
                  <button onClick={() => void reconnect(item)} disabled={!item.enabled}>
                    立即重连
                  </button>
                  <button onClick={() => startEdit(item)} disabled={!item.db_id}>
                    编辑
                  </button>
                  <button onClick={() => void remove(item)} disabled={!item.db_id}>
                    删除
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {creating ? (
        <Modal title="新增拨号目标" onClose={() => setCreating(false)} wide>
          <EndpointForm form={form} setForm={setForm} bots={bots} onSubmit={submitCreate} onCancel={() => setCreating(false)} submitLabel="创建" />
        </Modal>
      ) : null}

      {editing ? (
        <Modal title={"编辑 " + editing.form.name} onClose={() => setEditing(null)} wide>
          <EndpointForm
            form={editing.form}
            setForm={(next) => setEditing({ id: editing.id, form: next })}
            bots={bots}
            onSubmit={submitEdit}
            onCancel={() => setEditing(null)}
            submitLabel="保存"
            tokenHint="留空表示不修改已保存的 token"
          />
        </Modal>
      ) : null}
    </Window>
  );
}

function EndpointForm(props: {
  form: FormState;
  setForm: (next: FormState) => void;
  bots: Bot[];
  onSubmit: (event: Event) => void;
  onCancel: () => void;
  submitLabel: string;
  tokenHint?: string;
}) {
  const form = props.form;
  const set = (patch: Partial<FormState>) => props.setForm({ ...form, ...patch });
  const needsBot = form.kind === "downstream_dial";

  return (
    <form onSubmit={props.onSubmit}>
      <label class="hub-field">
        <span class="hub-field-label">名称</span>
        <input value={form.name} placeholder="例如 qq-10001 / bot-nonebot" onInput={(e) => set({ name: (e.target as HTMLInputElement).value })} />
      </label>
      <label class="hub-field">
        <span class="hub-field-label">方向</span>
        <select value={form.kind} onChange={(e) => set({ kind: (e.target as HTMLSelectElement).value })}>
          <option value="upstream_dial">上游拨号（连 QQ 实现端的正向 WS）</option>
          <option value="downstream_dial">下游拨号（连 Bot 的 WS 服务端）</option>
        </select>
      </label>
      <label class="hub-field">
        <span class="hub-field-label">地址</span>
        <input
          value={form.url}
          placeholder={needsBot ? "ws://bot-host:8080/onebot/v11/ws" : "ws://qq-host:6700/"}
          onInput={(e) => set({ url: (e.target as HTMLInputElement).value })}
        />
      </label>
      {!needsBot ? (
        <>
          <label class="hub-field">
            <span class="hub-field-label">模式</span>
            <select value={form.mode} onChange={(e) => set({ mode: (e.target as HTMLSelectElement).value })}>
              <option value="universal">universal（单连接，优先）</option>
              <option value="split">split（/api 与 /event 两条连接）</option>
            </select>
          </label>
          <label class="hub-field">
            <span class="hub-field-label">账号 self_id</span>
            <input value={form.account_hint} placeholder="例如 10001" onInput={(e) => set({ account_hint: (e.target as HTMLInputElement).value })} />
          </label>
        </>
      ) : (
        <>
          <label class="hub-field">
            <span class="hub-field-label">Bot</span>
            <select value={String(form.bot_id)} onChange={(e) => set({ bot_id: Number((e.target as HTMLSelectElement).value) })}>
              <option value="0">请选择</option>
              {props.bots.map((bot) => (
                <option key={bot.id} value={String(bot.id)}>
                  {bot.name}
                </option>
              ))}
            </select>
          </label>
          <label class="hub-field">
            <span class="hub-field-label">固定 self_id（透明视图，可空）</span>
            <input value={form.fixed_self_id} onInput={(e) => set({ fixed_self_id: (e.target as HTMLInputElement).value })} />
          </label>
        </>
      )}
      <label class="hub-field">
        <span class="hub-field-label">token</span>
        <input
          type="password"
          value={form.token}
          placeholder={props.tokenHint ?? "可选；中继拨号时作为 Bearer 与 access_token"}
          onInput={(e) => set({ token: (e.target as HTMLInputElement).value })}
        />
      </label>
      <label class="hub-field">
        <span class="hub-field-label">重连 min / max / jitter</span>
        <span class="hub-field-control">
          <input class="hub-input-sm" value={form.min} onInput={(e) => set({ min: (e.target as HTMLInputElement).value })} />
          <input class="hub-input-sm" value={form.max} onInput={(e) => set({ max: (e.target as HTMLInputElement).value })} />
          <input class="hub-input-sm" value={form.jitter} onInput={(e) => set({ jitter: (e.target as HTMLInputElement).value })} />
        </span>
      </label>
      <label class="hub-field">
        <span class="hub-field-label">启用</span>
        <input type="checkbox" checked={form.enabled} onChange={(e) => set({ enabled: (e.target as HTMLInputElement).checked })} />
      </label>
      <p class="hub-muted">
        指数退避 + 抖动；连接成功后重新从 min 开始。token 存在 connect.json（本地密钥加密），可在「连接文件」页查看或导出。
      </p>
      <div class="hub-row hub-row--end">
        <button type="submit">{props.submitLabel}</button>
        <button type="button" onClick={props.onCancel}>
          取消
        </button>
      </div>
    </form>
  );
}

function stateLabel(state: string): string {
  switch (state) {
    case "online":
      return "在线";
    case "connecting":
      return "连接中";
    case "backoff":
      return "退避重试";
    case "disabled":
      return "已停用";
    default:
      return state || "空闲";
  }
}