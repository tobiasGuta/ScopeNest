export const PROTOCOL_VERSION = 1;
export const HOST_NAME = "com.scopenest.host";

export const COMMANDS = Object.freeze([
  "ping", "get_status", "list_containers", "create_container", "update_container",
  "launch_container", "close_container", "delete_container", "create_temporary_container",
  "cleanup_temporary_containers", "get_running_containers", "validate_browser_path",
]);

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
