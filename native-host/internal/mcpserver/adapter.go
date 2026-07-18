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
	StartStartupCleanup()
}

var allowedCommands = map[string]bool{
	"ping": true, "get_status": true, "list_containers": true,
	"get_running_containers": true, "list_proxy_profiles": true,
	"list_environment_templates": true, "get_container_readiness": true,
	"create_container": true, "create_temporary_container": true,
	"launch_container": true, "close_container": true,
}

// Adapter serializes all access to one long-lived ScopeNest host instance.
type Adapter struct {
	handler CommandHandler
	mu      sync.Mutex
	cleanup sync.Once
}

func NewAdapter(handler CommandHandler) *Adapter { return &Adapter{handler: handler} }

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
	if command != "launch_container" && command != "close_container" {
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
