import { useState } from "preact/hooks";
import { api, Account, Bot, Listener } from "../api";
import { useInterval, Window } from "../ui";

export function ListenersPage() {
  const [listeners, setListeners] = useState<Listener[]>([]);
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [bots, setBots] = useState<Bot[]>([]);
  const [name, setName] = useState("");
  const [role, setRole] = useState("upstream");
  const [bindAddr, setBindAddr] = useState("0.0.0.0:6710");
  const [path, setPath] = useState("/onebot/v11/ws");
  const [owner, setOwner] = useState("");
  const [error, setError] = useState("");

  const load = () => {
    api.listeners().then((r) => setListeners(r.listeners || [])).catch((e) => setError(String(e.message || e)));
    api.accounts().then((r) => setAccounts(r.accounts || [])).catch(() => undefined);
    api.bots().then((r) => setBots(r.bots || [])).catch(() => undefined);
  };
  useInterval(load, 5000);

  const create = () => {
    const body: Partial<Listener> = { name, role, bind_addr: bindAddr, path, enabled: true };
    if (owner) {
      if (role === "upstream") body.account_id = Number(owner);
      else body.bot_id = Number(owner);
    }
    api
      .listenerCreate(body)
      .then(() => {
        setError("");
        setName("");
        setOwner("");
        load();
      })
      .catch((e) => setError(String(e.message || e)));
  };

  const accountName = (id?: number | null) => (id ? accounts.find((a) => a.id === id)?.self_id || `#${id}` : "-");
  const botName = (id?: number | null) => (id ? bots.find((b) => b.id === id)?.name || `#${id}` : "-");

  return (
    <div class="stack">
      <Window title="新建监听端点（另开端口 / 独立路径）">
        <div class="field-row" style={{ display: "flex", gap: "8px", flexWrap: "wrap", alignItems: "flex-end" }}>
          <div>
            <label>名称</label>
            <br />
            <input value={name} onInput={(e) => setName((e.target as HTMLInputElement).value)} />
          </div>
          <div>
            <label>角色</label>
            <br />
            <select value={role} onChange={(e) => { setRole((e.target as HTMLSelectElement).value); setOwner(""); }}>
              <option value="upstream">upstream (QQ 接入)</option>
              <option value="downstream">downstream (Bot 接入)</option>
            </select>
          </div>
          <div>
            <label>绑定地址</label>
            <br />
            <input value={bindAddr} onInput={(e) => setBindAddr((e.target as HTMLInputElement).value)} />
          </div>
          <div>
            <label>路径</label>
            <br />
            <input value={path} onInput={(e) => setPath((e.target as HTMLInputElement).value)} />
          </div>
          <div>
            <label>归属（可选）</label>
            <br />
            <select value={owner} onChange={(e) => setOwner((e.target as HTMLSelectElement).value)}>
              <option value="">无</option>
              {role === "upstream"
                ? accounts.map((a) => (
                    <option key={a.id} value={a.id}>
                      {a.self_id}
                    </option>
                  ))
                : bots.map((b) => (
                    <option key={b.id} value={b.id}>
                      {b.name}
                    </option>
                  ))}
            </select>
          </div>
          <button onClick={create}>创建</button>
        </div>
        {error ? <p style={{ color: "#a00" }}>{error}</p> : null}
      </Window>

      <Window title="监听端点">
        <div class="sunken">
          <table class="grid">
            <thead>
              <tr>
                <th>名称</th>
                <th>角色</th>
                <th>地址</th>
                <th>路径</th>
                <th>归属</th>
                <th>启用</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {listeners.map((l) => (
                <tr key={l.id}>
                  <td>{l.name}</td>
                  <td>{l.role}</td>
                  <td>{l.bind_addr}</td>
                  <td>{l.path}</td>
                  <td>{l.role === "upstream" ? accountName(l.account_id) : botName(l.bot_id)}</td>
                  <td>{l.enabled ? "是" : "否"}</td>
                  <td>
                    <div class="row-actions">
                      <button onClick={() => api.listenerUpdate(l.id, { enabled: !l.enabled }).then(load)}>
                        {l.enabled ? "停止" : "启动"}
                      </button>
                      <button onClick={() => api.listenerDelete(l.id).then(load)}>删除</button>
                    </div>
                  </td>
                </tr>
              ))}
              {listeners.length === 0 ? (
                <tr>
                  <td colSpan={7} class="muted">
                    当前只使用共享端点
                  </td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </div>
      </Window>
    </div>
  );
}
