import { useCallback, useEffect, useState } from "preact/hooks";
import { ApiError, createBot, deleteBot, getBot, listBots, rotateBotToken, updateBot, type Bot } from "../api";
import { Empty, JsonEditor, Modal, Window } from "../ui";
import { showSecret, toast } from "../state";

export function BotsPage() {
  const [bots, setBots] = useState<Bot[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [creating, setCreating] = useState(false);
  const [form, setForm] = useState({ name: "", note: "" });
  const [editing, setEditing] = useState<Bot | null>(null);
  const [policy, setPolicy] = useState("");
  const [rateLimit, setRateLimit] = useState("");

  const load = useCallback(async () => {
    try {
      const result = await listBots();
      setBots(result.bots);
      setError("");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
    const timer = window.setInterval(load, 5000);
    return () => window.clearInterval(timer);
  }, [load]);

  async function submitCreate(event: Event) {
    event.preventDefault();
    try {
      const result = await createBot(form.name.trim(), form.note.trim());
      showSecret("Bot token — " + result.bot.name, result.token, result.hint);
      setForm({ name: "", note: "" });
      setCreating(false);
      await load();
    } catch (err) {
      toast(err instanceof ApiError ? err.message : String(err), "error");
    }
  }

  function startEdit(bot: Bot) {
    setEditing(bot);
    setPolicy(JSON.stringify(bot.action_policy ?? {}, null, 2));
    setRateLimit(JSON.stringify(bot.rate_limit ?? {}, null, 2));
  }

  async function saveEdit(event: Event) {
    event.preventDefault();
    if (!editing) return;
    try {
      await updateBot(editing.id, {
        name: editing.name,
        note: editing.note,
        enabled: editing.enabled,
        action_policy: JSON.parse(policy || "{}"),
        rate_limit: JSON.parse(rateLimit || "{}"),
      });
      toast("已保存 " + editing.name);
      setEditing(null);
      await load();
    } catch (err) {
      toast(err instanceof ApiError ? err.message : String(err), "error");
    }
  }

  // showConnection hands the operator the full paste-ready URL, token included.
  // The relay keeps the token sealed in connect.json, so it can be shown again -
  // unlike a hash-only credential.
  async function showConnection(bot: Bot) {
    try {
      const result = await getBot(bot.id);
      const token = result.bot.token || "";
      if (!token) {
        toast("该 Bot 还没有 token，请先「轮换 token」");
        return;
      }
      const base = (result.bot.endpoints ?? [])[0] || "/onebot/v11/bot/ws";
      showSecret(
        "连接信息 — " + bot.name,
        base + "/" + token,
        "在地址末尾再加 /<self_id> 可得到只暴露一个账号的透明视图。token 也保存在 connect.json 中。",
      );
    } catch (err) {
      toast(err instanceof ApiError ? err.message : String(err), "error");
    }
  }

  async function rotate(bot: Bot) {
    if (!window.confirm("轮换 " + bot.name + " 的 token？旧 token 立即失效。")) return;
    try {
      const result = await rotateBotToken(bot.id);
      showSecret("Bot token — " + bot.name, result.token, result.hint);
      await load();
    } catch (err) {
      toast(err instanceof ApiError ? err.message : String(err), "error");
    }
  }

  async function remove(bot: Bot) {
    if (!window.confirm("删除 Bot " + bot.name + "？其绑定关系会一并删除。")) return;
    try {
      await deleteBot(bot.id);
      toast("已删除 " + bot.name);
      await load();
    } catch (err) {
      toast(err instanceof ApiError ? err.message : String(err), "error");
    }
  }

  return (
    <Window
      title="Bot 实例（下游应用） — OnebotNoa"
      tools={
        <>
          <button onClick={() => setCreating(true)}>新增 Bot</button>
          <button onClick={() => void load()}>刷新</button>
          <span class="hub-spacer" />
          <span class="hub-muted">连接地址：/onebot/v11/bot/ws/&lt;token&gt;</span>
        </>
      }
      footer={
        <>
          <p class="status-bar-field">{bots.length} 个 Bot</p>
          <p class="status-bar-field">{bots.reduce((sum, b) => sum + b.connections, 0)} 条在线连接</p>
        </>
      }
    >
      {error ? <p class="hub-error">{error}</p> : null}
      {loading ? (
        <Empty text="读取中…" />
      ) : bots.length === 0 ? (
        <Empty text="还没有 Bot。点击「新增 Bot」创建，并把返回的 token 填进你的框架。" />
      ) : (
        <table class="hub-table">
          <thead>
            <tr>
              <th>名称</th>
              <th>状态</th>
              <th>连接</th>
              <th>连接地址</th>
              <th>绑定账号</th>
              <th>备注</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {bots.map((bot) => (
              <tr key={bot.id} class={bot.enabled ? "" : "hub-row--disabled"}>
                <td>{bot.name}</td>
                <td>{bot.enabled ? "启用" : "停用"}</td>
                <td>{bot.connections}</td>
                <td class="hub-mono">
                  {(bot.endpoints ?? []).length === 0
                    ? "—"
                    : (bot.endpoints ?? []).map((url) => <div key={url}>{url}</div>)}
                </td>
                <td>{bot.binding_count}</td>
                <td class="hub-muted">{bot.note || "—"}</td>
                <td class="hub-row">
                  <button onClick={() => void showConnection(bot)}>连接信息</button>
                  <button onClick={() => startEdit(bot)}>编辑</button>
                  <button onClick={() => void rotate(bot)}>轮换 token</button>
                  <button
                    onClick={async () => {
                      await updateBot(bot.id, { enabled: !bot.enabled });
                      await load();
                    }}
                  >
                    {bot.enabled ? "停用" : "启用"}
                  </button>
                  <button onClick={() => void remove(bot)}>删除</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {creating ? (
        <Modal title="新增 Bot" onClose={() => setCreating(false)}>
          <form onSubmit={submitCreate}>
            <label class="hub-field">
              <span class="hub-field-label">名称</span>
              <input
                value={form.name}
                placeholder="例如 nonebot-main"
                onInput={(e) => setForm({ ...form, name: (e.target as HTMLInputElement).value })}
              />
            </label>
            <label class="hub-field">
              <span class="hub-field-label">备注</span>
              <input
                value={form.note}
                placeholder="可选"
                onInput={(e) => setForm({ ...form, note: (e.target as HTMLInputElement).value })}
              />
            </label>
            <p class="hub-muted">
              创建后立即显示 token；它是 connect.json 里的加密条目，之后仍可在「连接信息」查看。
            </p>
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
        <Modal title={"编辑 " + editing.name} onClose={() => setEditing(null)} wide>
          <form onSubmit={saveEdit}>
            <label class="hub-field">
              <span class="hub-field-label">名称</span>
              <input value={editing.name} onInput={(e) => setEditing({ ...editing, name: (e.target as HTMLInputElement).value })} />
            </label>
            <label class="hub-field">
              <span class="hub-field-label">备注</span>
              <input value={editing.note} onInput={(e) => setEditing({ ...editing, note: (e.target as HTMLInputElement).value })} />
            </label>
            <label class="hub-field">
              <span class="hub-field-label">启用</span>
              <input
                type="checkbox"
                checked={editing.enabled}
                onChange={(e) => setEditing({ ...editing, enabled: (e.target as HTMLInputElement).checked })}
              />
            </label>
            <div class="hub-field">
              <span class="hub-field-label">动作黑白名单（action_policy）</span>
              <JsonEditor value={policy} onChange={setPolicy} rows={5} placeholder='{"deny":["set_group_ban"]}' />
            </div>
            <div class="hub-field">
              <span class="hub-field-label">配额（rate_limit）</span>
              <JsonEditor value={rateLimit} onChange={setRateLimit} rows={4} placeholder='{"rate":5,"burst":10,"concurrency":4}' />
            </div>
            <p class="hub-muted">黑白名单与配额的执行随增量 I5 生效；当前先持久化。</p>
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