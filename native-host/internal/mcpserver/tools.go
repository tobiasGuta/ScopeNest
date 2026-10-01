package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

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
	NetworkMode           string `json:"networkMode"`
	ProxyProfileID        string `json:"proxyProfileId,omitempty"`
	EnvironmentTemplateID string `json:"environmentTemplateId,omitempty"`
	AutomationEnabled     bool   `json:"automationEnabled,omitempty"`
}

type updateContainerInput struct {
	ID                    string `json:"id"`
	ExpectedName          string `json:"expectedName"`
	Name                  string `json:"name"`
	Color                 string `json:"color"`
	Icon                  string `json:"icon,omitempty"`
	BrowserType           string `json:"browserType"`
	NetworkMode           string `json:"networkMode"`
	ProxyProfileID        string `json:"proxyProfileId,omitempty"`
	EnvironmentTemplateID string `json:"environmentTemplateId,omitempty"`
	AutomationEnabled     bool   `json:"automationEnabled,omitempty"`
}

var mcpBrowserTypes = []string{"chrome", "chromium", "edge", "brave"}

type launchContainerInput struct {
	ID                string `json:"id"`
	ExpectedName      string `json:"expectedName"`
	URL               string `json:"url,omitempty"`
	AutomationEnabled *bool  `json:"automationEnabled,omitempty"`
}

type closeContainerInput struct {
	ID           string `json:"id"`
	ExpectedName string `json:"expectedName"`
}

type browserIdentityInput struct {
	ID           string `json:"id"`
	ExpectedName string `json:"expectedName"`
}

type browserPageInput struct {
	ID           string `json:"id"`
	ExpectedName string `json:"expectedName"`
	PageID       string `json:"pageId"`
}

type browserURLInput struct {
	ID           string `json:"id"`
	ExpectedName string `json:"expectedName"`
	URL          string `json:"url"`
}

type browserNavigateInput struct {
	ID           string `json:"id"`
	ExpectedName string `json:"expectedName"`
	PageID       string `json:"pageId"`
	URL          string `json:"url"`
}

type browserClickInput struct {
	ID           string `json:"id"`
	ExpectedName string `json:"expectedName"`
	PageID       string `json:"pageId"`
	Selector     string `json:"selector"`
}

type browserTypeInput struct {
	ID           string `json:"id"`
	ExpectedName string `json:"expectedName"`
	PageID       string `json:"pageId"`
	Selector     string `json:"selector"`
	Text         string `json:"text"`
}

type browserKeyInput struct {
	ID           string `json:"id"`
	ExpectedName string `json:"expectedName"`
	PageID       string `json:"pageId"`
	Key          string `json:"key"`
}

type toolValidationError struct{ code string }

func (e toolValidationError) Error() string { return e.code }

func registerTools(server *mcp.Server, adapter *Adapter) {
	readOnly := annotations(true, false, true, false)
	mutating := annotations(false, false, false, false)
	launch := annotations(false, true, false, true)
	processControl := annotations(false, true, false, false)

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
		description: "List sanitized loopback proxy-profile metadata for container selection. Bypass rules are excluded, and this tool cannot create or change proxies.",
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
		name: "scopenest_update_container", command: "update_container",
		description: "Update an existing ScopeNest browser container's metadata, browser selection, network mode, and automation settings.",
		schema:      updateSchema(), annotations: mutating,
	}, func(in updateContainerInput) protocol.Response {
		return adapter.ExecuteWithIdentity("update_container", in.ID, in.ExpectedName, in)
	}, validateUpdate)

	addTool(server, toolSpec{
		name: "scopenest_launch_container", command: "launch_container",
		description: "Launch a standard-browser container at an optional authorized HTTP(S) URL. Custom-browser containers require a human launch. Use only for systems the user owns or is authorized to test. Call scopenest_get_container_readiness first for proxy/template containers. This opens a browser; it does not browse, click, inspect page content, or perform testing.",
		schema:      launchSchema(), annotations: launch,
	}, func(in launchContainerInput) protocol.Response {
		return adapter.LaunchForMCP(in.ID, in.ExpectedName, in.URL, in.AutomationEnabled)
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

	addTool(server, toolSpec{
		name: "scopenest_browser_status", command: "browser_status",
		description: "Return whether the explicitly named running container has its opted-in, local-only automation bridge ready. Runtime endpoint details are not exposed.",
		schema:      browserIdentitySchema(), annotations: readOnly,
	}, func(in browserIdentityInput) protocol.Response { return adapter.BrowserStatus(in.ID, in.ExpectedName) }, validateBrowserIdentity)

	addTool(server, toolSpec{
		name: "scopenest_get_cdp_endpoint", command: "get_cdp_endpoint",
		description: "Return the local loopback Chrome DevTools Protocol (CDP) WebSocket and HTTP endpoints for an explicitly named running container with automation enabled. External tools like Playwright or Puppeteer can connect to this endpoint.",
		schema:      browserIdentitySchema(), annotations: readOnly,
	}, func(in browserIdentityInput) protocol.Response {
		return adapter.BrowserCDPEndpoint(in.ID, in.ExpectedName)
	}, validateBrowserIdentity)

	addTool(server, toolSpec{
		name: "scopenest_browser_list_pages", command: "browser_list_pages",
		description: "List the pages belonging only to this automation-enabled ScopeNest container. Returned page IDs are opaque and container-scoped.",
		schema:      browserIdentitySchema(), annotations: readOnly,
	}, func(in browserIdentityInput) protocol.Response {
		return adapter.BrowserListPages(in.ID, in.ExpectedName)
	}, validateBrowserIdentity)

	addTool(server, toolSpec{
		name: "scopenest_browser_open_page", command: "browser_open_page",
		description: "Open an authorized HTTP(S) URL in a new page inside this exact automation-enabled ScopeNest container.",
		schema:      browserURLSchema(), annotations: launch,
	}, func(in browserURLInput) protocol.Response {
		return adapter.BrowserOpenPage(in.ID, in.ExpectedName, in.URL)
	}, validateBrowserURL)

	addTool(server, toolSpec{
		name: "scopenest_browser_navigate", command: "browser_navigate",
		description: "Navigate one opaque page belonging to this exact automation-enabled ScopeNest container to an authorized HTTP(S) URL.",
		schema:      browserNavigateSchema(), annotations: launch,
	}, func(in browserNavigateInput) protocol.Response {
		return adapter.BrowserNavigate(in.ID, in.ExpectedName, in.PageID, in.URL)
	}, validateBrowserNavigate)

	addTool(server, toolSpec{
		name: "scopenest_browser_snapshot", command: "browser_snapshot",
		description: "Read a bounded text snapshot of one page belonging to this exact automation-enabled ScopeNest container. It may include authenticated page content.",
		schema:      browserPageSchema(), annotations: readOnly,
	}, func(in browserPageInput) protocol.Response {
		return adapter.BrowserSnapshot(in.ID, in.ExpectedName, in.PageID)
	}, validateBrowserPage)

	addTool(server, toolSpec{
		name: "scopenest_browser_click", command: "browser_click",
		description: "Click a CSS-selected element in one page belonging to this exact automation-enabled ScopeNest container.",
		schema:      browserClickSchema(), annotations: mutating,
	}, func(in browserClickInput) protocol.Response {
		return adapter.BrowserClick(in.ID, in.ExpectedName, in.PageID, in.Selector)
	}, validateBrowserClick)

	addTool(server, toolSpec{
		name: "scopenest_browser_type", command: "browser_type",
		description: "Type bounded UTF-8 text into a CSS-selected element in one page belonging to this exact automation-enabled ScopeNest container.",
		schema:      browserTypeSchema(), annotations: mutating,
	}, func(in browserTypeInput) protocol.Response {
		return adapter.BrowserType(in.ID, in.ExpectedName, in.PageID, in.Selector, in.Text)
	}, validateBrowserType)

	addTool(server, toolSpec{
		name: "scopenest_browser_press_key", command: "browser_press_key",
		description: "Send one allowlisted navigation or confirmation key to a page belonging to this exact automation-enabled ScopeNest container.",
		schema:      browserKeySchema(), annotations: mutating,
	}, func(in browserKeyInput) protocol.Response {
		return adapter.BrowserPressKey(in.ID, in.ExpectedName, in.PageID, in.Key)
	}, validateBrowserKey)

	addTool(server, toolSpec{
		name: "scopenest_browser_screenshot", command: "browser_screenshot",
		description: "Capture a bounded PNG screenshot of one page belonging to this exact automation-enabled ScopeNest container. It may include authenticated page content.",
		schema:      browserPageSchema(), annotations: readOnly,
	}, func(in browserPageInput) protocol.Response {
		return adapter.BrowserScreenshot(in.ID, in.ExpectedName, in.PageID)
	}, validateBrowserPage)
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
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		trimmed = []byte("{}")
	}
	if trimmed[0] != '{' {
		return errors.New("arguments must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
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
	if !isMCPBrowserType(in.BrowserType) {
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

func validateUpdate(in updateContainerInput) error {
	if security.ValidateID(in.ID) != nil {
		return toolValidationError{"INVALID_CONTAINER_ID"}
	}
	if strings.TrimSpace(in.ExpectedName) == "" {
		return toolValidationError{"INVALID_ARGUMENT"}
	}
	return validateCreate(createContainerInput{
		Name:                  in.Name,
		Color:                 in.Color,
		Icon:                  in.Icon,
		BrowserType:           in.BrowserType,
		NetworkMode:           in.NetworkMode,
		ProxyProfileID:        in.ProxyProfileID,
		EnvironmentTemplateID: in.EnvironmentTemplateID,
		AutomationEnabled:     in.AutomationEnabled,
	})
}

func isMCPBrowserType(browserType string) bool {
	for _, supported := range mcpBrowserTypes {
		if browserType == supported {
			return true
		}
	}
	return false
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

func validateBrowserIdentity(in browserIdentityInput) error {
	if security.ValidateID(in.ID) != nil {
		return toolValidationError{"INVALID_CONTAINER_ID"}
	}
	if strings.TrimSpace(in.ExpectedName) == "" {
		return toolValidationError{"INVALID_ARGUMENT"}
	}
	return nil
}

func validateBrowserPage(in browserPageInput) error {
	if err := validateBrowserIdentity(browserIdentityInput{ID: in.ID, ExpectedName: in.ExpectedName}); err != nil {
		return err
	}
	if security.ValidateID(in.PageID) != nil {
		return toolValidationError{"INVALID_PAGE_ID"}
	}
	return nil
}

func validateBrowserURL(in browserURLInput) error {
	if err := validateBrowserIdentity(browserIdentityInput{ID: in.ID, ExpectedName: in.ExpectedName}); err != nil {
		return err
	}
	if _, err := security.ValidateURL(in.URL); err != nil {
		return toolValidationError{"INVALID_URL"}
	}
	return nil
}

func validateBrowserNavigate(in browserNavigateInput) error {
	if err := validateBrowserPage(browserPageInput{ID: in.ID, ExpectedName: in.ExpectedName, PageID: in.PageID}); err != nil {
		return err
	}
	if _, err := security.ValidateURL(in.URL); err != nil {
		return toolValidationError{"INVALID_URL"}
	}
	return nil
}

func validateBrowserClick(in browserClickInput) error {
	if err := validateBrowserPage(browserPageInput{ID: in.ID, ExpectedName: in.ExpectedName, PageID: in.PageID}); err != nil {
		return err
	}
	selector := strings.TrimSpace(in.Selector)
	if selector == "" || len(selector) > 512 || !utf8.ValidString(selector) {
		return toolValidationError{"INVALID_SELECTOR"}
	}
	for _, char := range selector {
		if unicode.IsControl(char) {
			return toolValidationError{"INVALID_SELECTOR"}
		}
	}
	return nil
}

func validateBrowserType(in browserTypeInput) error {
	if err := validateBrowserClick(browserClickInput{ID: in.ID, ExpectedName: in.ExpectedName, PageID: in.PageID, Selector: in.Selector}); err != nil {
		return err
	}
	if in.Text == "" || len(in.Text) > 8192 {
		return toolValidationError{"INVALID_TEXT"}
	}
	return nil
}

func validateBrowserKey(in browserKeyInput) error {
	if err := validateBrowserPage(browserPageInput{ID: in.ID, ExpectedName: in.ExpectedName, PageID: in.PageID}); err != nil {
		return err
	}
	if !map[string]bool{"Enter": true, "Tab": true, "Escape": true, "Backspace": true, "Delete": true, "ArrowUp": true, "ArrowDown": true, "ArrowLeft": true, "ArrowRight": true, "Home": true, "End": true, "PageUp": true, "PageDown": true, "Space": true}[in.Key] {
		return toolValidationError{"INVALID_KEY"}
	}
	return nil
}

func annotations(readOnly, destructive, idempotent, openWorld bool) *mcp.ToolAnnotations {
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
		"color":                 map[string]any{"type": "string", "description": "Container color in exact #RRGGBB form, including the leading # (example: #3B82F6)", "pattern": "^#[0-9a-fA-F]{6}$"},
		"icon":                  map[string]any{"type": "string", "description": "Optional short container icon", "maxLength": 8},
		"browserType":           map[string]any{"type": "string", "description": "Standard Chromium-family browser type resolved from locally detected installations", "enum": stringsToAny(mcpBrowserTypes)},
		"networkMode":           map[string]any{"type": "string", "description": "Direct, proxy-profile, or environment-template networking", "enum": stringsToAny(security.SupportedNetworkModes())},
		"proxyProfileId":        idProperty("Existing proxy profile ID when networkMode is proxy"),
		"environmentTemplateId": idProperty("Existing environment template ID when networkMode is template"),
		"automationEnabled":     map[string]any{"type": "boolean", "description": "Explicitly allow localhost-only browser automation for this container when launched"},
	}, "name", "color", "browserType", "networkMode")
}

func updateSchema() map[string]any {
	return objectSchema(map[string]any{
		"id":                    idProperty("ScopeNest container ID to update"),
		"expectedName":          map[string]any{"type": "string", "description": "Exact current container name used as an identity confirmation", "minLength": 1, "maxLength": 80},
		"name":                  map[string]any{"type": "string", "description": "New container name", "minLength": 1, "maxLength": 80},
		"color":                 map[string]any{"type": "string", "description": "Container color in exact #RRGGBB form, including the leading # (example: #3B82F6)", "pattern": "^#[0-9a-fA-F]{6}$"},
		"icon":                  map[string]any{"type": "string", "description": "Optional short container icon", "maxLength": 8},
		"browserType":           map[string]any{"type": "string", "description": "Standard Chromium-family browser type resolved from locally detected installations", "enum": stringsToAny(mcpBrowserTypes)},
		"networkMode":           map[string]any{"type": "string", "description": "Direct, proxy-profile, or environment-template networking", "enum": stringsToAny(security.SupportedNetworkModes())},
		"proxyProfileId":        idProperty("Existing proxy profile ID when networkMode is proxy"),
		"environmentTemplateId": idProperty("Existing environment template ID when networkMode is template"),
		"automationEnabled":     map[string]any{"type": "boolean", "description": "Explicitly allow localhost-only browser automation for this container when launched"},
	}, "id", "expectedName", "name", "color", "browserType", "networkMode")
}

func browserIdentitySchema() map[string]any {
	return objectSchema(map[string]any{"id": idProperty("ScopeNest container ID"), "expectedName": map[string]any{"type": "string", "minLength": 1, "maxLength": 80}}, "id", "expectedName")
}

func browserPageSchema() map[string]any {
	return objectSchema(map[string]any{"id": idProperty("ScopeNest container ID"), "expectedName": map[string]any{"type": "string", "minLength": 1, "maxLength": 80}, "pageId": idProperty("Opaque page ID returned by scopenest_browser_list_pages for this container")}, "id", "expectedName", "pageId")
}

func browserURLSchema() map[string]any {
	return objectSchema(map[string]any{"id": idProperty("ScopeNest container ID"), "expectedName": map[string]any{"type": "string", "minLength": 1, "maxLength": 80}, "url": map[string]any{"type": "string", "minLength": 1, "maxLength": 8192}}, "id", "expectedName", "url")
}

func browserNavigateSchema() map[string]any {
	return objectSchema(map[string]any{"id": idProperty("ScopeNest container ID"), "expectedName": map[string]any{"type": "string", "minLength": 1, "maxLength": 80}, "pageId": idProperty("Opaque page ID returned by scopenest_browser_list_pages for this container"), "url": map[string]any{"type": "string", "minLength": 1, "maxLength": 8192}}, "id", "expectedName", "pageId", "url")
}

func browserClickSchema() map[string]any {
	return objectSchema(map[string]any{"id": idProperty("ScopeNest container ID"), "expectedName": map[string]any{"type": "string", "minLength": 1, "maxLength": 80}, "pageId": idProperty("Opaque page ID returned by scopenest_browser_list_pages for this container"), "selector": map[string]any{"type": "string", "minLength": 1, "maxLength": 512}}, "id", "expectedName", "pageId", "selector")
}

func browserTypeSchema() map[string]any {
	return objectSchema(map[string]any{"id": idProperty("ScopeNest container ID"), "expectedName": map[string]any{"type": "string", "minLength": 1, "maxLength": 80}, "pageId": idProperty("Opaque page ID returned by scopenest_browser_list_pages for this container"), "selector": map[string]any{"type": "string", "minLength": 1, "maxLength": 512}, "text": map[string]any{"type": "string", "minLength": 1, "maxLength": 8192}}, "id", "expectedName", "pageId", "selector", "text")
}

func browserKeySchema() map[string]any {
	return objectSchema(map[string]any{"id": idProperty("ScopeNest container ID"), "expectedName": map[string]any{"type": "string", "minLength": 1, "maxLength": 80}, "pageId": idProperty("Opaque page ID returned by scopenest_browser_list_pages for this container"), "key": map[string]any{"type": "string", "enum": stringsToAny([]string{"Enter", "Tab", "Escape", "Backspace", "Delete", "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "Home", "End", "PageUp", "PageDown", "Space"})}}, "id", "expectedName", "pageId", "key")
}

func launchSchema() map[string]any {
	return objectSchema(map[string]any{
		"id":                idProperty("ScopeNest container ID to launch"),
		"expectedName":      map[string]any{"type": "string", "description": "Exact current container name used as an identity confirmation", "minLength": 1, "maxLength": 80},
		"url":               map[string]any{"type": "string", "description": "Optional absolute HTTP(S) URL without credentials", "maxLength": 8192},
		"automationEnabled": map[string]any{"type": "boolean", "description": "Optional override to enable or disable local browser automation for this launch session"},
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
