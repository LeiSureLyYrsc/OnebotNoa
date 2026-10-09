import { useState } from "preact/hooks";
import { api, Account, ConnectInfo } from "../api";
import { CopyButton, Modal, StatusDot, useInterval, Window } from "../ui";

export function AccountsPage() {
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [pending, setPending] = useState<string[]>([]);
  const [error, setError] = useState("");
  const [newId, setNewId] = useState("");
  const [newName, setNewName] = useState("");
  const [info, setInfo] = useState<ConnectInfo | null>(null);

  const load = () => {
    api
      .accounts()
      .then((r) => {
        setAccounts(r.accounts || []);
        setPending(r.pending || []);
        setError("");
      })
      .catch((e) => setError(String(e.message || e)));
  };
  useInterval(load, 4000);

  const create = () => {
    if (!newId.trim()) return;
    api.accountCreate(newId.trim(), newName.trim()).then(() => {
      setNewId("");
      setNewName("");
      load();
    });
  };

  return (
    <div class="stack">
      <Window title="新建 QQ 实例">
        <div class="field-row" style={{ display: "flex", gap: "6px", alignItems: "flex-end", flexWrap: "wrap" }}>
          <div>
            <label>QQ 号 (self_id)</label>
            <br />
            <input value={newId} onInput={(e) => setNewId((e.target as HTMLInputElement).value)} />
          </div>
          <div>
            <label>备注名</label>
            <br />
            <input value={newName} onInput={(e) => setNewName((e.target as HTMLInputElement).value)} />
          </div>
          <button onClick={create}>创建</button>
        </div>
        {pending.length ? (
          <div style={{ marginTop: "6px" }}>
            <span style={{ color: "#a60" }}>待审批：</span>
            {pending.map((id) => (
              <button key={id} style={{ marginLeft: "4px" }} onClick={() => api.accountApprove(id).then(load)}>
                批准 {id}
              </button>
            ))}
          </div>
        ) : null}
        {error ? <p style={{ color: "#a00" }}>{error}</p> : null}
      </Window>

      <Window title="QQ 实例列表">
        <div class="sunken">
          <table class="grid">
            <thead>
              <tr>
                <th>状态</th>
                <th>self_id</th>
                <th>备注</th>
                <th>昵称</th>
                <th>连接</th>
                <th>启用</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {accounts.map((a) => (
                <tr key={a.id}>
                  <td>
                    <StatusDot status={a.status} />
                    {a.status}
                  </td>
                  <td>{a.self_id}</td>
                  <td>{a.name || <span class="muted">-</span>}</td>
                  <td>{a.nickname || <span class="muted">-</span>}</td>
                  <td>{a.online}</td>
                  <td>{a.enabled ? "是" : "否"}</td>
                  <td>
                    <div class="row-actions">
                      <button onClick={() => api.accountConnect(a.id).then(setInfo)}>连接信息</button>
                      <button onClick={() => api.accountRotate(a.id).then(load)}>轮换 token</button>
                      <button onClick={() => api.accountUpdate(a.id, { enabled: !a.enabled }).then(load)}>
                        {a.enabled ? "禁用" : "启用"}
                      </button>
                      <button onClick={() => confirm(`删除 ${a.self_id}?`) && api.accountDelete(a.id).then(load)}>
                        删除
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
              {accounts.length === 0 ? (
                <tr>
                  <td colSpan={7} class="muted">
                    暂无实例
                  </td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </div>
      </Window>

      {info ? (
        <Modal title={`连接信息 · ${info.self_id || ""}`} onClose={() => setInfo(null)}>
          <p class="muted">把下面的地址与 token 填入 QQ 实现端（如 NapCat / Lagrange）的反向 WebSocket。</p>
          <ConnectRow label="Access token" value={info.token} />
          {info.ws_shared ? <ConnectRow label="共享端点" value={info.ws_shared} /> : null}
          {info.ws_token ? <ConnectRow label="带 token 端点" value={info.ws_token} /> : null}
          {info.http_report ? <ConnectRow label="HTTP 上报" value={info.http_report} /> : null}
        </Modal>
      ) : null}
    </div>
  );
}

export function ConnectRow(props: { label: string; value: string }) {
  return (
    <div class="field-row">
      <label>{props.label}</label>
      <div style={{ display: "flex", gap: "4px", marginTop: "2px" }}>
        <input readOnly value={props.value} style={{ flex: "1", fontFamily: "Consolas, monospace" }} />
        <CopyButton text={props.value} />
      </div>
    </div>
  );
}
