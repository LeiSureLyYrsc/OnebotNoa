import type { ComponentChildren } from "preact";
import { useState } from "preact/hooks";
import { ApiError, login } from "../api";
import { setState } from "../state";

export function LoginPage() {
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function submit(event: Event) {
    event.preventDefault();
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      const result = await login(username.trim(), password);
      setState({ user: result.user, page: "dashboard", booting: false });
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div class="hub-desktop hub-login-bg">
      <div class="hub-login-wrap">
        <div class="window hub-login">
          <div class="title-bar">
            <div class="title-bar-text">OnebotNoa — 登录</div>
            <div class="title-bar-controls">
              <button aria-label="Minimize" />
              <button aria-label="Maximize" />
              <button aria-label="Close" />
            </div>
          </div>
          <div class="window-body">
            <div class="hub-login-banner">
              <div class="hub-login-logo">OnebotNoa</div>
              <div class="hub-muted">OneBot V11 中继管理端</div>
            </div>
            <form onSubmit={submit}>
              <FieldRow label="用户名">
                <input
                  type="text"
                  value={username}
                  autoComplete="username"
                  onInput={(e) => setUsername((e.target as HTMLInputElement).value)}
                />
              </FieldRow>
              <FieldRow label="密码">
                <input
                  type="password"
                  value={password}
                  autoComplete="current-password"
                  onInput={(e) => setPassword((e.target as HTMLInputElement).value)}
                />
              </FieldRow>
              {error ? <p class="hub-error">{error}</p> : null}
              <div class="hub-row hub-row--end">
                <button type="submit" disabled={busy}>
                  {busy ? "登录中…" : "登录"}
                </button>
              </div>
            </form>
            <p class="hub-muted">
              首次启动的管理员密码打印在服务端日志里（<span class="hub-mono">docker compose logs onebotnoa | grep -i password</span>）；
              也可以用 <span class="hub-mono">onebotnoa reset-password</span> 重置。
            </p>
          </div>
          <div class="status-bar">
            <p class="status-bar-field">请使用管理员账号登录</p>
            <p class="status-bar-field">会话有效期 7 天</p>
          </div>
        </div>
      </div>
    </div>
  );
}

function FieldRow(props: { label: string; children: ComponentChildren }) {
  return (
    <div class="hub-row hub-login-row">
      <label class="hub-login-label">{props.label}</label>
      {props.children}
    </div>
  );
}
