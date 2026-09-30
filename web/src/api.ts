// Thin REST client for the management API. Cookies carry the session, so every
// request is sent with credentials; unsafe methods get the CSRF token attached
// by the wrapper below (the token arrives with the login response).

export interface HealthStatus {
  status: string;
  version: string;
  uptime_sec: number;
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

export function setCSRFToken(token: string): void {
  csrfToken = token;
}

const UNSAFE_METHODS = new Set(["POST", "PUT", "PATCH", "DELETE"]);

export async function api<T>(path: string, init: RequestInit = {}): Promise<T> {
  const method = (init.method ?? "GET").toUpperCase();
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  if (UNSAFE_METHODS.has(method) && csrfToken) {
    headers.set("X-CSRF-Token", csrfToken);
  }

  const res = await fetch(path, { ...init, method, headers, credentials: "same-origin" });
  const text = await res.text();
  if (!res.ok) {
    let message = res.statusText || String(res.status);
    try {
      const parsed = JSON.parse(text) as { error?: string; message?: string };
      message = parsed.error ?? parsed.message ?? message;
    } catch {
      if (text) message = text.slice(0, 200);
    }
    throw new ApiError(res.status, message);
  }
  if (!text) return undefined as T;
  return JSON.parse(text) as T;
}

export function getHealth(): Promise<HealthStatus> {
  return api<HealthStatus>("/healthz");
}
