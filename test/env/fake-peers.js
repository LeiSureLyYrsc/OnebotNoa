#!/usr/bin/env node
"use strict";
// Simulate OneBot V11 peers against a running OnebotNoa hub.
//
//   node test/env/fake-peers.js qq  <self_id> <account_token> [wsBase]
//   node test/env/fake-peers.js bot <bot_token> [wsBase]
//
// Default wsBase is ws://127.0.0.1:8080 (override with HUB_WS).

const [, , mode, ...rest] = process.argv;
const base = process.env.HUB_WS || "ws://127.0.0.1:8080";

const nowSec = () => Math.floor(Date.now() / 1000);
const log = (...a) => console.log(`[${mode}]`, ...a);

if (typeof WebSocket === "undefined") {
  console.error("This script needs Node 22+ (global WebSocket).");
  process.exit(2);
}

if (mode === "qq") {
  const selfId = rest[0];
  const token = rest[1];
  const b = rest[2] || base;
  if (!selfId || !token) {
    console.error("usage: fake-peers.js qq <self_id> <account_token> [wsBase]");
    process.exit(2);
  }
  const url = `${b}/onebot/v11/ws?access_token=${encodeURIComponent(token)}`;
  const ws = new WebSocket(url);
  ws.onopen = () => {
    log("connected", url);
    ws.send(JSON.stringify({ post_type: "meta_event", meta_event_type: "lifecycle", sub_type: "connect", time: nowSec(), self_id: Number(selfId) }));
    setInterval(() => ws.send(JSON.stringify({
      post_type: "meta_event", meta_event_type: "heartbeat", time: nowSec(),
      self_id: Number(selfId), status: { online: true, good: true }, interval: 5000,
    })), 5000);
    setTimeout(() => {
      ws.send(JSON.stringify({
        post_type: "message", message_type: "group", sub_type: "normal", time: nowSec(),
        self_id: Number(selfId), message_id: 1, group_id: 123456, user_id: 22222,
        raw_message: "hello from qq", message: "hello from qq",
      }));
      log("sent group message event");
    }, 1200);
  };
  ws.onmessage = (m) => {
    let data;
    try { data = JSON.parse(m.data); } catch { return log("recv(raw)", m.data); }
    log("recv", JSON.stringify(data));
    if (data.action) {
      ws.send(JSON.stringify({ status: "ok", retcode: 0, data: { message_id: 999 }, echo: data.echo }));
      log("replied to action", data.action);
    }
  };
  ws.onclose = () => { log("closed"); process.exit(0); };
  ws.onerror = (e) => log("error", e.message || String(e));
} else if (mode === "bot") {
  const token = rest[0];
  const b = rest[1] || base;
  if (!token) {
    console.error("usage: fake-peers.js bot <bot_token> [wsBase]");
    process.exit(2);
  }
  const url = `${b}/onebot/v11/bot/ws/${encodeURIComponent(token)}`;
  const ws = new WebSocket(url);
  ws.onopen = () => {
    log("connected", url);
    setTimeout(() => ws.send(JSON.stringify({ action: "get_login_info", params: {}, echo: "bot-1" })), 800);
    setTimeout(() => ws.send(JSON.stringify({ action: "send_group_msg", params: { group_id: 123456, message: "hi from bot" }, echo: "bot-2" })), 1800);
  };
  ws.onmessage = (m) => log("recv", m.data);
  ws.onclose = () => { log("closed"); process.exit(0); };
  ws.onerror = (e) => log("error", e.message || String(e));
} else {
  console.error("usage: fake-peers.js qq <self_id> <token> | bot <token>");
  process.exit(2);
}
