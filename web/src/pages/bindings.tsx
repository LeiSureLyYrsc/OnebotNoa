import { useState } from "preact/hooks";
import { api, Account, Binding, Bot } from "../api";
import { useInterval, Window } from "../ui";

export function BindingsPage() {
  const [bindings, setBindings] = useState<Binding[]>([]);
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [bots, setBots] = useState<Bot[]>([]);
  const [botId, setBotId] = useState("");
  const [accountId, setAccountId] = useState("");
  const [isDefault, setIsDefault] = useState(false);
  const [scope, setScope] = useState("{}");
  const [error, setError] = useState("");

  const load = () => {
    api.bindings().then((r) => setBindings(r.bindings || [])).catch((e) => setError(String(e.message || e)));
    api.accounts().then((r) => setAccounts(r.accounts || [])).catch(() => undefined);
    api.bots().then((r) => setBots(r.bots || [])).catch(() => undefined);
  };
  useInterval(load, 5000);

  const create = () => {
    if (!botId || !accountId) return;
    let parsed = scope;
    try {
      JSON.parse(scope);
    } catch {
      setError("scope 不是合法 JSON");
      return;
    }
    api
      .bindingCreate({
        bot_id: Number(botId),
        account_id: Number(accountId),
        priority: 100,
        is_default: isDefault,
        scope: parsed,
      })
      .then(() => {
        setError("");
        load();
      })
      .catch((e) => setError(String(e.message || e)));
  };

  const accountName = (id: number) => accounts.find((a) => a.id === id)?.self_id || `#${id}`;
  const botName = (id: number) => bots.find((b) => b.id === id)?.name || `#${id}`;

  return (
    <div class="stack">
      <Window title="新建绑定">
        <div class="field-row" style={{ display: "flex", gap: "8px", flexWrap: "wrap", alignItems: "flex-end" }}>
          <div>
            <label>Bot</label>
            <br />
            <select value={botId} onChange={(e) => setBotId((e.target as HTMLSelectElement).value)}>
              <option value="">选择 Bot</option>
              {bots.map((b) => (
                <option key={b.id} value={b.id}>
                  {b.name}
                </option>
              ))}
            </select>
          </div>
          <div>
            <label>QQ 实例</label>
            <br />
            <select value={accountId} onChange={(e) => setAccountId((e.target as HTMLSelectElement).value)}>
              <option value="">选择账号</option>
              {accounts.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.self_id} {a.name ? `(${a.name})` : ""}
                </option>
              ))}
            </select>
          </div>
          <label>
            <input type="checkbox" checked={isDefault} onChange={(e) => setIsDefault((e.target as HTMLInputElement).checked)} />
            默认账号
          </label>
          <button onClick={create}>绑定</button>
        </div>
        <div class="field-row">
          <label>事件过滤 scope (JSON)</label>
          <br />
          <input
            style={{ width: "100%" }}
            value={scope}
            onInput={(e) => setScope((e.target as HTMLInputElement).value)}
            placeholder='{"post_types":["message"],"include_groups":[]}'
          />
        </div>
        {error ? <p style={{ color: "#a00" }}>{error}</p> : null}
      </Window>

      <Window title="绑定关系">
        <div class="sunken">
          <table class="grid">
            <thead>
              <tr>
                <th>Bot</th>
                <th>QQ 实例</th>
                <th>优先级</th>
                <th>默认</th>
                <th>启用</th>
                <th>scope</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {bindings.map((b) => (
                <tr key={b.id}>
                  <td>{botName(b.bot_id)}</td>
                  <td>{accountName(b.account_id)}</td>
                  <td>{b.priority}</td>
                  <td>{b.is_default ? "是" : "否"}</td>
                  <td>{b.enabled ? "是" : "否"}</td>
                  <td class="muted" style={{ maxWidth: "220px", overflow: "hidden", textOverflow: "ellipsis" }}>
                    {b.scope}
                  </td>
                  <td>
                    <div class="row-actions">
                      <button onClick={() => api.bindingUpdate(b.id, { is_default: !b.is_default }).then(load)}>
                        {b.is_default ? "取消默认" : "设为默认"}
                      </button>
                      <button onClick={() => api.bindingUpdate(b.id, { enabled: !b.enabled }).then(load)}>
                        {b.enabled ? "禁用" : "启用"}
                      </button>
                      <button onClick={() => api.bindingDelete(b.id).then(load)}>删除</button>
                    </div>
                  </td>
                </tr>
              ))}
              {bindings.length === 0 ? (
                <tr>
                  <td colSpan={7} class="muted">
                    暂无绑定
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
