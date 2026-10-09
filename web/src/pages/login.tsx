import { useState } from "preact/hooks";
import { api } from "../api";
import { Field, Window } from "../ui";

export function LoginPage(props: { onLogin: () => void }) {
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = (e: Event) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    api
      .login(username, password)
      .then(() => props.onLogin())
      .catch((err) => setError(String(err.message || err)))
      .finally(() => setBusy(false));
  };

  return (
    <div class="desktop">
      <div class="shell">
        <div class="login-wrap">
          <Window title="OnebotNoa 登录">
            <form onSubmit={submit}>
              <Field label="用户名">
                <input value={username} onInput={(e) => setUsername((e.target as HTMLInputElement).value)} />
              </Field>
              <Field label="密码">
                <input
                  type="password"
                  value={password}
                  onInput={(e) => setPassword((e.target as HTMLInputElement).value)}
                />
              </Field>
              {error ? <p style={{ color: "#a00" }}>{error}</p> : null}
              <button type="submit" disabled={busy}>
                {busy ? "登录中..." : "登录"}
              </button>
            </form>
          </Window>
        </div>
      </div>
    </div>
  );
}
