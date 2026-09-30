// Minimal observable store: enough for a small admin UI without a framework.
import { useEffect, useState } from "preact/hooks";
import type { User } from "./api";

export type PageId = "dashboard" | "accounts" | "bots" | "bindings" | "events" | "listeners" | "endpoints";

export interface AppState {
  user: User | null;
  booting: boolean;
  page: PageId;
  toast: { text: string; kind: "info" | "error" } | null;
  secret: { title: string; value: string; hint: string } | null;
}

export const state: AppState = {
  user: null,
  booting: true,
  page: "dashboard",
  toast: null,
  secret: null,
};

type Listener = () => void;
const listeners = new Set<Listener>();

export function setState(patch: Partial<AppState>): void {
  Object.assign(state, patch);
  for (const listener of listeners) listener();
}

export function useAppState(): AppState {
  const [, force] = useState(0);
  useEffect(() => {
    const listener = () => force((n) => n + 1);
    listeners.add(listener);
    return () => {
      listeners.delete(listener);
    };
  }, []);
  return state;
}

let toastTimer: number | undefined;

export function toast(text: string, kind: "info" | "error" = "info"): void {
  setState({ toast: { text, kind } });
  if (toastTimer !== undefined) window.clearTimeout(toastTimer);
  toastTimer = window.setTimeout(() => setState({ toast: null }), kind === "error" ? 6000 : 3500);
}

export function showSecret(title: string, value: string, hint: string): void {
  setState({ secret: { title, value, hint } });
}

export function closeSecret(): void {
  setState({ secret: null });
}

export function go(page: PageId): void {
  setState({ page });
}
