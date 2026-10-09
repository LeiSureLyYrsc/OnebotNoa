import { useState } from "preact/hooks";
import { api, Bot, ConnectInfo } from "../api";
import { Modal, StatusDot, useInterval, Window } from "../ui";
import { ConnectRow } from "./accounts";

export function BotsPage() {
  const [bots, setBots] = useState<Bot[]>([]);
  const [name, setName] = useState("");
  const [error, setError] = useState("");
  const [info, setInfo] = useState<ConnectInfo | null>(null);

  const load = () => {
    api
      .bots()
      .then((r) => {
        setBots(r.bots || []);
        setError("");
      })
      .catch((e) => setError(String(e.message || e)));
  };
  useInterval(load, 4000);

  const create = () => {
    if (!name.trim()) return;
    api.botCreate(name.trim()).then(() => {
      setName("");
      load();
    });
  };

  return (
    <div class="stack">
      <Window title="新建 Bot 实例">
        <div class="field-row" style={{ display: "flex", gap: "6px", alignItems: "flex-end" }}>
          <div>
            <label>名称</label>
            <br />
            <input value={name} onInput={(e) => setName((e.target as HTMLInputElement).value)} />
          </div>
          <button onClick={create}>创建</button>
        </div>
        {error ? <p style={{ color: "#a00" }}>{error}</p> : null}
      </Window>

      <Window title="Bot 实例列表">
        <div class="sunken">
          <table class="grid">
            <thead>
              <tr>
                <th>状态</th>
                <th>名称</th>
                <th>在线连接</th>
                <th>绑定账号数</th>
                <th>启用</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {bots.map((b) => (
                <tr key={b.id}>
                  <td>
                    <StatusDot status={b.online > 0 ? "online" : "offline"} />
                    {b.online > 0 ? "online" : "offline"}
                  </td>
                  <td>{b.name}</td>
                  <td>{b.online}</td>
                  <td>{b.accounts?.length || 0}</td>
                  <td>{b.enabled ? "是" : "否"}</td>
                  <td>
                    <div class="row-actions">
                      <button onClick={() => api.botConnect(b.id).then(setInfo)}>连接信息</button>
                      <button onClick={() => api.botRotate(b.id).then(load)}>轮换 token</button>
                      <button onClick={() => api.botUpdate(b.id, { enabled: !b.enabled }).then(load)}>
                        {b.enabled ? "禁用" : "启用"}
                      </button>
                      <button onClick={() => confirm(`删除 ${b.name}?`) && api.botDelete(b.id).then(load)}>删除</button>
                    </div>
                  </td>
                </tr>
              ))}
              {bots.length === 0 ? (
                <tr>
                  <td colSpan={6} class="muted">
                    暂无 Bot
                  </td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </div>
      </Window>

      {info ? (
        <Modal title={`连接信息 · ${info.name || ""}`} onClose={() => setInfo(null)}>
          <p class="muted">把下面的地址与 token 填入 Bot 框架（如 NoneBot2 / Koishi）。</p>
          <ConnectRow label="Access token" value={info.token} />
          {info.ws_aggregate ? <ConnectRow label="聚合视图" value={info.ws_aggregate} /> : null}
          {info.ws_single ? <ConnectRow label="透明单账号视图（替换 {self_id}）" value={info.ws_single} /> : null}
          {info.http_api ? <ConnectRow label="HTTP API" value={info.http_api} /> : null}
        </Modal>
      ) : null}
    </div>
  );
}
