import { useEffect, useState } from "preact/hooks";
import { ApiError, me, logout } from "./api";
import { useAppState, setState, go, type PageId } from "./state";
import { SecretDialog, Toast } from "./ui";
import { LoginPage } from "./pages/login";
import { DashboardPage } from "./pages/dashboard";
import { AccountsPage } from "./pages/accounts";
import { BotsPage } from "./pages/bots";
import { BindingsPage } from "./pages/bindings";
import { EventsPage } from "./pages/events";
import { ListenersPage } from "./pages/listeners";
import { EndpointsPage } from "./pages/endpoints";

const NAV: Array<{ id: PageId; label: string }> = [
  { id: "dashboard", label: "仪表盘" },
  { id: "accounts", label: "QQ 实例" },
  { id: "bots", label: "Bot 实例" },
  { id: "bindings", label: "绑定关系" },
  { id: "events", label: "实时事件" },
  { id: "listeners", label: "监听端点" },
  { id: "endpoints", label: "拨号目标" },
];

export function App() {
  const app = useAppState();
  const [menuOpen, setMenuOpen] = useState(false);
  const [now, setNow] = useState(() => new Date());

  useEffect(() => {
    let cancelled = false;
    me()
      .then((result) => {
        if (!cancelled) setState({ user: result.user, booting: false });
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        // 401 simply means "not logged in yet"; anything else is worth showing.
        const unauthorized = err instanceof ApiError && err.status === 401;
        setState({
          user: null,
          booting: false,
          toast: unauthorized ? null : { text: err instanceof ApiError ? err.message : String(err), kind: "error" },
        });
      });
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    const timer = window.setInterval(() => setNow(new Date()), 1000);
    return () => window.clearInterval(timer);
  }, []);

  if (app.booting) {
    return (
      <div class="hub-desktop hub-desktop--surface">
        <div class="hub-center hub-muted">正在连接服务端…</div>
      </div>
    );
  }

  if (!app.user) {
    return (
      <>
        <LoginPage />
        <Toast />
      </>
    );
  }

  return (
    <div class="hub-desktop">
      <div class="hub-menubar">
        {NAV.map((item) => (
          <button
            key={item.id}
            class={app.page === item.id ? "hub-menubar-item hub-menubar-item--active" : "hub-menubar-item"}
            onClick={() => go(item.id)}
          >
            {item.label}
          </button>
        ))}
        <span class="hub-spacer" />
        <span class="hub-muted">已登录：{app.user.username}</span>
        <button
          class="hub-menubar-item"
          onClick={async () => {
            await logout();
            setState({ user: null });
          }}
        >
          退出
        </button>
      </div>

      <div class="hub-workarea hub-workarea--pages">{renderPage(app.page)}</div>

      <div class="hub-taskbar">
        <div class="hub-start" onClick={() => setMenuOpen(!menuOpen)}>
          ⊞ 开始
        </div>
        {menuOpen ? (
          <div class="hub-startmenu" onMouseLeave={() => setMenuOpen(false)}>
            <div class="hub-startmenu-header">OnebotNoa</div>
            {NAV.map((item) => (
              <button
                key={item.id}
                class="hub-startmenu-item"
                onClick={() => {
                  go(item.id);
                  setMenuOpen(false);
                }}
              >
                {item.label}
              </button>
            ))}
          </div>
        ) : null}
        <div class="hub-tray">
          <span class="hub-muted">OnebotNoa</span>
          <span class="hub-clock">{now.toLocaleTimeString("zh-CN", { hour12: false })}</span>
        </div>
      </div>

      <SecretDialog />
      <Toast />
    </div>
  );
}

function renderPage(page: PageId) {
  switch (page) {
    case "accounts":
      return <AccountsPage />;
    case "bots":
      return <BotsPage />;
    case "bindings":
      return <BindingsPage />;
    case "events":
      return <EventsPage />;
    case "listeners":
      return <ListenersPage />;
    case "endpoints":
      return <EndpointsPage />;
    default:
      return <DashboardPage />;
  }
}
