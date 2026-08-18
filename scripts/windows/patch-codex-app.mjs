#!/usr/bin/env node

import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import { extractFile, getRawHeader, listPackage } from "@electron/asar";

const APPROVED_ASAR_SHA256 = new Set([
  "c7ac6d76cf5f30aa5cb92e1e46561933c06e94e3fe2d6582a04dac18c76f3ed1",
]);
const BUNDLE_PATTERN = /^\\webview\\assets\\app-initial-[A-Za-z0-9_-]+\.js$/;
const BOOTSTRAP_PATTERN = /^\\\.vite\\build\\bootstrap-[A-Za-z0-9_-]+\.js$/;
const START_ANCHOR = "function lcl(e){";
const END_ANCHOR = "function ucl(e){";

function fail(message) {
  throw new Error(`Codex Router app patch failed: ${message}`);
}

function sha256(data) {
  return crypto.createHash("sha256").update(data).digest("hex");
}

function integrity(data, blockSize) {
  const blocks = [];
  for (let offset = 0; offset < data.length; offset += blockSize) {
    blocks.push(sha256(data.subarray(offset, Math.min(offset + blockSize, data.length))));
  }
  if (blocks.length === 0) blocks.push(sha256(data));
  return { algorithm: "SHA256", hash: sha256(data), blockSize, blocks };
}

function recordFor(header, archiveName) {
  let current = header;
  for (const segment of archiveName.replace(/^\\/, "").split("\\")) {
    current = current?.files?.[segment];
    if (!current) fail(`ASAR entry is missing: ${archiveName}`);
  }
  return current;
}

function routerMenuFunction(originalLength) {
  const code = "function lcl(e){let t=Fo(Q);return(0,e7.jsx)(pH,{LeftIcon:Msl,leftIconClassName:`icon-xs`,onClick:e=>{t.set(Zsl,!1),ES({event:e,href:`codex-router://connect`,initiator:`open_in_browser_bridge`,openTarget:`external-browser`})},children:(0,e7.jsx)(Z,{id:`codex.router.addAccount`,defaultMessage:`Add account`,description:`Connect another OpenAI account through Codex Router`})})}";
  const codeLength = Buffer.byteLength(code);
  if (codeLength > originalLength) fail("the minimal account item no longer fits its approved source seam");
  return code + " ".repeat(originalLength - codeLength);
}

function parseArgs() {
  const args = process.argv.slice(2);
  const asarIndex = args.indexOf("--asar");
  if (asarIndex < 0 || !args[asarIndex + 1]) fail("usage: patch-codex-app.mjs --asar C:\\path\\to\\app.asar");
  return path.resolve(args[asarIndex + 1]);
}

const asarPath = parseArgs();
if (!fs.statSync(asarPath, { throwIfNoEntry: false })?.isFile()) fail(`ASAR not found: ${asarPath}`);
if (/\\WindowsApps\\/iu.test(asarPath)) fail("the protected official Windows package is never patched in place");

const beforeHash = sha256(fs.readFileSync(asarPath));
if (!APPROVED_ASAR_SHA256.has(beforeHash)) fail(`unapproved official app.asar SHA-256 ${beforeHash}`);

const bundles = listPackage(asarPath).filter((entry) => BUNDLE_PATTERN.test(entry));
if (bundles.length !== 1) fail(`expected one app-initial renderer bundle, found ${bundles.length}`);
const bundleName = bundles[0];
const source = extractFile(asarPath, bundleName.slice(1)).toString("utf8");
const start = source.indexOf(START_ANCHOR);
const end = source.indexOf(END_ANCHOR, start + START_ANCHOR.length);
if (start < 0 || end < 0 || source.indexOf(START_ANCHOR, start + 1) >= 0) fail("native profile-menu seam changed");
const originalFunction = source.slice(start, end);
if (!originalFunction.includes("defaultMessage:`Invite a friend`") || !originalFunction.includes("onClick:h")) {
  fail("native referral item no longer matches the reviewed replacement seam");
}
const patchedSource = source.slice(0, start) + routerMenuFunction(Buffer.byteLength(originalFunction)) + source.slice(end);
if (Buffer.byteLength(patchedSource) !== Buffer.byteLength(source)) fail("renderer patch changed the ASAR entry size");
if ((patchedSource.match(/id:`codex\.router\.addAccount`/g) ?? []).length !== 1 ||
    (patchedSource.match(/href:`codex-router:\/\/connect`/g) ?? []).length !== 1) {
  fail("account item patch was not unique");
}

const bootstraps = listPackage(asarPath).filter((entry) => BOOTSTRAP_PATTERN.test(entry));
if (bootstraps.length !== 1) fail(`expected one desktop bootstrap bundle, found ${bootstraps.length}`);
const bootstrapName = bootstraps[0];
const bootstrapSource = extractFile(asarPath, bootstrapName.slice(1)).toString("utf8");
const profileStart = bootstrapSource.indexOf("function ee({appDataPath:e,buildFlavor:n,env:r}){");
const profileEnd = bootstrapSource.indexOf("var T=", profileStart);
if (profileStart < 0 || profileEnd < 0 || bootstrapSource.indexOf("function ee({appDataPath:e,buildFlavor:n,env:r}){", profileStart + 1) >= 0) {
  fail("desktop profile-isolation seam changed");
}
const originalProfileFunction = bootstrapSource.slice(profileStart, profileEnd);
const isolatedProfileFunction = "function ee({appDataPath:e}){return(0,o.join)(process.env.LOCALAPPDATA||e,`Codex Subscription Router`,`app-user-data`)}";
if (Buffer.byteLength(isolatedProfileFunction) > Buffer.byteLength(originalProfileFunction)) fail("isolated profile patch no longer fits its approved source seam");
const paddedProfileFunction = isolatedProfileFunction + " ".repeat(Buffer.byteLength(originalProfileFunction) - Buffer.byteLength(isolatedProfileFunction));
const patchedBootstrap = bootstrapSource.slice(0, profileStart) + paddedProfileFunction + bootstrapSource.slice(profileEnd);

const raw = getRawHeader(asarPath);
const patches = [
  { name: bundleName, original: source, patched: patchedSource, label: "renderer" },
  { name: bootstrapName, original: bootstrapSource, patched: patchedBootstrap, label: "bootstrap" },
];
for (const patch of patches) {
  const record = recordFor(raw.header, patch.name);
  const patchedBuffer = Buffer.from(patch.patched, "utf8");
  if (record.unpacked || typeof record.offset !== "string" || record.size !== Buffer.byteLength(patch.original) || patchedBuffer.length !== record.size) {
    fail(`${patch.label} bundle storage metadata changed`);
  }
  const blockSize = record.integrity?.blockSize;
  if (!Number.isSafeInteger(blockSize) || blockSize <= 0) fail(`${patch.label} integrity metadata is missing`);
  record.integrity = integrity(patchedBuffer, blockSize);
  patch.record = record;
  patch.buffer = patchedBuffer;
}
const nextHeaderString = JSON.stringify(raw.header);
if (Buffer.byteLength(nextHeaderString) !== Buffer.byteLength(raw.headerString)) {
  fail("patched ASAR header changed size");
}

const archive = fs.openSync(asarPath, "r+");
try {
  const headerBuffer = Buffer.alloc(raw.headerSize);
  if (fs.readSync(archive, headerBuffer, 0, raw.headerSize, 8) !== raw.headerSize) fail("could not read ASAR header");
  const oldHeaderBytes = Buffer.from(raw.headerString, "utf8");
  const headerStringOffset = headerBuffer.indexOf(oldHeaderBytes);
  if (headerStringOffset < 0 || headerBuffer.indexOf(oldHeaderBytes, headerStringOffset + 1) >= 0) fail("ASAR header string was not unique");
  Buffer.from(nextHeaderString, "utf8").copy(headerBuffer, headerStringOffset);
  fs.writeSync(archive, headerBuffer, 0, headerBuffer.length, 8);
  for (const patch of patches) {
    const dataOffset = 8 + raw.headerSize + Number.parseInt(patch.record.offset, 10);
    fs.writeSync(archive, patch.buffer, 0, patch.buffer.length, dataOffset);
  }
  fs.fsyncSync(archive);
} finally {
  fs.closeSync(archive);
}

const verified = extractFile(asarPath, bundleName.slice(1)).toString("utf8");
if (!verified.includes("id:`codex.router.addAccount`") || !verified.includes("href:`codex-router://connect`") || verified.includes("defaultMessage:`Invite a friend`")) {
  fail("post-write renderer verification failed");
}
const verifiedBootstrap = extractFile(asarPath, bootstrapName.slice(1)).toString("utf8");
if (!verifiedBootstrap.includes("`Codex Subscription Router`,`app-user-data`")) fail("post-write profile-isolation verification failed");
console.log(JSON.stringify({
  patched: true,
  sourceAsarSha256: beforeHash,
  patchedAsarSha256: sha256(fs.readFileSync(asarPath)),
  bundle: bundleName,
  bootstrap: bootstrapName,
  isolatedProfile: true,
  menuItem: "Add account",
  protocol: "codex-router://connect",
}));
