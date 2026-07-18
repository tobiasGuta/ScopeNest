package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/scopenest/scopenest/native-host/internal/protocol"
	"github.com/scopenest/scopenest/native-host/internal/security"
)

type emptyInput struct{}

type idInput struct {
	ID string `json:"id"`
}

type createContainerInput struct {
	Name                  string `json:"name"`
	Color                 string `json:"color"`
	Icon                  string `json:"icon,omitempty"`
	BrowserType           string `json:"browserType"`
	BrowserExecutable     string `json:"browserExecutable,omitempty"`
	NetworkMode           string `json:"networkMode"`
	ProxyProfileID        string `json:"proxyProfileId,omitempty"`
	EnvironmentTemplateID string `json:"environmentTemplateId,omitempty"`
}

type launchContainerInput struct {
	ID           string `json:"id"`
	ExpectedName string `json:"expectedName"`
	URL          string `json:"url,omitempty"`
}

type closeContainerInput struct {
	ID           string `json:"id"`
	ExpectedName string `json:"expectedName"`
}

type toolValidationError struct{ code string }

func (e toolValidationError) Error() string { return e.code }

func registerTools(server *mcp.Server, adapter *Adapter) {
	readOnly := annotations(true, false, true)
	mutating := annotations(false, false, false)
	processControl := annotations(false, true, false)

	addTool(server, toolSpec{
		name: "scopenest_ping", command: "ping",
		description: "Verify the local ScopeNest MCP server and core host. Returns versions, platform, protocol version, and startup-cleanup state without local paths.",
		schema:      emptySchema(), annotations: readOnly,
	}, func(emptyInput) protocol.Response { return adapter.Execute("ping", struct{}{}) }, validateEmpty)

	addTool(server, toolSpec{
		name: "scopenest_get_status", command: "get_status",
		description: "Return a sanitized local ScopeNest health summary: browser types, container/running counts, cleanup state, broken-reference counts, and trust capabilities.",
		schema:      emptySchema(), annotations: readOnly,
	}, func(emptyInput) protocol.Response { return adapter.Execute("get_status", struct{}{}) }, validateEmpty)

	addTool(server, toolSpec{
		name: "scopenest_list_containers", command: "list_containers",
		description: "List saved and temporary ScopeNest containers using sanitized metadata only; profile paths, browser paths, PIDs, and launch tokens are never returned.",
		schema:      emptySchema(), annotations: readOnly,
	}, func(emptyInput) protocol.Response { return adapter.Execute("list_containers", struct{}{}) }, validateEmpty)

	addTool(server, toolSpec{
		name: "scopenest_list_running_containers", command: "get_running_containers",
		description: "List the sanitized subset of ScopeNest containers currently reported as running.",
		schema:      emptySchema(), annotations: readOnly,
	}, func(emptyInput) protocol.Response { return adapter.Execute("get_running_containers", struct{}{}) }, validateEmpty)

	addTool(server, toolSpec{
		name: "scopenest_list_proxy_profiles", command: "list_proxy_profiles",
		description: "List existing non-secret loopback proxy-profile metadata for container selection. This tool cannot create or change proxies.",
		schema:      emptySchema(), annotations: readOnly,
	}, func(emptyInput) protocol.Response { return adapter.Execute("list_proxy_profiles", struct{}{}) }, validateEmpty)

	addTool(server, toolSpec{
		name: "scopenest_list_environment_templates", command: "list_environment_templates",
		description: "List existing ScopeNest environment templates and their proxy/certificate references. This tool cannot change templates or trust.",
		schema:      emptySchema(), annotations: readOnly,
	}, func(emptyInput) protocol.Response { return adapter.Execute("list_environment_templates", struct{}{}) }, validateEmpty)

	addTool(server, toolSpec{
		name: "scopenest_get_container_readiness", command: "get_container_readiness",
		description: "Check effective networking, proxy-listener status, required certificate states, warnings, and launch readiness. Call this before launching proxy or template containers.",
		schema:      idSchema("ScopeNest container ID to check"), annotations: readOnly,
	}, func(in idInput) protocol.Response {
		return adapter.Execute("get_container_readiness", struct {
			ID string `json:"id"`
		}{in.ID})
	}, validateID)

	addTool(server, toolSpec{
		name: "scopenest_create_container", command: "create_container",
		description: "Create a persistent isolated ScopeNest browser container. The existing host validates browser selection, managed paths, and network references.",
		schema:      createSchema(), annotations: mutating,
	}, func(in createContainerInput) protocol.Response { return adapter.Execute("create_container", in) }, validateCreate)

	addTool(server, toolSpec{
		name: "scopenest_create_temporary_container", command: "create_temporary_container",
		description: "Create a fresh disposable ScopeNest browser context. Cleanup occurs after the owned browser process tree exits when it is safe to delete the profile.",
		schema:      createSchema(), annotations: mutating,
	}, func(in createContainerInput) protocol.Response {
		return adapter.Execute("create_temporary_container", in)
	}, validateCreate)

	addTool(server, toolSpec{
		name: "scopenest_launch_container", command: "launch_container",
		description: "Launch an existing container at an optional authorized HTTP(S) URL. Use only for systems the user owns or is authorized to test. Call scopenest_get_container_readiness first for proxy/template containers. This opens a browser; it does not browse, click, inspect page content, or perform testing.",
		schema:      launchSchema(), annotations: mutating,
	}, func(in launchContainerInput) protocol.Response {
		data := struct {
			ID  string `json:"id"`
			URL string `json:"url,omitempty"`
		}{in.ID, in.URL}
		return adapter.ExecuteWithIdentity("launch_container", in.ID, in.ExpectedName, data)
	}, validateLaunch)

	addTool(server, toolSpec{
		name: "scopenest_close_container", command: "close_container",
		description: "Close a running container only when this MCP server process launched and still owns it. Extension-owned or other-process containers return PROCESS_NOT_OWNED; persisted PIDs never grant kill authority.",
		schema:      closeSchema(), annotations: processControl,
	}, func(in closeContainerInput) protocol.Response {
		return adapter.ExecuteWithIdentity("close_container", in.ID, in.ExpectedName, struct {
			ID string `json:"id"`
		}{in.ID})
	}, validateClose)
}

type toolSpec struct {
	name, command, description string
	schema                     map[string]any
	annotations                *mcp.ToolAnnotations
}

func addTool[T any](server *mcp.Server, spec toolSpec, execute func(T) protocol.Response, validate func(T) error) {
	server.AddTool(&mcp.Tool{Name: spec.name, Description: spec.description, InputSchema: spec.schema, Annotations: spec.annotations},
		func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var input T
			if err := strictDecode(request.Params.Arguments, &input); err != nil {
				return toolError(spec.command, "INVALID_ARGUMENT"), nil
			}
			if err := validate(input); err != nil {
				var typed toolValidationError
				if errors.As(err, &typed) {
					return toolError(spec.command, typed.code), nil
				}
				return toolError(spec.command, "INVALID_ARGUMENT"), nil
			}
			return makeToolResult(execute(input)), nil
		})
}

func strictDecode(raw json.RawMessage, target any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("arguments must contain exactly one object")
	}
	return nil
}

func validateEmpty(emptyInput) error { return nil }

func validateID(in idInput) error {
	if security.ValidateID(in.ID) != nil {
		return toolValidationError{"INVALID_CONTAINER_ID"}
	}
	return nil
}

func validateCreate(in createContainerInput) error {
	if security.ValidateName(in.Name) != nil {
		return toolValidationError{"INVALID_NAME"}
	}
	if security.ValidateColor(in.Color) != nil {
		return toolValidationError{"INVALID_COLOR"}
	}
	if security.ValidateIcon(in.Icon) != nil {
		return toolValidationError{"INVALID_ICON"}
	}
	if security.ValidateBrowserType(in.BrowserType) != nil {
		return toolValidationError{"INVALID_BROWSER"}
	}
	if security.ValidateNetworkMode(in.NetworkMode) != nil {
		return toolValidationError{"INVALID_NETWORK_MODE"}
	}
	if in.ProxyProfileID != "" && security.ValidateID(in.ProxyProfileID) != nil {
		return toolValidationError{"INVALID_PROXY_PROFILE_ID"}
	}
	if in.EnvironmentTemplateID != "" && security.ValidateID(in.EnvironmentTemplateID) != nil {
		return toolValidationError{"INVALID_TEMPLATE_ID"}
	}
	return nil
}

func validateLaunch(in launchContainerInput) error {
	if security.ValidateID(in.ID) != nil {
		return toolValidationError{"INVALID_CONTAINER_ID"}
	}
	if strings.TrimSpace(in.ExpectedName) == "" {
		return toolValidationError{"INVALID_ARGUMENT"}
	}
	if _, err := security.ValidateURL(in.URL); err != nil {
		return toolValidationError{"INVALID_URL"}
	}
	return nil
}

func validateClose(in closeContainerInput) error {
	if security.ValidateID(in.ID) != nil {
		return toolValidationError{"INVALID_CONTAINER_ID"}
	}
	if strings.TrimSpace(in.ExpectedName) == "" {
		return toolValidationError{"INVALID_ARGUMENT"}
	}
	return nil
}

func annotations(readOnly, destructive, idempotent bool) *mcp.ToolAnnotations {
	openWorld := false
	destructiveValue := destructive
	return &mcp.ToolAnnotations{ReadOnlyHint: readOnly, DestructiveHint: &destructiveValue, IdempotentHint: idempotent, OpenWorldHint: &openWorld}
}

func emptySchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
}

func idSchema(description string) map[string]any {
	return objectSchema(map[string]any{"id": idProperty(description)}, "id")
}

func createSchema() map[string]any {
	return objectSchema(map[string]any{
		"name":                  map[string]any{"type": "string", "description": "Container name", "minLength": 1, "maxLength": 80},
		"color":                 map[string]any{"type": "string", "description": "Six-digit hexadecimal container color", "pattern": "^#[0-9a-fA-F]{6}$"},
		"icon":                  map[string]any{"type": "string", "description": "Optional short container icon", "maxLength": 8},
		"browserType":           map[string]any{"type": "string", "description": "Supported Chromium-family browser type", "enum": stringsToAny(security.SupportedBrowserTypes())},
		"browserExecutable":     map[string]any{"type": "string", "description": "Optional custom Chromium-family executable path; required for browserType custom"},
		"networkMode":           map[string]any{"type": "string", "description": "Direct, proxy-profile, or environment-template networking", "enum": stringsToAny(security.SupportedNetworkModes())},
		"proxyProfileId":        idProperty("Existing proxy profile ID when networkMode is proxy"),
		"environmentTemplateId": idProperty("Existing environment template ID when networkMode is template"),
	}, "name", "color", "browserType", "networkMode")
}

func launchSchema() map[string]any {
	return objectSchema(map[string]any{
		"id":           idProperty("ScopeNest container ID to launch"),
		"expectedName": map[string]any{"type": "string", "description": "Exact current container name used as an identity confirmation", "minLength": 1, "maxLength": 80},
		"url":          map[string]any{"type": "string", "description": "Optional absolute HTTP(S) URL without credentials", "maxLength": 8192},
	}, "id", "expectedName")
}

func closeSchema() map[string]any {
	return objectSchema(map[string]any{
		"id":           idProperty("ScopeNest container ID to close"),
		"expectedName": map[string]any{"type": "string", "description": "Exact current container name used as an identity confirmation", "minLength": 1, "maxLength": 80},
	}, "id", "expectedName")
}

func idProperty(description string) map[string]any {
	return map[string]any{"type": "string", "description": description, "pattern": "^[a-f0-9]{32}$"}
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}

func stringsToAny(values []string) []any {
	result := make([]any, len(values))
	for i, value := range values {
		result[i] = value
	}
	return result
}
