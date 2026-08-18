import assert from "node:assert/strict";
import fs from "node:fs";
import vm from "node:vm";

const source = fs.readFileSync(new URL("../ui/dashboard/app.js", import.meta.url), "utf8");

function extractFunction(name) {
  let start = source.indexOf(`function ${name}`);
  assert.notEqual(start, -1, `${name} is missing`);
  if (source.slice(Math.max(0, start - 6), start) === "async ") start -= 6;
  const bodyStart = source.indexOf("{", start);
  let depth = 0;
  for (let index = bodyStart; index < source.length; index += 1) {
    if (source[index] === "{") depth += 1;
    if (source[index] === "}") depth -= 1;
    if (depth === 0) return source.slice(start, index + 1);
  }
  throw new Error(`${name} is incomplete`);
}

const helper = extractFunction("requestFailureMessage");
const context = vm.createContext({});
vm.runInContext(`${helper}; this.message = requestFailureMessage;`, context);
const expired = "Dashboard access expired. Open FLOW again.";
assert.equal(context.message({ status: 401 }, "fallback"), expired);
assert.equal(context.message({ status: 500 }, "fallback"), "fallback");

const notices = [];
const updateContext = vm.createContext({
  api: async () => { throw Object.assign(new Error("unauthorized"), { status: 401 }); },
  announce: (...args) => notices.push(args),
  loadAccounts: async () => { throw new Error("must not refresh after failed update"); },
  render: () => { throw new Error("must not render after failed update"); },
  encodeURIComponent,
  JSON,
});
vm.runInContext(`${helper}; ${extractFunction("updateAccount")}; this.run = updateAccount;`, updateContext);
await updateContext.run("primary", { enabled: false });
assert.deepEqual(notices.pop(), [expired, true]);

let loginClosed = 0;
let loginRendered = 0;
const loginState = { pending: new Map([["primary", { id: "attempt-1" }]]) };
const watchContext = vm.createContext({
  state: loginState,
  api: async () => { throw Object.assign(new Error("unauthorized"), { status: 401 }); },
  announce: (...args) => notices.push(args),
  closeLoginDialog: () => { loginClosed += 1; },
  render: () => { loginRendered += 1; },
  encodeURIComponent,
});
vm.runInContext(`${helper}; ${extractFunction("watchLogin")}; this.run = watchLogin;`, watchContext);
await watchContext.run("primary", "attempt-1");
assert.equal(loginState.pending.has("primary"), false);
assert.equal(loginClosed, 1);
assert.equal(loginRendered, 1);
assert.deepEqual(notices.pop(), [expired, true]);

const migrationFile = { name: "account.json", size: 12, lastModified: 34 };
const migrationButton = { disabled: false };
const migrationState = {
  migrationInFlight: false,
  migrationReview: {
    signature: "account.json:12:34",
    items: [{ targetId: "primary", exported: {}, file: migrationFile }],
  },
};
const migrationContext = vm.createContext({
  state: migrationState,
  api: async () => { throw Object.assign(new Error("unauthorized"), { status: 401 }); },
  announce: (...args) => notices.push(args),
  loadAccounts: async () => {},
  encodeURIComponent,
  JSON,
  Array,
  $: (selector) => {
    if (selector === "#codex-lb-files") return { files: [migrationFile] };
    if (selector === "#codex-lb-paused") return { checked: true };
    if (selector === "#migrate-codex-lb") return migrationButton;
    throw new Error(`unexpected selector ${selector}`);
  },
});
vm.runInContext(`${helper}; ${extractFunction("migrationSignature")}; ${extractFunction("migrateCodexLB")}; this.run = migrateCodexLB;`, migrationContext);
await migrationContext.run();
assert.equal(migrationState.migrationInFlight, false);
assert.equal(migrationButton.disabled, false);
assert.deepEqual(notices.pop(), [expired, true, true]);

let finishingShown = 0;
let successClosed = 0;
let accountsLoaded = 0;
const successState = { pending: new Map([['primary', { id: 'attempt-success' }]]) };
const successContext = vm.createContext({
  state: successState,
  showLoginFinishing: () => { finishingShown += 1; },
  closeLoginDialog: () => { successClosed += 1; },
  loadAccounts: async () => { accountsLoaded += 1; },
  announce: (...args) => notices.push(args),
  render: () => {},
  loginFailureMessage: () => 'failed',
  window: { setTimeout },
  Promise,
});
vm.runInContext(`${extractFunction('finishLogin')}; this.run = finishLogin;`, successContext);
await successContext.run('primary', { id: 'attempt-success', state: 'succeeded' });
assert.equal(finishingShown, 1);
assert.equal(successClosed, 1);
assert.equal(accountsLoaded, 1);
assert.deepEqual(notices.pop(), ['Account connected']);

console.log("Dashboard session expiry contracts passed");
