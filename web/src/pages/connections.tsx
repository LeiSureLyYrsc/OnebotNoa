import { useCallback, useEffect, useState } from "preact/hooks";
import {
  ApiError,
  formatTime,
  getConnections,
  putConnections,
  type ConnectDocument,
} from "../api";
import { Empty, Modal, Window } from "../ui";
import { toast } from "../state";

// ConnectionsPage shows connect.json itself: the generated document that holds
// every account, Bot, connection and grant. It is the page an operator uses to
// back the file up, seed another deployment from it, or just confirm what the
// hub actually loaded.
export function ConnectionsPage() {
  const [doc, setDoc] = useState<ConnectDocument | null>(null);
  const [path, setPath] = useState("");
  const [note, setNote] = useState("");
  const [error, setError] = useState("");
  const [editing, setEditing] = useState<string | null>(null);
  const [draft, setDraft] = useState("");

  const load = useCallback(async () => {
    try {
      const result = await getConnections();
      setDoc(result.connect);
      setPath(result.path ?? "");
      setNote(result.note ?? "");
      setError("");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  function startEditing() {
    if (!doc) return;
    setDraft(JSON.stringify(doc, null, 2));
    setEditing("document");
  }

  // Import replaces the whole document, so it goes through one confirmation that
  // names the consequence instead of a generic "are you sure".
  async function submitImport(event: Event) {
    event.preventDefault();
    let parsed: ConnectDocument;
    try {
      parsed = JSON.parse(draft) as ConnectDocument;
    } catch (err) {
      toast("不是合法的 JSON：" + String(err), "error");
      return;
    }
    if (!window.confirm("导入会替换当前的 connect.json（原文件会保留为 .bak）。继续？")) return;
    try {
      await putConnections(parsed);
      toast("已导入 connect.json，连接正在按新内容重建");
      setEditing(null);
      await load();
    } catch (err) {
      toast(err instanceof ApiError ? err.message : String(err), "error");
    }
  }

  async function download() {
    if (!doc) return;
    try {
      const blob = new Blob([JSON.stringify(doc, null, 2)], { type: "application/json" });
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = "connect.json";
      link.click();
      URL.revokeObjectURL(url);
      toast("已导出 connect.json");
    } catch (err) {
      toast(String(err), "error");
    }
  }

  const counts = doc
    ? {
        accounts: doc.accounts?.length ?? 0,
        bots: doc.bots?.length ?? 0,
        connections: doc.connections?.length ?? 0,
        bindings: doc.bindings?.length ?? 0,
      }
    : { accounts: 0, bots: 0, connections: 0, bindings: 0 };

  return (
    <Window
      title="连接文件 connect.json — OnebotNoa"
      tools={
        <>
          <button onClick={() => void load()}>刷新</button>
          <button onClick={startEditing} disabled={!doc}>
            编辑 / 导入
          </button>
          <button onClick={() => void download()} disabled={!doc}>
            导出
          </button>
          <span class="hub-spacer" />
          <span class="hub-muted">进程级静态配置在 config.yaml；连接与密钥都在这里</span>
        </>
      }
      footer={
        <>
          <p class="status-bar-field">账号 {counts.accounts}</p>
          <p class="status-bar-field">Bot {counts.bots}</p>
          <p class="status-bar-field">连接 {counts.connections}</p>
          <p class="status-bar-field">绑定 {counts.bindings}</p>
        </>
      }
    >
      {error ? <p class="hub-error">{error}</p> : null}
      {path ? (
        <table class="hub-kv">
          <tbody>
            <tr>
              <th>文件</th>
              <td class="hub-mono">{path}</td>
            </tr>
            <tr>
              <th>密钥</th>
              <td class="hub-mono">{path}.key</td>
            </tr>
            <tr>
              <th>更新时间</th>
              <td>{doc?.updated_at ? formatTime(doc.updated_at) : "—"}</td>
            </tr>
          </tbody>
        </table>
      ) : null}
      {note ? <p class="hub-muted">{note}</p> : null}

      <h3>账号</h3>
      {counts.accounts === 0 ? (
        <Empty text="还没有账号。" />
      ) : (
        <table class="hub-table">
          <thead>
            <tr>
              <th>self_id</th>
              <th>别名</th>
              <th>启用</th>
              <th>凭据</th>
              <th>来源</th>
            </tr>
          </thead>
          <tbody>
            {doc?.accounts?.map((account) => (
              <tr key={account.id} class={account.enabled ? "" : "hub-row--disabled"}>
                <td class="hub-mono">{account.self_id}</td>
                <td>{account.name || "—"}</td>
                <td>{account.enabled ? "是" : "否"}</td>
                <td class="hub-mono">
                  {account.grant?.token_hint || "—"}
                  {account.grant?.sealed ? <span class="hub-muted"> · 已加密</span> : null}
                </td>
                <td>{account.source || "—"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <h3>Bot</h3>
      {counts.bots === 0 ? (
        <Empty text="还没有 Bot。" />
      ) : (
        <table class="hub-table">
          <thead>
            <tr>
              <th>名称</th>
              <th>启用</th>
              <th>凭据</th>
              <th>备注</th>
            </tr>
          </thead>
          <tbody>
            {doc?.bots?.map((bot) => (
              <tr key={bot.id} class={bot.enabled ? "" : "hub-row--disabled"}>
                <td>{bot.name}</td>
                <td>{bot.enabled ? "是" : "否"}</td>
                <td class="hub-mono">
                  {bot.grant?.token_hint || "—"}
                  {bot.grant?.sealed ? <span class="hub-muted"> · 已加密</span> : null}
                </td>
                <td class="hub-muted">{bot.note || "—"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <h3>连接</h3>
      {counts.connections === 0 ? (
        <Empty text="还没有连接。在「监听端点」或「拨号目标」里添加。" />
      ) : (
        <table class="hub-table">
          <thead>
            <tr>
              <th>名称</th>
              <th>类型</th>
              <th>地址</th>
              <th>账号 / Bot</th>
              <th>凭据</th>
              <th>启用</th>
            </tr>
          </thead>
          <tbody>
            {doc?.connections?.map((connection) => (
              <tr key={connection.id} class={connection.enabled ? "" : "hub-row--disabled"}>
                <td>{connection.name}</td>
                <td>{kindLabel(connection.kind)}</td>
                <td class="hub-mono">{connection.addr || connection.url || "—"}</td>
                <td class="hub-mono">{connection.account_self_id || connection.bot_name || "—"}</td>
                <td class="hub-mono">{connection.grant?.token_hint || "—"}</td>
                <td>{connection.enabled ? "是" : "否"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <h3>绑定</h3>
      {counts.bindings === 0 ? (
        <Empty text="还没有绑定关系。" />
      ) : (
        <table class="hub-table">
          <thead>
            <tr>
              <th>Bot</th>
              <th>账号</th>
              <th>默认</th>
              <th>优先级</th>
              <th>启用</th>
            </tr>
          </thead>
          <tbody>
            {doc?.bindings?.map((binding) => (
              <tr key={binding.id} class={binding.enabled ? "" : "hub-row--disabled"}>
                <td>{binding.bot_name}</td>
                <td class="hub-mono">{binding.account_self_id}</td>
                <td>{binding.is_default ? "是" : "—"}</td>
                <td>{binding.priority}</td>
                <td>{binding.enabled ? "是" : "否"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {editing ? (
        <Modal title="connect.json（可直接编辑或粘贴导入）" onClose={() => setEditing(null)} wide>
          <form onSubmit={submitImport}>
            <label class="hub-field">
              <span class="hub-field-label">文档内容</span>
              <textarea
                class="hub-textarea"
                rows={18}
                value={draft}
                onInput={(e) => setDraft((e.target as HTMLTextAreaElement).value)}
              />
            </label>
            <p class="hub-warn">
              导入会整体替换当前连接文件，原文件保留为 <code>.bak</code>。
              粘贴的文件里 token 可以是明文，导入时会自动加密。
            </p>
            <div class="hub-row hub-row--end">
              <button type="submit">导入并应用</button>
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

function kindLabel(kind: string): string {
  switch (kind) {
    case "upstream_listen":
      return "上游监听（QQ 接入）";
    case "upstream_dial":
      return "上游拨号（连 QQ）";
    case "downstream_listen":
      return "下游监听（Bot 接入）";
    case "downstream_dial":
      return "下游拨号（连 Bot）";
    default:
      return kind;
  }
}
