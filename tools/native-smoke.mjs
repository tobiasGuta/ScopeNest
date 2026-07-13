import { spawn } from "node:child_process";
import { mkdir } from "node:fs/promises";
import { resolve } from "node:path";

const executable = resolve(process.argv[2] || "bin/scopenest-host.exe");
const appData = resolve(".tools/native-smoke-data");
await mkdir(appData, { recursive: true });

const child = spawn(executable, [], { env: { ...process.env, APPDATA: appData }, stdio: ["pipe", "pipe", "inherit"] });
const request = Buffer.from(JSON.stringify({ version: 1, requestId: "smoke-ping", command: "ping" }));
const frame = Buffer.alloc(4 + request.length);
frame.writeUInt32LE(request.length, 0); request.copy(frame, 4);

const chunks = [];
child.stdout.on("data", (chunk) => chunks.push(chunk));
child.stdin.end(frame);
const exitCode = await new Promise((resolveExit, reject) => { child.once("error", reject); child.once("exit", resolveExit); });
if (exitCode !== 0) throw new Error(`native host exited with code ${exitCode}`);

const responseFrame = Buffer.concat(chunks);
if (responseFrame.length < 4) throw new Error("native host returned no framed response");
const length = responseFrame.readUInt32LE(0);
if (responseFrame.length !== length + 4) throw new Error("native host returned an invalid frame length");
const response = JSON.parse(responseFrame.subarray(4).toString("utf8"));
if (!response.success || response.requestId !== "smoke-ping" || response.command !== "ping" || response.data?.protocolVersion !== 1) {
  throw new Error(`unexpected native response: ${JSON.stringify(response)}`);
}
console.log(`Native messaging smoke test passed (host ${response.data.hostVersion}, protocol ${response.data.protocolVersion}).`);
