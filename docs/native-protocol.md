# Native messaging protocol

ScopeNest protocol version 1 uses Chrome's Native Messaging framing: a four-byte unsigned little-endian payload length followed by one UTF-8 JSON object. The maximum encoded message is 1 MiB. Standard output contains framed responses only.

## Request

```json
{
  "version": 1,
  "requestId": "a caller-generated correlation ID",
  "command": "launch_container",
  "data": {
    "id": "32 lowercase hexadecimal characters",
    "url": "https://example.com/"
  }
}
```

`requestId` must contain 1–128 characters. Every object is decoded strictly: unknown properties, incorrect JSON types, multiple values, and version mismatches are rejected.

## Response

```json
{
  "version": 1,
  "success": false,
  "requestId": "the matching request ID",
  "command": "launch_container",
  "error": { "message": "container is already running" },
  "errorCode": "ALREADY_RUNNING",
  "timestamp": "2026-07-13T16:00:00Z"
}
```

Successful responses use `data` instead of `error`/`errorCode`. Responses are never ambiguous plain text.

## Commands

| Command | Data | Result |
|---|---|---|
| `ping` | omitted or `{}` | host/protocol versions |
| `get_status` | omitted or `{}` | versions, data directory, count, detected browsers |
| `list_containers` | omitted or `{}` | all reconciled container metadata |
| `get_running_containers` | omitted or `{}` | running subset |
| `create_container` | name, color, icon, browser type/executable | created container |
| `create_temporary_container` | same as create | created temporary container |
| `update_container` | ID plus editable create fields | updated container |
| `launch_container` | ID and optional HTTP(S) URL | launched container/process metadata |
| `close_container` | ID | close requested for a process owned by this host instance |
| `delete_container` | ID | deletion result; running containers are rejected |
| `cleanup_temporary_containers` | omitted or `{}` | cleaned and pending ID lists |
| `validate_browser_path` | path | normalized valid path |

Representative error codes include `INVALID_REQUEST`, `UNSUPPORTED_VERSION`, `INVALID_REQUEST_ID`, `UNKNOWN_COMMAND`, `INVALID_DATA`, `INVALID_CONTAINER_ID`, `INVALID_NAME`, `INVALID_COLOR`, `INVALID_ICON`, `INVALID_BROWSER`, `INVALID_BROWSER_PATH`, `INVALID_URL`, `NOT_FOUND`, `ALREADY_RUNNING`, `CONTAINER_RUNNING`, `PROFILE_IN_USE`, `PROCESS_NOT_OWNED`, `LAUNCH_FAILED`, `CLOSE_FAILED`, `DELETE_FAILED`, `MESSAGE_TOO_LARGE`, and `INTERNAL_ERROR`.

Changing a request or response shape requires a new protocol version or a backward-compatible optional field. Extension and native-host command allowlists must remain synchronized.
