import { useCallback, useEffect, useState } from "preact/hooks";
import {
  ApiError,
  createBinding,
  deleteBinding,
  listAccounts,
  listBindings,
  listBots,
  updateBinding,
  type Account,
  type Binding,
  type Bot,
} from "../api";
import { Empty, JsonEditor, Modal, Window } from "../ui";
import { toast } from "../state";

export function BindingsPage() {
  const [bindings, setBindings] = useState<Binding[]>([]);
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [bots, setBots] = useState<Bot[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [creating, setCreating] = useState(false);
  const [form, setForm] = useState({ bot_id: 0, account_id: 0, is_default: false, scope: "" });
  const [editing, setEditing] = useState<Binding | null>(null);
  const [editScope, setEditScope] = useState("");

  const load = useCallback(async () => {
    try {
      const [b, a, t] = await Promise.all([listBindings(), listAccounts(), listBots()]);
      setBindings(b.bindings);
      setAccounts(a.accounts);
      setBots(t.bots);
      setError("");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  async function submitCreate(event: Event) {
    event.preventDefault();
    if (!form.bot_id || !form.account_id) {
      toast("请选择 Bot 与账号", "error");
      return;
    }
    try {
      await createBinding({
        bot_id: form.bot_id,
        account_id: form.account_id,
        is_default: form.is_default,
        scope: form.scope.trim() ? JSON.parse(form.scope) : {},
      });
      toast("绑定已创建");
      setCreating(false);
      setForm({ bot_id: 0, account_id: 0, is_default: false, scope: "" });
      await load();
    } catch (err) {
      toast(err instanceof ApiError ? err.message : String(err), "error");
    }
  }

  async function patch(binding: Binding, changes: Record<string, unknown>) {
    try {
      await updateBinding(binding.id, changes);
      await load();
    } catch (err) {
      toast(err instanceof ApiError ? err.message : String(err), "error");
    }
  }

  async function remove(binding: Binding) {
    if (!window.confirm("删除 " + binding.bot_name + " → " + binding.account_self_id + " 的绑定？")) return;
    try {
      await deleteBinding(binding.id);
      toast("已删除绑定");
      await load();
    } catch (err) {
      toast(err instanceof ApiError ? err.message : String(err), "error");
    }
  }

  function startEdit(binding: Binding) {
    setEditing(binding);
    setEditScope(JSON.stringify(binding.scope ?? {}, null, 2));
  }

  async function saveEdit(event: Event) {
    event.preventDefault();
    if (!editing) return;
    try {
      await updateBinding(editing.id, {
        priority: editing.priority,
        is_default: editing.is_default,
        enabled: editing.enabled,
        scope: JSON.parse(editScope || "{}"),
      });
      toast("已保存");
      setEditing(null);
      await load();
    } catch (err) {
      toast(err instanceof ApiError ? err.message : String(err), "error");
    }
  }

  return (
    <Window
      title="绑定关系 — OnebotNoa"
      tools={
        <>
          <button onClick={() => setCreating(true)} disabled={bots.length === 0 || accounts.length === 0}>
            新增绑定
          </button>
          <button onClick={() => void load()}>刷新</button>
          <span class="hub-spacer" />
          <span class="hub-muted">一个账号可以服务多个 Bot；事件按绑定的过滤范围多播</span>
        </>
      }
      footer={
        <>
          <p class="status-bar-field">{bindings.length} 条绑定</p>
          <p class="status-bar-field">
            {bots.length} Bot / {accounts.length} 账号
          </p>
        </>
      }
    >
      {error ? <p class="hub-error">{error}</p> : null}
      {loading ? (
        <Empty text="读取中…" />
      ) : bindings.length === 0 ? (
        <Empty text="还没有绑定关系。先创建 Bot 与账号，再把它们绑起来。" />
      ) : (
        <table class="hub-table">
          <thead>
            <tr>
              <th>Bot</th>
              <th>账号</th>
              <th>默认</th>
              <th>优先级</th>
              <th>状态</th>
              <th>过滤范围</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {bindings.map((binding) => (
              <tr key={binding.id} class={binding.enabled ? "" : "hub-row--disabled"}>
                <td>{binding.bot_name}</td>
                <td class="hub-mono">{binding.account_self_id}</td>
                <td>
                  <input
                    type="radio"
                    name="default-binding"
                    checked={binding.is_default}
                    onChange={() => void patch(binding, { is_default: true })}
                  />
                </td>
                <td>{binding.priority}</td>
                <td>{binding.enabled ? "启用" : "停用"}</td>
                <td class="hub-mono hub-scope">{summarizeScope(binding)}</td>
                <td class="hub-row">
                  <button onClick={() => startEdit(binding)}>编辑</button>
                  <button onClick={() => void patch(binding, { enabled: !binding.enabled })}>{binding.enabled ? "停用" : "启用"}</button>
                  <button onClick={() => void remove(binding)}>删除</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {creating ? (
        <Modal title="新增绑定" onClose={() => setCreating(false)}>
          <form onSubmit={submitCreate}>
            <label class="hub-field">
              <span class="hub-field-label">Bot</span>
              <select value={String(form.bot_id)} onChange={(e) => setForm({ ...form, bot_id: Number((e.target as HTMLSelectElement).value) })}>
                <option value="0">请选择</option>
                {bots.map((bot) => (
                  <option key={bot.id} value={String(bot.id)}>
                    {bot.name}
                  </option>
                ))}
              </select>
            </label>
            <label class="hub-field">
              <span class="hub-field-label">账号</span>
              <select
                value={String(form.account_id)}
                onChange={(e) => setForm({ ...form, account_id: Number((e.target as HTMLSelectElement).value) })}
              >
                <option value="0">请选择</option>
                {accounts.map((account) => (
                  <option key={account.id} value={String(account.id)}>
                    {account.self_id} {account.name ? "(" + account.name + ")" : ""}
                  </option>
                ))}
              </select>
            </label>
            <label class="hub-field">
              <span class="hub-field-label">设为该 Bot 的默认账号</span>
              <input type="checkbox" checked={form.is_default} onChange={(e) => setForm({ ...form, is_default: (e.target as HTMLInputElement).checked })} />
            </label>
            <div class="hub-field">
              <span class="hub-field-label">过滤范围（scope）</span>
              <JsonEditor value={form.scope} onChange={(next) => setForm({ ...form, scope: next })} rows={5} placeholder='{"post_types":["message"],"exclude_self":true}' />
            </div>
            <div class="hub-row hub-row--end">
              <button type="submit">创建</button>
              <button type="button" onClick={() => setCreating(false)}>
                取消
              </button>
            </div>
          </form>
        </Modal>
      ) : null}

      {editing ? (
        <Modal title={"编辑绑定：" + editing.bot_name + " → " + editing.account_self_id} onClose={() => setEditing(null)} wide>
          <form onSubmit={saveEdit}>
            <label class="hub-field">
              <span class="hub-field-label">优先级</span>
              <input
                type="number"
                value={String(editing.priority)}
                onInput={(e) => setEditing({ ...editing, priority: Number((e.target as HTMLInputElement).value) })}
              />
            </label>
            <label class="hub-field">
              <span class="hub-field-label">默认账号</span>
              <input type="checkbox" checked={editing.is_default} onChange={(e) => setEditing({ ...editing, is_default: (e.target as HTMLInputElement).checked })} />
            </label>
            <label class="hub-field">
              <span class="hub-field-label">启用</span>
              <input type="checkbox" checked={editing.enabled} onChange={(e) => setEditing({ ...editing, enabled: (e.target as HTMLInputElement).checked })} />
            </label>
            <div class="hub-field">
              <span class="hub-field-label">过滤范围（scope）</span>
              <JsonEditor value={editScope} onChange={setEditScope} rows={8} />
            </div>
            <p class="hub-muted">
              可用字段：post_types / include_groups / exclude_groups / include_users / exclude_users / exclude_self / meta_events
            </p>
            <div class="hub-row hub-row--end">
              <button type="submit">保存</button>
              <button type="button" onClick={() => setEditing(null)}>
                取消
              </button>
            </div>
          </form>
        </Modal>
      ) : null}
    </Window>
  );
}

function summarizeScope(binding: Binding): string {
  const scope = binding.scope ?? {};
  const parts: string[] = [];
  if (scope.post_types?.length) parts.push("类型:" + scope.post_types.join("/"));
  if (scope.include_groups?.length) parts.push("群:" + scope.include_groups.length);
  if (scope.exclude_groups?.length) parts.push("排除群:" + scope.exclude_groups.length);
  if (scope.include_users?.length) parts.push("用户:" + scope.include_users.length);
  if (scope.exclude_users?.length) parts.push("排除用户:" + scope.exclude_users.length);
  if (scope.exclude_self) parts.push("排除自聊");
  if (scope.meta_events) parts.push("元事件:" + scope.meta_events);
  return parts.length ? parts.join(" · ") : "全部";
}
