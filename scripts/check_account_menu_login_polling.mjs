import assert from "node:assert/strict";
import fs from "node:fs";
import vm from "node:vm";

const source = fs.readFileSync(new URL("../ui/account-menu.js", import.meta.url), "utf8");
const name = "codexMuxReadLoginTerminal";
const start = source.indexOf(`async function ${name}`);
assert.notEqual(start, -1, `${name} is missing`);
const bodyStart = source.indexOf("{", start);
let depth = 0;
let extracted = "";
for (let index = bodyStart; index < source.length; index += 1) {
  if (source[index] === "{") depth += 1;
  if (source[index] === "}") depth -= 1;
  if (depth === 0) {
    extracted = source.slice(start, index + 1);
    break;
  }
}
const context = vm.createContext({ encodeURIComponent });
vm.runInContext(`${extracted}; this.readTerminal = ${name};`, context);
const seen = [];
const pending = await context.readTerminal(async (path) => {
  seen.push(path);
  return { attempt: { id: "attempt-1", state: "pending" } };
}, "attempt-1");
assert.equal(pending, null, "pending sign-in was treated as terminal");
const succeeded = { id: "attempt-1", state: "succeeded" };
assert.deepEqual(await context.readTerminal(async () => ({ attempt: succeeded }), "attempt-1"), succeeded);
assert.deepEqual(seen, ["/login-attempts/attempt-1"], "polling did not use the exact authenticated attempt endpoint");
assert.doesNotMatch(source, /EventSource|[?&]token=/, "account menu still places a bearer token in an event URL");

console.log("Account menu authenticated login polling contracts passed");
