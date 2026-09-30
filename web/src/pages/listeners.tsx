import { useCallback, useEffect, useState } from "preact/hooks";
import { ApiError, createListener, deleteListener, listListeners, type Listener } from "../api";
import { Empty, Modal, Window } from "../ui";
import { toast } from "../state";

interface Shared {
  upstream_path?: string;
  downstream_path?: string;
  listen?: string;
}

export function ListenersPage() {
  const [listeners, setListeners] = useState<Listener[]>([]);
  const [shared, setShared] = useState<Shared>({});
  const [note, setNote] = useState("");
  const [error, setError] = useState("");
  const [creating, setCreating] = useState(false);
  const [form, setForm] = useState({ name: "", kind: "upstream_listen", bind_addr: "", path: "/onebot/v11/ws", fixed_self_id: "" });

  const load = useCallback(async () => {
    try {
      const result = await listListeners();
      setListeners(result.listeners ?? []);
      setShared(result.shared ?? {});
      setNote(result.runtime_note ?? "");
      setError("");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    }
  }, []);

  useEffect(() => {
    void load();
    const timer = window.setInterval(load, 4000);
    return () => window.clearInterval(timer);
  }, [load]);

  async function submit(event: Event) {
    event.preventDefault();
    try {
      await createListener({
        name: form.name.trim(),
        kind: form.kind,
        bind_addr: form.bind_addr.trim(),
        path: form.path.trim(),
        fixed_self_id: form.fixed_self_id.trim(),
      });
      toast("已保存监听端点");
      setCreating(false);
      await load();
    } catch (err) {
      toast(err instanceof ApiError ? err.message : String(err), "error");
    }
  }

  async function remove(listener: Listener) {
    if (!window.confirm("删除监听端点 " + listener.name + "？")) return;
    try {
      await deleteListener(listener.id);
      await load();
    } catch (err) {
      toast(err instanceof ApiError ? err.message : String(err), "error");
    }
  }

  return (
    <Window
      title="监听端点 — OnebotNoa"
      tools={
        <>
          <button onClick={() => setCreating(true)}>新增独立监听</button>
          <button onClick={() => void load()}>刷新</button>
          <span class="hub-spacer" />
          <span class="hub-muted">共享端点 1 个端口即可服务全部实例；独立端口用于物理隔离</span>
        </>
      }
      footer={
        <>
          <p class="status-bar-field">上游 {shared.upstream_path ?? "—"}</p>
          <p class="status-bar-field">下游 {shared.downstream_path ?? "—"}</p>
          <p class="status-bar-field">管理面 {shared.listen ?? "—"}</p>
        </>
      }
    >
      {error ? <p class="hub-error">{error}</p> : null}
      {note ? <p class="hub-warn">{note}</p> : null}

      <h3>共享端点（当前生效）</h3>
      <table class="hub-kv">
        <tbody>
          <tr>
            <th>QQ 侧</th>
            <td class="hub-mono">{shared.upstream_path ?? "—"}</td>
          </tr>
          <tr>
            <th>Bot 侧</th>
            <td class="hub-mono">{shared.downstream_path ?? "—"}</td>
          </tr>
        </tbody>
      </table>

      <h3>独立监听</h3>
      {listeners.length === 0 ? (
        <Empty text="暂无独立监听端点。" />
      ) : (
        <table class="hub-table">
          <thead>
            <tr>
              <th>名称</th>
              <th>类型</th>
              <th>地址</th>
              <th>路径</th>
              <th>固定 self_id</th>
              <th>运行时</th>
              <th>对外地址</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {listeners.map((listener) => (
              <tr key={listener.id}>
                <td>{listener.name}</td>
                <td>{listener.kind === "upstream_listen" ? "上游（QQ 接入）" : "下游（Bot 接入）"}</td>
                <td class="hub-mono">{listener.bind_addr}</td>
                <td class="hub-mono">{listener.path}</td>
                <td class="hub-mono">{listener.fixed_self_id || "—"}</td>
                <td>
                  <span class={"hub-dot hub-dot--" + runtimeDot(listener.runtime)} title={listener.last_error || ""}>
                    {runtimeLabel(listener.runtime)}
                  </span>
                  {listener.last_error ? <div class="hub-error hub-error--inline">{listener.last_error}</div> : null}
                </td>
                <td class="hub-mono">{listener.url || "—"}</td>
                <td class="hub-row">
                  <button onClick={() => void remove(listener)}>删除</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {creating ? (
        <Modal title="新增独立监听" onClose={() => setCreating(false)}>
          <form onSubmit={submit}>
            <label class="hub-field">
              <span class="hub-field-label">名称</span>
              <input value={form.name} placeholder="例如 qq-10001" onInput={(e) => setForm({ ...form, name: (e.target as HTMLInputElement).value })} />
            </label>
            <label class="hub-field">
              <span class="hub-field-label">类型</span>
              <select value={form.kind} onChange={(e) => setForm({ ...form, kind: (e.target as HTMLSelectElement).value })}>
                <option value="upstream_listen">上游（QQ 实例接入）</option>
                <option value="downstream_listen">下游（Bot 接入）</option>
              </select>
            </label>
            <label class="hub-field">
              <span class="hub-field-label">监听地址</span>
              <input value={form.bind_addr} placeholder="0.0.0.0:6710" onInput={(e) => setForm({ ...form, bind_addr: (e.target as HTMLInputElement).value })} />
            </label>
            <label class="hub-field">
              <span class="hub-field-label">路径</span>
              <input value={form.path} onInput={(e) => setForm({ ...form, path: (e.target as HTMLInputElement).value })} />
            </label>
            <label class="hub-field">
              <span class="hub-field-label">固定 self_id（下游透明视图留空即可）</span>
              <input value={form.fixed_self_id} onInput={(e) => setForm({ ...form, fixed_self_id: (e.target as HTMLInputElement).value })} />
            </label>
            <div class="hub-row hub-row--end">
              <button type="submit">保存</button>
              <button type="button" onClick={() => setCreating(false)}>
                取消
              </button>
            </div>
          </form>
        </Modal>
      ) : null}
    </Window>
  );
}

function runtimeLabel(state?: string): string {
  switch (state) {
    case "listening":
      return "监听中";
    case "error":
      return "启动失败";
    case "stopped":
      return "已停止";
    case "disabled":
      return "已停用";
    case "pending":
      return "待生效";
    default:
      return state || "未知";
  }
}

function runtimeDot(state?: string): string {
  switch (state) {
    case "listening":
      return "online";
    case "error":
      return "offline";
    case "disabled":
    case "stopped":
    case "pending":
      return "degraded";
    default:
      return "offline";
  }
}
