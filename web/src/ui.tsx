import { ComponentChildren } from "preact";
import { useEffect, useState } from "preact/hooks";

export function Window(props: { title: string; children: ComponentChildren; width?: number | string }) {
  return (
    <div class="window" style={props.width ? { width: typeof props.width === "number" ? `${props.width}px` : props.width } : undefined}>
      <div class="title-bar">
        <div class="title-bar-text">{props.title}</div>
        <div class="title-bar-controls">
          <button aria-label="Minimize" />
          <button aria-label="Maximize" />
          <button aria-label="Close" />
        </div>
      </div>
      <div class="window-body">{props.children}</div>
    </div>
  );
}

export function StatusDot(props: { status: string }) {
  const cls = props.status === "online" ? "online" : props.status === "degraded" ? "degraded" : "offline";
  return <span class={`dot ${cls}`} title={props.status} />;
}

export function Field(props: { label: string; children: ComponentChildren }) {
  return (
    <div class="field-row">
      <label>{props.label}</label>
      <div style={{ marginTop: "2px" }}>{props.children}</div>
    </div>
  );
}

export function CopyButton(props: { text: string; label?: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      onClick={() => {
        navigator.clipboard?.writeText(props.text).then(
          () => {
            setCopied(true);
            setTimeout(() => setCopied(false), 1200);
          },
          () => undefined,
        );
      }}
    >
      {copied ? "已复制" : props.label || "复制"}
    </button>
  );
}

export function Modal(props: { title: string; onClose: () => void; children: ComponentChildren }) {
  return (
    <div
      style={{
        position: "fixed",
        inset: "0",
        background: "rgba(0,0,0,0.35)",
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        zIndex: 20,
      }}
    >
      <Window title={props.title} width={560}>
        <div class="stack">{props.children}</div>
        <div style={{ textAlign: "right", marginTop: "8px" }}>
          <button onClick={props.onClose}>关闭</button>
        </div>
      </Window>
    </div>
  );
}

export function useInterval(fn: () => void, ms: number) {
  useEffect(() => {
    fn();
    const id = window.setInterval(fn, ms);
    return () => window.clearInterval(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ms]);
}

export function fmtTime(unixSeconds: number): string {
  if (!unixSeconds) return "-";
  return new Date(unixSeconds * 1000).toLocaleString();
}
