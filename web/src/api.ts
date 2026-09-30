// Typed client for the OnebotNoa management API.
//
// The session lives in an HttpOnly cookie, so every request is sent with
// credentials; unsafe methods additionally carry the CSRF token that the login
// (or /auth/me) response handed us.

export interface User {
  id: number;
  username: string;
  role: string;
}

export interface Peer {
  id: string;
  role: string;
  remote_addr: string;
  user_agent?: string;
  queued: number;
  dropped: number;
}

export interface Account {
  id: number;
  self_id: string;
  name: string;
  nickname: string;
  avatar: string;
  enabled: boolean;
  status: string;
  live_state: string;
  has_token: boolean;
  tags: unknown;
  peers: Peer[];
  binding_count: number;
  last_seen_at?: string;
}

export interface PendingAccount {
  id: string;
  self_id: string;
  role: string;
  remote_addr: string;
  source?: string;
  first_seen: string;
  last_seen: string;
  attempts: number;
}

export interface Bot {
  id: number;
  name: string;
  enabled: boolean;
  note: string;
  rate_limit: unknown;
  action_policy: unknown;
  connections: number;
  binding_count: number;
  last_seen_at?: string;
}

export interface Scope {
  post_types?: string[];
  include_groups?: string[];
  exclude_groups?: string[];
  include_users?: string[];
  exclude_users?: string[];
  exclude_self?: boolean;
  meta_events?: string;
}

export interface Binding {
  id: number;
  bot_id: number;
  account_id: number;
  priority: number;
  is_default: boolean;
  enabled: boolean;
  scope: Scope;
  bot_name: string;
  account_self_id: string;
  account_name: string;
}

export interface Listener {
  id: number;
  name: string;
  kind: string;
  bind_addr: string;
  path: string;
  fixed_self_id: string;
  enabled: boolean;
  runtime: string;
}

export interface RelayCounters {
  accounts: number;
  online: number;
  degraded: number;
  offline: number;
  upstream_conns: number;
  downstream_conns: number;
  pending_accounts: number;
  pending_actions: number;
}

export interface EventCounters {
  ring_size: number;
  subscribers: number;
  dropped: number;
}

export interface SystemStatus {
  version: string;
  uptime_sec: number;
  started_at: string;
  listen: string;
  database: string;
  users: number;
  audit_entries: number;
  session_ttl: string;
  relay?: RelayCounters;
  events?: EventCounters;
}

export interface EventRecord {
  seq: number;
  at: string;
  kind: string;
  self_id?: string;
  bot?: string;
  role?: string;
  action?: string;
  post_type?: string;
  group_id?: string;
  user_id?: string;
  retcode?: number;
  note?: string;
  raw?: unknown;
  bytes: number;
  truncated?: boolean;
}

export class ApiError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

let csrfToken = "";
let refreshing: Promise<void> | null = null;

export function setCSRFToken(token: string): void {
  csrfToken = token;
}

const UNSAFE = new Set(["POST", "PUT", "PATCH", "DELETE"]);

async function call<T>(path: string, init: RequestInit = {}, retry = true): Promise<T> {
  const method = (init.method ?? "GET").toUpperCase();
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  if (UNSAFE.has(method) && csrfToken) {
    headers.set("X-CSRF-Token", csrfToken);
  }

  const res = await fetch(path, { ...init, method, headers, credentials: "same-origin" });
  const text = await res.text();

  // A stale CSRF token (server restarted, session rotated) is recoverable: ask
  // for a fresh one and replay the request exactly once.
  if (res.status === 403 && retry && UNSAFE.has(method)) {
    const ok = await refreshCSRF();
    if (ok) {
      return call<T>(path, init, false);
    }
  }

  if (!res.ok) {
    throw new ApiError(res.status, messageFrom(res.status, text));
  }
  if (!text) {
    return undefined as T;
  }
  return JSON.parse(text) as T;
}

function messageFrom(status: number, text: string): string {
  try {
    const parsed = JSON.parse(text) as { error?: string };
    if (parsed.error) return parsed.error;
  } catch {
    /* not json */
  }
  return text ? text.slice(0, 200) : "HTTP " + status;
}

async function refreshCSRF(): Promise<boolean> {
  if (!refreshing) {
    refreshing = (async () => {
      try {
        const res = await fetch("/api/v1/auth/me", { credentials: "same-origin" });
        if (res.ok) {
          const payload = (await res.json()) as { csrf_token?: string };
          if (payload.csrf_token) csrfToken = payload.csrf_token;
        }
      } finally {
        refreshing = null;
      }
    })();
  }
  await refreshing;
  return csrfToken !== "";
}

// ---------------------------------------------------------------- auth

export interface LoginResult {
  user: User;
  csrf_token: string;
}

export async function login(username: string, password: string): Promise<LoginResult> {
  const result = await call<LoginResult>("/api/v1/auth/login", {
    method: "POST",
    body: JSON.stringify({ username, password }),
  });
  csrfToken = result.csrf_token;
  return result;
}

export async function logout(): Promise<void> {
  await call<void>("/api/v1/auth/logout", { method: "POST" });
  csrfToken = "";
}

export async function me(): Promise<{ user: User; csrf_token: string }> {
  const result = await call<{ user: User; csrf_token: string }>("/api/v1/auth/me");
  csrfToken = result.csrf_token;
  return result;
}

// ---------------------------------------------------------------- system

export function systemStatus(): Promise<SystemStatus> {
  return call<SystemStatus>("/api/v1/system/status");
}

// ---------------------------------------------------------------- accounts

export function listAccounts(): Promise<{ accounts: Account[]; pending: PendingAccount[] }> {
  return call("/api/v1/accounts");
}

export function createAccount(selfId: string, name: string): Promise<{ account: Account }> {
  return call("/api/v1/accounts", { method: "POST", body: JSON.stringify({ self_id: selfId, name }) });
}

export function updateAccount(id: number, patch: Record<string, unknown>): Promise<{ account: Account }> {
  return call("/api/v1/accounts/" + id, { method: "PATCH", body: JSON.stringify(patch) });
}

export function deleteAccount(id: number): Promise<{ deleted: string }> {
  return call("/api/v1/accounts/" + id, { method: "DELETE" });
}

export function rotateAccountToken(id: number): Promise<{ token: string; hint: string }> {
  return call("/api/v1/accounts/" + id + "/token", { method: "POST" });
}

export function clearAccountToken(id: number): Promise<{ account: Account }> {
  return call("/api/v1/accounts/" + id + "/token", { method: "DELETE" });
}

export function approvePending(id: string): Promise<{ account: Account; hint: string }> {
  return call("/api/v1/accounts/pending/approve", { method: "POST", body: JSON.stringify({ id }) });
}

export function rejectPending(id: string): Promise<{ rejected: string }> {
  return call("/api/v1/accounts/pending/reject", { method: "POST", body: JSON.stringify({ id }) });
}

// ---------------------------------------------------------------- bots

export function listBots(): Promise<{ bots: Bot[] }> {
  return call("/api/v1/bots");
}

export function createBot(name: string, note: string): Promise<{ bot: Bot; token: string; hint: string }> {
  return call("/api/v1/bots", { method: "POST", body: JSON.stringify({ name, note }) });
}

export function updateBot(id: number, patch: Record<string, unknown>): Promise<{ bot: Bot }> {
  return call("/api/v1/bots/" + id, { method: "PATCH", body: JSON.stringify(patch) });
}

export function deleteBot(id: number): Promise<{ deleted: string }> {
  return call("/api/v1/bots/" + id, { method: "DELETE" });
}

export function rotateBotToken(id: number): Promise<{ token: string; hint: string }> {
  return call("/api/v1/bots/" + id + "/token/rotate", { method: "POST" });
}

// ---------------------------------------------------------------- bindings

export function listBindings(): Promise<{ bindings: Binding[] }> {
  return call("/api/v1/bindings");
}

export function createBinding(payload: Record<string, unknown>): Promise<{ bindings: Binding[] }> {
  return call("/api/v1/bindings", { method: "POST", body: JSON.stringify(payload) });
}

export function updateBinding(id: number, patch: Record<string, unknown>): Promise<{ bindings: Binding[] }> {
  return call("/api/v1/bindings/" + id, { method: "PATCH", body: JSON.stringify(patch) });
}

export function deleteBinding(id: number): Promise<{ bindings: Binding[] }> {
  return call("/api/v1/bindings/" + id, { method: "DELETE" });
}

// ---------------------------------------------------------------- listeners

export function listListeners(): Promise<{ listeners: Listener[]; shared: Record<string, string>; runtime_note: string }> {
  return call("/api/v1/listeners");
}

export function createListener(payload: Record<string, unknown>): Promise<{ listener: Listener }> {
  return call("/api/v1/listeners", { method: "POST", body: JSON.stringify(payload) });
}

export function deleteListener(id: number): Promise<{ deleted: string }> {
  return call("/api/v1/listeners/" + id, { method: "DELETE" });
}

// ---------------------------------------------------------------- events

export function recentEvents(filter: string): Promise<{ events: EventRecord[]; subscribers: number; dropped: number; ring_size: number }> {
  return call("/api/v1/events/recent" + filter);
}

// ---------------------------------------------------------------- helpers

export function formatTime(iso?: string): string {
  if (!iso) return "—";
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return iso;
  return date.toLocaleString("zh-CN", { hour12: false });
}

export function formatClock(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return iso;
  return date.toLocaleTimeString("zh-CN", { hour12: false });
}

export function formatBytes(bytes: number): string {
  if (bytes < 1024) return bytes + " B";
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + " KB";
  return (bytes / 1024 / 1024).toFixed(2) + " MB";
}

export function stateLabel(state: string): string {
  switch (state) {
    case "online":
      return "在线";
    case "degraded":
      return "降级";
    case "connecting":
      return "连接中";
    default:
      return "离线";
  }
}
