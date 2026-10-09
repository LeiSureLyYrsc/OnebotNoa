export type Account = {
  id: number;
  self_id: string;
  name: string;
  nickname: string;
  enabled: boolean;
  status: string;
  online: number;
  last_seen_at: number;
  connections?: { peer_id: string; role: string }[];
};

export type Bot = {
  id: number;
  name: string;
  enabled: boolean;
  note: string;
  online: number;
  rate_limit: string;
  action_policy: string;
  accounts: number[];
};

export type Binding = {
  id: number;
  bot_id: number;
  account_id: number;
  priority: number;
  is_default: boolean;
  enabled: boolean;
  scope: string;
};

export type Listener = {
  id: number;
  name: string;
  role: string;
  bind_addr: string;
  path: string;
  account_id?: number | null;
  bot_id?: number | null;
  enabled: boolean;
};

export type FeedEvent = {
  time: number;
  direction: string;
  account?: string;
  bot?: string;
  kind: string;
  summary: string;
  raw?: unknown;
};

export type ConnectInfo = {
  self_id?: string;
  name?: string;
  token: string;
  ws_shared?: string;
  ws_token?: string;
  http_report?: string;
  ws_aggregate?: string;
  ws_single?: string;
  http_api?: string;
};

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    credentials: "include",
    headers: { "Content-Type": "application/json" },
    ...init,
  });
  const text = await res.text();
  const data = text ? JSON.parse(text) : null;
  if (!res.ok) {
    throw new Error((data && data.error) || res.statusText || "request failed");
  }
  return data as T;
}

const json = (body: unknown): RequestInit => ({ body: JSON.stringify(body) });

export const api = {
  login: (username: string, password: string) =>
    request<{ ok: boolean; username: string }>("/api/v1/login", { method: "POST", ...json({ username, password }) }),
  logout: () => request<{ ok: boolean }>("/api/v1/logout", { method: "POST" }),
  me: () => request<{ username: string }>("/api/v1/me"),

  accounts: () => request<{ accounts: Account[]; pending: string[] }>("/api/v1/accounts"),
  accountCreate: (self_id: string, name: string) =>
    request<{ account: Account }>("/api/v1/accounts", { method: "POST", ...json({ self_id, name }) }),
  accountUpdate: (id: number, body: { name?: string; enabled?: boolean }) =>
    request<{ ok: boolean }>(`/api/v1/accounts/${id}`, { method: "PATCH", ...json(body) }),
  accountDelete: (id: number) => request<{ ok: boolean }>(`/api/v1/accounts/${id}`, { method: "DELETE" }),
  accountRotate: (id: number) => request<{ token: string }>(`/api/v1/accounts/${id}/token/rotate`, { method: "POST" }),
  accountConnect: (id: number) => request<ConnectInfo>(`/api/v1/accounts/${id}/connect`),
  accountApprove: (self_id: string) =>
    request<{ ok: boolean }>("/api/v1/accounts/approve", { method: "POST", ...json({ self_id }) }),

  bots: () => request<{ bots: Bot[] }>("/api/v1/bots"),
  botCreate: (name: string) => request<{ bot: Bot }>("/api/v1/bots", { method: "POST", ...json({ name }) }),
  botUpdate: (id: number, body: Partial<Bot>) =>
    request<{ ok: boolean }>(`/api/v1/bots/${id}`, { method: "PATCH", ...json(body) }),
  botDelete: (id: number) => request<{ ok: boolean }>(`/api/v1/bots/${id}`, { method: "DELETE" }),
  botRotate: (id: number) => request<{ token: string }>(`/api/v1/bots/${id}/token/rotate`, { method: "POST" }),
  botConnect: (id: number) => request<ConnectInfo>(`/api/v1/bots/${id}/connect`),

  bindings: () => request<{ bindings: Binding[] }>("/api/v1/bindings"),
  bindingCreate: (body: { bot_id: number; account_id: number; priority: number; is_default: boolean; scope: string }) =>
    request<Binding>("/api/v1/bindings", { method: "POST", ...json(body) }),
  bindingUpdate: (id: number, body: Partial<Binding>) =>
    request<{ ok: boolean }>(`/api/v1/bindings/${id}`, { method: "PATCH", ...json(body) }),
  bindingDelete: (id: number) => request<{ ok: boolean }>(`/api/v1/bindings/${id}`, { method: "DELETE" }),

  listeners: () => request<{ listeners: Listener[] }>("/api/v1/listeners"),
  listenerCreate: (body: Partial<Listener>) =>
    request<Listener>("/api/v1/listeners", { method: "POST", ...json(body) }),
  listenerUpdate: (id: number, body: Partial<Listener>) =>
    request<{ ok: boolean }>(`/api/v1/listeners/${id}`, { method: "PATCH", ...json(body) }),
  listenerDelete: (id: number) => request<{ ok: boolean }>(`/api/v1/listeners/${id}`, { method: "DELETE" }),

  connections: () =>
    request<{ upstream: { self_id: string; peer_id: string; role: string }[]; downstream: { id: number; name: string; online: number }[] }>(
      "/api/v1/connections",
    ),
  eventsRecent: (limit = 100) => request<{ events: FeedEvent[] }>(`/api/v1/events/recent?limit=${limit}`),
  invoke: (self_id: string, action: string, params: unknown) =>
    request<any>("/api/v1/console/invoke", { method: "POST", ...json({ self_id, action, params }) }),

  system: () =>
    request<{
      version: string;
      uptime_seconds: number;
      accounts_total: number;
      accounts_online: number;
      bots_total: number;
      bots_online: number;
      pending_accounts: string[] | null;
      metrics: Record<string, number>;
    }>("/api/v1/system"),
  audit: (limit = 200) => request<{ audit: any[] }>(`/api/v1/audit?limit=${limit}`),
  connectExport: () => request<any>("/api/v1/connect"),
  connectImport: (doc: unknown) => request<any>("/api/v1/connect", { method: "PUT", ...json(doc) }),
};

export function openEventStream(onEvent: (ev: FeedEvent) => void): () => void {
  const source = new EventSource("/api/v1/events/stream");
  source.onmessage = (msg) => {
    try {
      onEvent(JSON.parse(msg.data) as FeedEvent);
    } catch {
      /* ignore malformed frame */
    }
  };
  return () => source.close();
}
