package mcpserver

import (
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/scopenest/scopenest/native-host/internal/protocol"
)

type errorOutput struct {
	Success   bool   `json:"success"`
	Command   string `json:"command"`
	ErrorCode string `json:"errorCode"`
	Message   string `json:"message"`
}

var safeErrorMessages = map[string]string{
	"INVALID_ARGUMENT":                      "The MCP tool arguments are invalid.",
	"INVALID_DATA":                          "The ScopeNest command data is invalid.",
	"INVALID_NAME":                          "The container name is invalid.",
	"INVALID_COLOR":                         "The container color is invalid.",
	"INVALID_ICON":                          "The container icon is invalid.",
	"INVALID_BROWSER":                       "The selected browser type is invalid.",
	"INVALID_BROWSER_PATH":                  "The selected browser executable is invalid.",
	"AUTOMATION_REQUIRES_STANDARD_BROWSER":  "Automation requires a detected standard Chromium-family browser.",
	"AUTOMATION_INITIALIZATION_FAILED":      "The browser automation endpoint could not be initialized safely.",
	"AUTOMATION_DISABLED":                   "Browser automation is not enabled for this container.",
	"AUTOMATION_NOT_RUNNING":                "The automation-enabled container is not running.",
	"AUTOMATION_NOT_READY":                  "The owned browser automation endpoint is not ready.",
	"AUTOMATION_UNAVAILABLE":                "Browser automation is unavailable in this ScopeNest host.",
	"AUTOMATION_ACTION_FAILED":              "The bounded browser action could not be completed.",
	"INVALID_PAGE_ID":                       "The opaque page ID is invalid.",
	"PAGE_NOT_FOUND":                        "The page does not belong to this container runtime.",
	"INVALID_SELECTOR":                      "The CSS selector is invalid or exceeds the limit.",
	"INVALID_TEXT":                          "The text is invalid or exceeds the limit.",
	"INVALID_KEY":                           "The requested key is not supported.",
	"INVALID_NETWORK_MODE":                  "The network mode must be direct, proxy, or template.",
	"INVALID_CONTAINER_ID":                  "The container ID is invalid.",
	"INVALID_PROXY_PROFILE_ID":              "The proxy profile ID is invalid.",
	"INVALID_TEMPLATE_ID":                   "The environment template ID is invalid.",
	"INVALID_URL":                           "The URL must be an absolute HTTP(S) URL without credentials.",
	"NOT_FOUND":                             "The requested container was not found.",
	"CONTAINER_NAME_MISMATCH":               "The expected container name does not match the current container name; no action was taken.",
	"CUSTOM_BROWSER_REQUIRES_HUMAN_LAUNCH":  "Custom-browser containers must be launched by a human through the ScopeNest extension.",
	"PROCESS_NOT_OWNED":                     "This MCP server process does not own the container process. Close its browser window or use the ScopeNest process that launched it.",
	"ALREADY_RUNNING":                       "The container is already running.",
	"ALREADY_LAUNCHING":                     "The container launch is already in progress.",
	"PROFILE_IN_USE":                        "The container profile is already in use.",
	"PROXY_LISTENER_UNAVAILABLE":            "The configured proxy listener is unavailable.",
	"PROXY_PROFILE_NOT_FOUND":               "The configured proxy profile was not found.",
	"PROXY_PROFILE_DISABLED":                "The configured proxy profile is disabled.",
	"ENVIRONMENT_TEMPLATE_NOT_FOUND":        "The configured environment template was not found.",
	"INVALID_NETWORK_CONFIGURATION":         "The container network configuration is invalid.",
	"CERTIFICATE_NOT_FOUND":                 "A required certificate was not found.",
	"CERTIFICATE_NOT_READY":                 "A required certificate is not ready for launch.",
	"CERTIFICATE_DER_MISSING_OR_MISMATCHED": "Required certificate data is missing or does not match its metadata.",
	"CERTIFICATE_TRUST_VERIFICATION_FAILED": "Required certificate trust could not be verified.",
	"LAUNCH_FAILED":                         "The browser could not be started.",
	"CLOSE_FAILED":                          "The owned browser process could not be closed.",
	"LAUNCH_RESERVATION_LOST":               "The container launch reservation is no longer valid.",
	"INTERNAL_ERROR":                        "ScopeNest could not complete the request.",
	"UNKNOWN_COMMAND":                       "The requested ScopeNest operation is not available through MCP.",
}

func toolError(command, code string) *mcp.CallToolResult {
	message := safeErrorMessages[code]
	if message == "" {
		message = "ScopeNest rejected the request."
	}
	output := errorOutput{Success: false, Command: command, ErrorCode: code, Message: message}
	raw, _ := json.Marshal(output)
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(raw)}},
		StructuredContent: output,
		IsError:           true,
	}
}

func responseError(response protocol.Response) *mcp.CallToolResult {
	return toolError(response.Command, response.ErrorCode)
}
