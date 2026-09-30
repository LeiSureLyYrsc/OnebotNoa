// Shared XP-styled building blocks.
import type { ComponentChildren } from "preact";
import { state, setState } from "./state";
import { formatTime } from "./api";

export function Window(props: { title: string; children: ComponentChildren; footer?: ComponentChildren; tools?: ComponentChildren }) {
  return (
    <div class="window hub-window">
      <div class="title-bar">
        <div class="title-bar-text">{props.title}</div>
        <div class="title-bar-controls">
          <button aria-label="Minimize" />
          <button aria-label="Maximize" />
          <button aria-label="Close" />
        </div>
      </div>
      <div class="window-body">
        {props.tools ? <div class="hub-toolbar">{props.tools}</div> : null}
        {props.children}
      </div>
      {props.footer ? <div class="status-bar">{props.footer}</div> : null}
    </div>
  );
}

export function Field(props: { label: string; children: ComponentChildren; hint?: string }) {
  return (
    <label class="hub-field">
      <span class="hub-field-label">{props.label}</span>
      <span class="hub-field-control">{props.children}</span>
      {props.hint ? <span class="hub-muted hub-field-hint">{props.hint}</span> : null}
    </label>
  );
}

export function StateDot(props: { state: string; text: string }) {
  return <span class={"hub-dot hub-dot--" + props.state}>{props.text}</span>;
}

export function Modal(props: { title: string; onClose: () => void; children: ComponentChildren; wide?: boolean }) {
  return (
    <div class="hub-modal-backdrop" onClick={props.onClose}>
      <div class={"window hub-modal" + (props.wide ? " hub-modal--wide" : "")} onClick={(e) => e.stopPropagation()}>
        <div class="title-bar">
          <div class="title-bar-text">{props.title}</div>
          <div class="title-bar-controls">
            <button aria-label="Close" onClick={props.onClose} />
          </div>
        </div>
        <div class="window-body">{props.children}</div>
      </div>
    </div>
  );
}

export function SecretDialog() {
  const secret = state.secret;
  if (!secret) return null;
  return (
    <Modal title={secret.title} onClose={() => setState({ secret: null })}>
      <p class="hub-warn">这段内容只显示一次，请立刻复制保存；服务端只保存哈希，无法再次查看。</p>
      <textarea class="hub-secret" readOnly rows={2} value={secret.value} onFocus={(e) => (e.target as HTMLTextAreaElement).select()} />
      <p class="hub-muted">{secret.hint}</p>
      <div class="hub-row hub-row--end">
        <button onClick={() => copy(secret.value)}>复制</button>
        <button onClick={() => setState({ secret: null })}>关闭</button>
      </div>
    </Modal>
  );
}

export function Toast() {
  const toast = state.toast;
  if (!toast) return null;
  return <div class={"hub-toast hub-toast--" + toast.kind}>{toast.text}</div>;
}

export function copy(text: string): void {
  void navigator.clipboard?.writeText(text).catch(() => undefined);
}

export function JsonEditor(props: {
  value: string;
  onChange: (next: string) => void;
  rows?: number;
  placeholder?: string;
}) {
  const valid = isValidJSON(props.value);
  return (
    <div class="hub-json">
      <textarea
        rows={props.rows ?? 5}
        spellcheck={false}
        placeholder={props.placeholder ?? "{}"}
        value={props.value}
        onInput={(e) => props.onChange((e.target as HTMLTextAreaElement).value)}
      />
      <span class={valid ? "hub-muted" : "hub-error"}>{valid ? "JSON 合法" : "JSON 非法"}</span>
    </div>
  );
}

export function isValidJSON(text: string): boolean {
  if (text.trim() === "") return true;
  try {
    JSON.parse(text);
    return true;
  } catch {
    return false;
  }
}

export function Empty(props: { text: string }) {
  return <p class="hub-muted hub-empty">{props.text}</p>;
}

export function TimeCell(props: { iso?: string }) {
  return <span class="hub-mono">{formatTime(props.iso)}</span>;
}
