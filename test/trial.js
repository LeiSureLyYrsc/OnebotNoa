#!/usr/bin/env node
"use strict";
// One-click local trial environment.
//
//   node test/trial.js [--port 8080] [--account 10001] [--bot demo-bot]
//
// Builds the WebUI + binary, starts the hub, provisions a sample account, bot
// and binding, then prints the WebUI URL, credentials and peer tokens.
// Press Ctrl+C to stop the hub.

const { spawn, execSync } = require("node:child_process");
const fs = require("node:fs");
const path = require("node:path");

const root = path.resolve(__dirname, "..");
const dataDir = path.join(root, "test", "data");
fs.mkdirSync(dataDir, { recursive: true });

function arg(name, def) {
  const i = process.argv.indexOf(`--${name}`);
  return i >= 0 && process.argv[i + 1] ? process.argv[i + 1] : def;
}
const port = Number(arg("port", "8080"));
const account = arg("account", "10001");
const botName = arg("bot", "demo-bot");
const adminPassword = arg("password", "admin");
const base = `http://127.0.0.1:${port}`;

function run(cmd, opts) {
  console.log(`> ${cmd}`);
  execSync(cmd, { cwd: root, stdio: "inherit", shell: true, ...opts });
}

console.log("== building WebUI ==");
run("npm run build", { cwd: path.join(root, "web") });

console.log("== building binary ==");
run('go build -trimpath -ldflags "-s -w -X main.version=dev" -o test/bin/onebotnoa.exe ./cmd/onebotnoa', {
  env: { ...process.env, CGO_ENABLED: "0" },
});

for (const f of ["onebotnoa.db", "onebotnoa.db-wal", "onebotnoa.db-shm"]) {
  try { fs.rmSync(path.join(dataDir, f)); } catch {}
}

const exe = path.join(root, "test", "bin", "onebotnoa.exe");
const env = {
  ...process.env,
  ONEBOTNOA_LISTEN: `127.0.0.1:${port}`,
  ONEBOTNOA_SQLITE: path.join(dataDir, "onebotnoa.db"),
  ONEBOTNOA_CONNECT_FILE: path.join(dataDir, "connect.json"),
  ONEBOTNOA_CONNECT_KEY: path.join(dataDir, "connect.key"),
  ONEBOTNOA_ADMIN_PASSWORD: adminPassword,
  ONEBOTNOA_LOG_LEVEL: "info",
  ONEBOTNOA_LOG_FORMAT: "text",
};

console.log("== starting hub ==");
const hub = spawn(exe, ["serve", "-config", path.join(dataDir, "config.yaml")], {
  env,
  stdio: ["ignore", "pipe", "pipe"],
});
hub.stdout.on("data", (d) => process.stdout.write("[hub] " + d));
hub.stderr.on("data", (d) => process.stderr.write("[hub] " + d));
hub.on("exit", (code) => {
  console.log(`[hub] exited with code ${code}`);
  process.exit(code ?? 0);
});

let cookie = "";
async function api(pathname, init = {}) {
  const res = await fetch(base + pathname, {
    ...init,
    headers: { "Content-Type": "application/json", ...(cookie ? { cookie } : {}), ...(init.headers || {}) },
  });
  const setCookie = res.headers.getSetCookie ? res.headers.getSetCookie() : [];
  if (setCookie.length) cookie = setCookie.map((c) => c.split(";")[0]).join("; ");
  const text = await res.text();
  const body = text ? JSON.parse(text) : null;
  if (!res.ok) throw new Error((body && body.error) || res.statusText);
  return body;
}

async function waitReady() {
  for (let i = 0; i < 40; i++) {
    try {
      await fetch(base + "/healthz");
      return;
    } catch {
      await new Promise((r) => setTimeout(r, 300));
    }
  }
  throw new Error("hub did not become ready");
}

(async () => {
  await waitReady();
  await api("/api/v1/login", { method: "POST", body: JSON.stringify({ username: "admin", password: adminPassword }) });
  const acc = await api("/api/v1/accounts", { method: "POST", body: JSON.stringify({ self_id: account, name: "demo" }) });
  const bot = await api("/api/v1/bots", { method: "POST", body: JSON.stringify({ name: botName }) });
  await api("/api/v1/bindings", {
    method: "POST",
    body: JSON.stringify({ bot_id: bot.bot.id, account_id: acc.account.id, priority: 100, is_default: true }),
  });

  const line = "=".repeat(66);
  console.log(`\n${line}`);
  console.log("OnebotNoa trial environment ready");
  console.log(`WebUI:      ${base}`);
  console.log(`Login:      admin / ${adminPassword}`);
  console.log(`QQ account: ${account}   token: ${acc.token}`);
  console.log(`Bot:        ${botName}   token: ${bot.token}`);
  console.log("\nOpen two more terminals to simulate both ends:");
  console.log(`  node test/env/fake-peers.js qq  ${account} ${acc.token}`);
  console.log(`  node test/env/fake-peers.js bot ${bot.token}`);
  console.log("\nPress Ctrl+C to stop the hub.");
  console.log(line);
})().catch((err) => {
  console.error("trial failed:", err.message || err);
  hub.kill();
  process.exit(1);
});

function shutdown() {
  console.log("\nstopping hub...");
  hub.kill();
  process.exit(0);
}
process.on("SIGINT", shutdown);
process.on("SIGTERM", shutdown);
