#!/usr/bin/env node
"use strict";
// Automated end-to-end check against a running hub.
//
//   node test/env/e2e.js <self_id> <account_token> <bot_token> [wsBase]
//
// Verifies: event multicast QQ -> Bot, local action reply, and a relayed action
// round-trip (Bot -> QQ -> Bot). Exits 0 on success, 1 on failure.

const [, , selfId, accToken, botToken, wsArg] = process.argv;
if (!selfId || !accToken || !botToken) {
  console.error("usage: e2e.js <self_id> <account_token> <bot_token> [wsBase]");
  process.exit(2);
}
const base = wsArg || process.env.HUB_WS || "ws://127.0.0.1:8080";
const nowSec = () => Math.floor(Date.now() / 1000);

const results = { event: false, localReply: false, relayedReply: false };

const bot = new WebSocket(`${base}/onebot/v11/bot/ws/${encodeURIComponent(botToken)}`);
bot.onmessage = (m) => {
  let d;
  try { d = JSON.parse(m.data); } catch { return; }
  if (d.post_type === "message" && Number(d.group_id) === 123456) results.event = true;
  if (d.echo === "bot-1") results.localReply = true;
  if (d.echo === "bot-2") results.relayedReply = true;
};

bot.onopen = () => {
  setTimeout(() => bot.send(JSON.stringify({ action: "get_login_info", params: {}, echo: "bot-1" })), 300);
  setTimeout(() => bot.send(JSON.stringify({ action: "send_group_msg", params: { group_id: 123456, message: "hi" }, echo: "bot-2" })), 700);
  setTimeout(connectQQ, 400);
};

function connectQQ() {
  const qq = new WebSocket(`${base}/onebot/v11/ws?access_token=${encodeURIComponent(accToken)}`);
  qq.onopen = () => {
    setTimeout(() => qq.send(JSON.stringify({
      post_type: "message", message_type: "group", sub_type: "normal", time: nowSec(),
      self_id: Number(selfId), message_id: 1, group_id: 123456, user_id: 22222,
      raw_message: "hello from qq", message: "hello from qq",
    })), 200);
  };
  qq.onmessage = (m) => {
    let d;
    try { d = JSON.parse(m.data); } catch { return; }
    if (d.action) {
      qq.send(JSON.stringify({ status: "ok", retcode: 0, data: { message_id: 999 }, echo: d.echo }));
    }
  };
}

const deadline = Date.now() + 9000;
const timer = setInterval(() => {
  if (results.event && results.localReply && results.relayedReply) return finish(0);
  if (Date.now() > deadline) return finish(1);
}, 200);

function finish(code) {
  clearInterval(timer);
  console.log("e2e results:", JSON.stringify(results));
  try { bot.close(); } catch {}
  process.exit(code);
}
