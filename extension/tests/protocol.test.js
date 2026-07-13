import test from "node:test";
import assert from "node:assert/strict";
import { COMMANDS, createRequest, parseResponse, PROTOCOL_VERSION, REQUEST_TIMEOUTS, timeoutForCommand } from "../src/shared/protocol.js";

test("constructs a versioned allowlisted request", () => {
  const request = createRequest("launch_container", { id: "abc", url: "https://example.com" }, "request-1");
  assert.deepEqual(request, { version: PROTOCOL_VERSION, requestId: "request-1", command: "launch_container", data: { id: "abc", url: "https://example.com" } });
});

test("rejects unknown commands before native messaging", () => {
  assert.throws(() => createRequest("run_anything", {}, "request-1"), /Unsupported command/);
});

test("uses command-specific native request timeouts", () => {
  for (const command of ["ping", "get_status", "list_containers", "get_running_containers", "validate_browser_path"]) {
    assert.equal(timeoutForCommand(command), REQUEST_TIMEOUTS.fast, command);
  }
  for (const command of ["create_container", "create_temporary_container", "update_container", "launch_container", "close_container"]) {
    assert.equal(timeoutForCommand(command), REQUEST_TIMEOUTS.standard, command);
  }
  for (const command of ["delete_container", "cleanup_temporary_containers"]) {
    assert.equal(timeoutForCommand(command), REQUEST_TIMEOUTS.destructive, command);
  }
  assert.equal(COMMANDS.every((command) => timeoutForCommand(command) > 0), true);
  assert.throws(() => timeoutForCommand("run_anything"), /Unsupported command/);
});

test("parses matching successful responses", () => {
  const request = createRequest("ping", undefined, "request-2");
  const data = parseResponse({ version: 1, success: true, requestId: "request-2", command: "ping", data: { hostVersion: "1.0.0" }, timestamp: new Date().toISOString() }, request);
  assert.equal(data.hostVersion, "1.0.0");
});

test("turns structured host failures into coded errors", () => {
  const request = createRequest("ping", undefined, "request-3");
  assert.throws(() => parseResponse({ version: 1, success: false, requestId: "request-3", command: "ping", error: { message: "host missing" }, errorCode: "NATIVE_HOST_UNAVAILABLE", timestamp: new Date().toISOString() }, request), (error) => error.code === "NATIVE_HOST_UNAVAILABLE");
});

test("rejects mismatched responses", () => {
  const request = createRequest("ping", undefined, "request-4");
  assert.throws(() => parseResponse({ version: 1, success: true, requestId: "other", command: "ping", timestamp: new Date().toISOString() }, request), /did not match/);
});
