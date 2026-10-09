import { useEffect, useState } from "preact/hooks";
import { api } from "../api";
import { Window } from "../ui";

export function SystemPage() {
  const [exported, setExported] = useState("");
  const [importText, setImportText] = useState("");
  const [message, setMessage] = useState("");
  const [audit, setAudit] = useState<any[]>([]);

  const loadAudit = () => api.audit(100).then((r) => setAudit(r.audit || []));
  useEffect(() => {
    loadAudit().catch(() => undefined);
  }, []);

  const doExport = () =>
    api.connectExport().then((doc) => setExported(JSON.stringify(doc, null, 2))).catch((e) => setMessage(String(e.message || e)));

  const doImport = () => {
    try {
      const doc = JSON.parse(importText);
      api.connectImport(doc).then((r) => setMessage(`导入完成：${JSON.stringify(r)}`)).catch((e) => setMessage(String(e.message || e)));
    } catch {
      setMessage("导入内容不是合法 JSON");
    }
  };

  return (
    <div class="stack">
      <Window title="connect.json · 导出 / 导入">
        <div class="row-actions" style={{ marginBottom: "6px" }}>
          <button onClick={doExport}>生成导出</button>
          <button onClick={() => navigator.clipboard?.writeText(exported)}>复制导出</button>
        </div>
        <textarea
          readOnly
          value={exported}
          style={{ width: "100%", height: "160px", fontFamily: "Consolas, monospace" }}
          placeholder="点击“生成导出”"
        />
        <div class="field-row" style={{ marginTop: "8px" }}>
          <label>导入（粘贴连接文档，按名称合并）</label>
          <br />
          <textarea
            value={importText}
            onInput={(e) => setImportText((e.target as HTMLTextAreaElement).value)}
            style={{ width: "100%", height: "120px", fontFamily: "Consolas, monospace" }}
          />
        </div>
        <button onClick={doImport}>导入</button>
        {message ? <p>{message}</p> : null}
      </Window>

      <Window title="审计日志">
        <div class="sunken" style={{ maxHeight: "260px" }}>
          <table class="grid">
            <thead>
              <tr>
                <th>时间</th>
                <th>操作者</th>
                <th>动作</th>
                <th>目标</th>
                <th>来源</th>
              </tr>
            </thead>
            <tbody>
              {audit.map((a) => (
                <tr key={a.id}>
                  <td>{new Date(a.at * 1000).toLocaleString()}</td>
                  <td>{a.actor}</td>
                  <td>{a.action}</td>
                  <td>{a.target}</td>
                  <td class="muted">{a.ip}</td>
                </tr>
              ))}
              {audit.length === 0 ? (
                <tr>
                  <td colSpan={5} class="muted">
                    暂无记录
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
