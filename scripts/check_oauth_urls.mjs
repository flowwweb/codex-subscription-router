import assert from "node:assert/strict";
import fs from "node:fs";
import vm from "node:vm";

const functionName = "codexMuxTrustedBrowserLoginURL";
const valid = "https://auth.openai.com/oauth/authorize?response_type=code&code_challenge=challenge&code_challenge_method=S256&state=state&redirect_uri=http%3A%2F%2Flocalhost%3A1455%2Fauth%2Fcallback";
const invalid = [
  "https://auth.openai.com/oauth/authorize",
  valid.replace("/oauth/authorize", "/not-authorize"),
  valid.replace("auth.openai.com", "chatgpt.com"),
  valid.replace("http%3A%2F%2Flocalhost%3A1455%2Fauth%2Fcallback", "https%3A%2F%2Fattacker.example%2Fcallback"),
  valid.replace("%2Fauth%2Fcallback", "%2Fwrong"),
  `${valid}&redirect_uri=http%3A%2F%2Flocalhost%3A2455%2Fauth%2Fcallback`,
  valid.replace("%2Fauth%2Fcallback", "%2F%2561uth%2Fcallback"),
  `${valid}&bad=%ZZ`,
  `${valid}#fragment`,
];

function extractFunction(source) {
  const start = source.indexOf(`function ${functionName}`);
  assert.notEqual(start, -1, `${functionName} is missing`);
  const bodyStart = source.indexOf("{", start);
  let depth = 0;
  for (let index = bodyStart; index < source.length; index += 1) {
    if (source[index] === "{") depth += 1;
    if (source[index] === "}") depth -= 1;
    if (depth === 0) return source.slice(start, index + 1);
  }
  throw new Error(`${functionName} is incomplete`);
}

for (const relative of ["ui/account-menu.js", "ui/dashboard/app.js"]) {
  const source = fs.readFileSync(new URL(`../${relative}`, import.meta.url), "utf8");
  const context = vm.createContext({ URL });
  vm.runInContext(`${extractFunction(source)}; this.validate = ${functionName};`, context);
  assert.equal(context.validate(valid), valid, `${relative} rejected the canonical browser OAuth URL`);
  for (const candidate of invalid) {
    assert.equal(context.validate(candidate), "", `${relative} accepted unsafe OAuth URL: ${candidate}`);
  }
}

console.log("Browser OAuth URL contracts passed");
