import { useEffect, useState } from "preact/hooks";
import { api } from "./api";
import { Window } from "./ui";
import { LoginPage } from "./pages/login";
import { DashboardPage } from "./pages/dashboard";
import { AccountsPage } from "./pages/accounts";
import { BotsPage } from "./pages/bots";
import { BindingsPage } from "./pages/bindings";
import { ListenersPage } from "./pages/listeners";
import { EventsPage } from "./pages/events";
import { ConsolePage } from "./pages/console";
import { SystemPage } from "./pages/system";

const PAGES: { key: string; label: string }[] = [
  { key: "dashboard", label: "仪表盘" },
  { key: "accounts", label: "QQ 实例" },
  { key: "bots", label: "Bot 实例" },
  { key: "bindings", label: "绑定关系" },
  { key: "listeners", label: "监听端点" },
  { key: "events", label: "实时事件" },
  { key: "console", label: "API 调试台" },
  { key: "system", label: "系统 / 连接文件" },
];

function renderPage(page: string) {
  switch (page) {
    case "accounts":
      return <AccountsPage />;
    case "bots":
      return <BotsPage />;
    case "bindings":
      return <BindingsPage />;
    case "listeners":
      return <ListenersPage />;
    case "events":
      return <EventsPage />;
    case "console":
      return <ConsolePage />;
    case "system":
      return <SystemPage />;
    default:
      return <DashboardPage />;
  }
}

export function App() {
  const [authed, setAuthed] = useState<boolean | null>(null);
  const [page, setPage] = useState("dashboard");

  useEffect(() => {
    api
      .me()
      .then(() => setAuthed(true))
      .catch(() => setAuthed(false));
  }, []);

  if (authed === null) {
    return (
      <div class="desktop">
        <div class="shell">
          <Window title="OnebotNoa">正在加载...</Window>
        </div>
      </div>
    );
  }

  if (!authed) {
    return <LoginPage onLogin={() => setAuthed(true)} />;
  }

  return (
    <div class="desktop">
      <div class="shell">
        <Window title="OnebotNoa · OneBot V11 中继管理端">
          <div class="toolbar">
            {PAGES.map((p) => (
              <button key={p.key} aria-pressed={page === p.key} onClick={() => setPage(p.key)}>
                {p.label}
              </button>
            ))}
            <span style={{ flex: "1" }} />
            <button onClick={() => api.logout().then(() => setAuthed(false))}>注销</button>
          </div>
          <div style={{ marginTop: "8px" }}>{renderPage(page)}</div>
          <div class="status-bar" style={{ marginTop: "8px" }}>
            <div class="status-bar-field">OneBot V11</div>
            <div class="status-bar-field">Preact + XP.css</div>
          </div>
        </Window>
      </div>
    </div>
  );
}
