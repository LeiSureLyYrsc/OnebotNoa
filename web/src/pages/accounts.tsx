import { useCallback, useEffect, useState } from "preact/hooks";
import {
  ApiError,
  approvePending,
  clearAccountToken,
  createAccount,
  deleteAccount,
  listAccounts,
  rejectPending,
  rotateAccountToken,
  stateLabel,
  updateAccount,
  type Account,
  type PendingAccount,
} from "../api";
import { Empty, Modal, StateDot, TimeCell, Window } from "../ui";
import { showSecret, toast } from "../state";

export function AccountsPage() {
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [pending, setPending] = useState<PendingAccount[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [creating, setCreating] = useState(false);
  const [form, setForm] = useState({ self_id: "", name: "" });

  const load = useCallback(async () => {
    try {
      const result = await listAccounts();
      setAccounts(result.accounts);
      setPending(result.pending ?? []);
      setError("");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
    const timer = window.setInterval(load, 4000);
    return () => window.clearInterval(timer);
  }, [load]);

  async function submitCreate(event: Event) {
    event.preventDefault();
    try {
      await createAccount(form.self_id.trim(), form.name.trim());
      toast("已创建账号 " + form.self_id.trim());
      setForm({ self_id: "", name: "" });
      setCreating(false);
      await load();
    } catch (err) {
      toast(err instanceof ApiError ? err.message : String(err), "error");
    }
  }

  async function toggleEnabled(account: Account) {
    try {
      await updateAccount(account.id, { enabled: !account.enabled });
      toast(account.enabled ? "已停用 " + account.self_id : "已启用 " + account.self_id);
      await load();
    } catch (err) {
      toast(err instanceof ApiError ? err.message : String(err), "error");
    }
  }

  async function issueToken(account: Account) {
    try {
      const result = await rotateAccountToken(account.id);
      showSecret("账号 token — " + account.self_id, result.token, result.hint);
      await load();
    } catch (err) {
      toast(err instanceof ApiError ? err.message : String(err), "error");
    }
  }

  async function dropToken(account: Account) {
    if (!window.confirm("吊销 " + account.self_id + " 的 token？该实例将无法再接入。")) return;
    try {
      await clearAccountToken(account.id);
      toast("已吊销 token");
      await load();
    } catch (err) {
      toast(err instanceof ApiError ? err.message : String(err), "error");
    }
  }

  async function remove(account: Account) {
    if (!window.confirm("删除账号 " + account.self_id + "？绑定关系会一并删除。")) return;
    try {
      await deleteAccount(account.id);
      toast("已删除 " + account.self_id);
      await load();
    } catch (err) {
      toast(err instanceof ApiError ? err.message : String(err), "error");
    }
  }

  async function decide(item: PendingAccount, approve: boolean) {
    try {
      if (approve) {
        const result = await approvePending(item.id);
        toast(result.hint);
      } else {
        await rejectPending(item.id);
        toast("已拒绝 " + item.self_id);
      }
      await load();
    } catch (err) {
      toast(err instanceof ApiError ? err.message : String(err), "error");
    }
  }

  return (
    <Window
      title="QQ 实例（账号） — OnebotNoa"
      tools={
        <>
          <button onClick={() => setCreating(true)}>新增账号</button>
          <button onClick={() => void load()}>刷新</button>
          <span class="hub-spacer" />
          <span class="hub-muted">单端点多实例：每个实例一个 token，中继按 token / X-Self-ID 识别</span>
        </>
      }
      footer={
        <>
          <p class="status-bar-field">{accounts.length} 个账号</p>
          <p class="status-bar-field">{pending.length} 个待接入</p>
          <p class="status-bar-field">自动刷新 4s</p>
        </>
      }
    >
      {error ? <p class="hub-error">{error}</p> : null}

      {pending.length > 0 ? (
        <>
          <h3>待接入审批</h3>
          <table class="hub-table">
            <thead>
              <tr>
                <th>self_id</th>
                <th>角色</th>
                <th>来源地址</th>
                <th>尝试次数</th>
                <th>最近尝试</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {pending.map((item) => (
                <tr key={item.id}>
                  <td class="hub-mono">{item.self_id}</td>
                  <td>{item.role}</td>
                  <td class="hub-mono">{item.remote_addr}</td>
                  <td>{item.attempts}</td>
                  <td>{item.last_seen}</td>
                  <td class="hub-row">
                    <button onClick={() => void decide(item, true)}>批准</button>
                    <button onClick={() => void decide(item, false)}>拒绝</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      ) : null}

      <h3>账号列表</h3>
      {loading ? (
        <Empty text="读取中…" />
      ) : accounts.length === 0 ? (
        <Empty text="还没有账号。让 NapCat/SnowLuma 连接上游端点，或点「新增账号」预先创建。" />
      ) : (
        <table class="hub-table">
          <thead>
            <tr>
              <th>self_id</th>
              <th>别名</th>
              <th>状态</th>
              <th>物理连接</th>
              <th>绑定</th>
              <th>token</th>
              <th>最近活跃</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {accounts.map((account) => (
              <tr key={account.id} class={account.enabled ? "" : "hub-row--disabled"}>
                <td class="hub-mono">{account.self_id}</td>
                <td>{account.name || account.nickname || "—"}</td>
                <td>
                  <StateDot state={account.live_state} text={stateLabel(account.live_state)} />
                </td>
                <td>
                  {account.peers.length === 0
                    ? "—"
                    : account.peers.map((peer) => (
                        <div key={peer.id}>
                          <span class="hub-mono">{peer.role}</span> · {peer.remote_addr}
                          <span class="hub-muted"> (排队 {peer.queued} / 丢弃 {peer.dropped})</span>
                        </div>
                      ))}
                </td>
                <td>{account.binding_count}</td>
                <td>{account.has_token ? "已绑定" : "未绑定"}</td>
                <td>
                  <TimeCell iso={account.last_seen_at} />
                </td>
                <td class="hub-row">
                  <button onClick={() => void issueToken(account)}>签发 token</button>
                  <button onClick={() => void dropToken(account)} disabled={!account.has_token}>
                    吊销
                  </button>
                  <button onClick={() => void toggleEnabled(account)}>{account.enabled ? "停用" : "启用"}</button>
                  <button onClick={() => void remove(account)}>删除</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {creating ? (
        <Modal title="新增账号" onClose={() => setCreating(false)}>
          <form onSubmit={submitCreate}>
            <label class="hub-field">
              <span class="hub-field-label">self_id</span>
              <input
                value={form.self_id}
                placeholder="例如 10001"
                onInput={(e) => setForm({ ...form, self_id: (e.target as HTMLInputElement).value })}
              />
            </label>
            <label class="hub-field">
              <span class="hub-field-label">别名</span>
              <input
                value={form.name}
                placeholder="可选，方便识别"
                onInput={(e) => setForm({ ...form, name: (e.target as HTMLInputElement).value })}
              />
            </label>
            <p class="hub-muted">
              通常不需要手动创建：实例第一次连接时会进入「待接入审批」，批准后自动建号。
              手动创建适合提前准备 token。
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
    </Window>
  );
}
