package mcpserver

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sync"

	"github.com/scopenest/scopenest/native-host/internal/protocol"
)

// CommandHandler is the security authority used by the MCP adapter.
type CommandHandler interface {
	Handle(protocol.Request) protocol.Response
	LaunchForMCP(id, expectedName, url string) protocol.Response
	StartStartupCleanup()
}

type BrowserHandler interface {
	BrowserStatusForMCP(id, expectedName string) protocol.Response
	BrowserListPagesForMCP(id, expectedName string) protocol.Response
	BrowserOpenPageForMCP(id, expectedName, url string) protocol.Response
	BrowserNavigateForMCP(id, expectedName, pageID, url string) protocol.Response
	BrowserSnapshotForMCP(id, expectedName, pageID string) protocol.Response
	BrowserClickForMCP(id, expectedName, pageID, selector string) protocol.Response
	BrowserTypeForMCP(id, expectedName, pageID, selector, text string) protocol.Response
	BrowserPressKeyForMCP(id, expectedName, pageID, key string) protocol.Response
	BrowserScreenshotForMCP(id, expectedName, pageID string) protocol.Response
}

var allowedCommands = map[string]bool{
	"ping": true, "get_status": true, "list_containers": true,
	"get_running_containers": true, "list_proxy_profiles": true,
	"list_environment_templates": true, "get_container_readiness": true,
	"create_container": true, "create_temporary_container": true,
	"close_container": true,
}

// Adapter serializes all access to one long-lived ScopeNest host instance.
type Adapter struct {
	handler CommandHandler
	browser BrowserHandler
	mu      sync.Mutex
	cleanup sync.Once
}

func NewAdapter(handler CommandHandler) *Adapter {
	browser, _ := handler.(BrowserHandler)
	return &Adapter{handler: handler, browser: browser}
}

func (a *Adapter) Execute(command string, data any) protocol.Response {
	a.mu.Lock()
	defer a.mu.Unlock()
	response := a.executeLocked(command, data)
	if allowedCommands[command] {
		a.scheduleCleanupLocked()
	}
	return response
}

func (a *Adapter) ExecuteWithIdentity(command, id, expectedName string, data any) protocol.Response {
	a.mu.Lock()
	defer a.mu.Unlock()
	if command != "close_container" {
		return localError(command, "UNKNOWN_COMMAND", "The requested ScopeNest operation is not available through MCP.")
	}
	list := a.executeLocked("list_containers", struct{}{})
	if !list.Success {
		list.Command = command
		a.scheduleCleanupLocked()
		return list
	}
	var containers []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	encoded, err := json.Marshal(list.Data)
	if err != nil || json.Unmarshal(encoded, &containers) != nil {
		a.scheduleCleanupLocked()
		return localError(command, "INTERNAL_ERROR", "ScopeNest could not verify the container identity.")
	}
	for _, container := range containers {
		if container.ID != id {
			continue
		}
		if container.Name != expectedName {
			a.scheduleCleanupLocked()
			return localError(command, "CONTAINER_NAME_MISMATCH", "The expected container name does not match the current container name; no action was taken.")
		}
		response := a.executeLocked(command, data)
		a.scheduleCleanupLocked()
		return response
	}
	a.scheduleCleanupLocked()
	return localError(command, "NOT_FOUND", "The requested container was not found; no action was taken.")
}

func (a *Adapter) LaunchForMCP(id, expectedName, url string) protocol.Response {
	a.mu.Lock()
	defer a.mu.Unlock()
	requestID, err := newRequestID()
	if err != nil {
		a.scheduleCleanupLocked()
		return localError("launch_container", "INTERNAL_ERROR", "ScopeNest could not create an internal request identifier.")
	}
	response := a.handler.LaunchForMCP(id, expectedName, url)
	response.RequestID = requestID
	response.Command = "launch_container"
	a.scheduleCleanupLocked()
	return response
}

func (a *Adapter) BrowserStatus(id, expectedName string) protocol.Response {
	return a.browserCall("browser_status", func(handler BrowserHandler) protocol.Response { return handler.BrowserStatusForMCP(id, expectedName) })
}

func (a *Adapter) BrowserListPages(id, expectedName string) protocol.Response {
	return a.browserCall("browser_list_pages", func(handler BrowserHandler) protocol.Response {
		return handler.BrowserListPagesForMCP(id, expectedName)
	})
}

func (a *Adapter) BrowserOpenPage(id, expectedName, url string) protocol.Response {
	return a.browserCall("browser_open_page", func(handler BrowserHandler) protocol.Response {
		return handler.BrowserOpenPageForMCP(id, expectedName, url)
	})
}

func (a *Adapter) BrowserNavigate(id, expectedName, pageID, url string) protocol.Response {
	return a.browserCall("browser_navigate", func(handler BrowserHandler) protocol.Response {
		return handler.BrowserNavigateForMCP(id, expectedName, pageID, url)
	})
}

func (a *Adapter) BrowserSnapshot(id, expectedName, pageID string) protocol.Response {
	return a.browserCall("browser_snapshot", func(handler BrowserHandler) protocol.Response {
		return handler.BrowserSnapshotForMCP(id, expectedName, pageID)
	})
}

func (a *Adapter) BrowserClick(id, expectedName, pageID, selector string) protocol.Response {
	return a.browserCall("browser_click", func(handler BrowserHandler) protocol.Response {
		return handler.BrowserClickForMCP(id, expectedName, pageID, selector)
	})
}

func (a *Adapter) BrowserType(id, expectedName, pageID, selector, text string) protocol.Response {
	return a.browserCall("browser_type", func(handler BrowserHandler) protocol.Response {
		return handler.BrowserTypeForMCP(id, expectedName, pageID, selector, text)
	})
}

func (a *Adapter) BrowserPressKey(id, expectedName, pageID, key string) protocol.Response {
	return a.browserCall("browser_press_key", func(handler BrowserHandler) protocol.Response {
		return handler.BrowserPressKeyForMCP(id, expectedName, pageID, key)
	})
}

func (a *Adapter) BrowserScreenshot(id, expectedName, pageID string) protocol.Response {
	return a.browserCall("browser_screenshot", func(handler BrowserHandler) protocol.Response {
		return handler.BrowserScreenshotForMCP(id, expectedName, pageID)
	})
}

func (a *Adapter) browserCall(command string, invoke func(BrowserHandler) protocol.Response) protocol.Response {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.browser == nil {
		return localError(command, "AUTOMATION_UNAVAILABLE", "This ScopeNest host does not support browser automation.")
	}
	requestID, err := newRequestID()
	if err != nil {
		return localError(command, "INTERNAL_ERROR", "ScopeNest could not create an internal request identifier.")
	}
	response := invoke(a.browser)
	response.RequestID = requestID
	response.Command = command
	a.scheduleCleanupLocked()
	return response
}

func (a *Adapter) executeLocked(command string, data any) protocol.Response {
	if !allowedCommands[command] {
		return localError(command, "UNKNOWN_COMMAND", "The requested ScopeNest operation is not available through MCP.")
	}
	requestID, err := newRequestID()
	if err != nil {
		return localError(command, "INTERNAL_ERROR", "ScopeNest could not create an internal request identifier.")
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return localError(command, "INVALID_DATA", "The MCP input could not be encoded safely.")
	}
	return a.handler.Handle(protocol.Request{Version: protocol.Version, RequestID: requestID, Command: command, Data: raw})
}

func (a *Adapter) scheduleCleanupLocked() {
	a.cleanup.Do(a.handler.StartStartupCleanup)
}

func newRequestID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func localError(command, code, message string) protocol.Response {
	return protocol.Response{Version: protocol.Version, Success: false, Command: command, ErrorCode: code, Error: &protocol.ErrorDetail{Message: message}}
}
