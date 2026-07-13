export const PROTOCOL_VERSION = 1;
export const HOST_NAME = "com.scopenest.host";

export const COMMANDS = Object.freeze([
  "ping", "get_status", "list_containers", "create_container", "update_container",
  "launch_container", "close_container", "delete_container", "create_temporary_container",
  "cleanup_temporary_containers", "get_running_containers", "validate_browser_path",
]);

export const REQUEST_TIMEOUTS = Object.freeze({
  fast: 15_000,
  standard: 30_000,
  destructive: 300_000,
});

const COMMAND_TIMEOUTS = Object.freeze({
  ping: REQUEST_TIMEOUTS.fast,
  get_status: REQUEST_TIMEOUTS.fast,
  list_containers: REQUEST_TIMEOUTS.fast,
  get_running_containers: REQUEST_TIMEOUTS.fast,
  validate_browser_path: REQUEST_TIMEOUTS.fast,
  create_container: REQUEST_TIMEOUTS.standard,
  create_temporary_container: REQUEST_TIMEOUTS.standard,
  update_container: REQUEST_TIMEOUTS.standard,
  launch_container: REQUEST_TIMEOUTS.standard,
  close_container: REQUEST_TIMEOUTS.standard,
  delete_container: REQUEST_TIMEOUTS.destructive,
  cleanup_temporary_containers: REQUEST_TIMEOUTS.destructive,
});

export function timeoutForCommand(command) {
  const timeout = COMMAND_TIMEOUTS[command];
  if (!COMMANDS.includes(command) || !Number.isSafeInteger(timeout)) throw new Error(`Unsupported command: ${command}`);
  return timeout;
}

export function createRequest(command, data = undefined, requestId = crypto.randomUUID()) {
  if (!COMMANDS.includes(command)) throw new Error(`Unsupported command: ${command}`);
  if (typeof requestId !== "string" || !requestId || requestId.length > 128) throw new Error("Invalid request ID");
  const request = { version: PROTOCOL_VERSION, requestId, command };
  if (data !== undefined) request.data = data;
  return request;
}

export function parseResponse(response, expectedRequest) {
  if (!response || typeof response !== "object" || Array.isArray(response)) throw new Error("Malformed native-host response");
  if (response.version !== PROTOCOL_VERSION) throw new Error("Unsupported native-host protocol version");
  if (response.requestId !== expectedRequest.requestId || response.command !== expectedRequest.command) throw new Error("Native-host response did not match the request");
  if (typeof response.success !== "boolean" || typeof response.timestamp !== "string") throw new Error("Incomplete native-host response");
  if (!response.success) {
    const error = new Error(response.error?.message || "Native host request failed");
    error.code = response.errorCode || "NATIVE_ERROR";
    throw error;
  }
  return response.data;
}
