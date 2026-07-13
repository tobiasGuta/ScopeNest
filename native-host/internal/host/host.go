package host

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/scopenest/scopenest/native-host/internal/browser"
	"github.com/scopenest/scopenest/native-host/internal/model"
	"github.com/scopenest/scopenest/native-host/internal/protocol"
	"github.com/scopenest/scopenest/native-host/internal/security"
	"github.com/scopenest/scopenest/native-host/internal/store"
)

const HostVersion = "1.0.0"

var allowedCommands = map[string]bool{
	"ping": true, "get_status": true, "list_containers": true,
	"create_container": true, "update_container": true, "launch_container": true,
	"close_container": true, "delete_container": true, "create_temporary_container": true,
	"cleanup_temporary_containers": true, "get_running_containers": true,
	"validate_browser_path": true,
}

type Host struct {
	store     *store.Store
	launcher  browser.Launcher
	processes map[string]*os.Process
	mu        sync.Mutex
	now       func() time.Time
}

type containerInput struct {
	Name              string `json:"name"`
	Color             string `json:"color"`
	Icon              string `json:"icon,omitempty"`
	BrowserType       string `json:"browserType"`
	BrowserExecutable string `json:"browserExecutable,omitempty"`
}

type idInput struct {
	ID string `json:"id"`
}

type updateInput struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Color             string `json:"color"`
	Icon              string `json:"icon,omitempty"`
	BrowserType       string `json:"browserType"`
	BrowserExecutable string `json:"browserExecutable,omitempty"`
}

type launchInput struct {
	ID  string `json:"id"`
	URL string `json:"url,omitempty"`
}

type validatePathInput struct {
	Path string `json:"path"`
}

func New(st *store.Store, launcher browser.Launcher) *Host {
	return &Host{store: st, launcher: launcher, processes: map[string]*os.Process{}, now: func() time.Time { return time.Now().UTC() }}
}

func DecodeRequest(payload []byte) (protocol.Request, error) {
	var req protocol.Request
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return req, fail("INVALID_REQUEST", "invalid request JSON: %v", err)
	}
	if err := ensureEOF(dec); err != nil {
		return req, fail("INVALID_REQUEST", "request must contain exactly one JSON object")
	}
	if req.Version != protocol.Version {
		return req, fail("UNSUPPORTED_VERSION", "protocol version must be %d", protocol.Version)
	}
	if req.RequestID == "" || len(req.RequestID) > 128 {
		return req, fail("INVALID_REQUEST_ID", "requestId must contain 1 to 128 characters")
	}
	if !allowedCommands[req.Command] {
		return req, fail("UNKNOWN_COMMAND", "command is not supported")
	}
	return req, nil
}

func (h *Host) Handle(req protocol.Request) protocol.Response {
	data, err := h.dispatch(req)
	if err == nil {
		return protocol.NewSuccess(req, data)
	}
	var commandErr *commandError
	if errors.As(err, &commandErr) {
		return protocol.NewError(req, commandErr.code, commandErr.message)
	}
	return protocol.NewError(req, "INTERNAL_ERROR", "the native host could not complete the request")
}

func (h *Host) dispatch(req protocol.Request) (any, error) {
	switch req.Command {
	case "ping":
		if err := requireEmptyData(req.Data); err != nil {
			return nil, err
		}
		return map[string]any{"hostVersion": HostVersion, "protocolVersion": protocol.Version}, nil
	case "get_status":
		if err := requireEmptyData(req.Data); err != nil {
			return nil, err
		}
		return h.status()
	case "list_containers":
		if err := requireEmptyData(req.Data); err != nil {
			return nil, err
		}
		return h.list(false)
	case "get_running_containers":
		if err := requireEmptyData(req.Data); err != nil {
			return nil, err
		}
		return h.list(true)
	case "create_container":
		var in containerInput
		if err := decodeData(req.Data, &in); err != nil {
			return nil, err
		}
		return h.create(in, false)
	case "create_temporary_container":
		var in containerInput
		if err := decodeData(req.Data, &in); err != nil {
			return nil, err
		}
		return h.create(in, true)
	case "update_container":
		var in updateInput
		if err := decodeData(req.Data, &in); err != nil {
			return nil, err
		}
		return h.update(in)
	case "launch_container":
		var in launchInput
		if err := decodeData(req.Data, &in); err != nil {
			return nil, err
		}
		return h.launch(in)
	case "close_container":
		var in idInput
		if err := decodeData(req.Data, &in); err != nil {
			return nil, err
		}
		return h.close(in.ID)
	case "delete_container":
		var in idInput
		if err := decodeData(req.Data, &in); err != nil {
			return nil, err
		}
		return h.delete(in.ID)
	case "cleanup_temporary_containers":
		if err := requireEmptyData(req.Data); err != nil {
			return nil, err
		}
		return h.cleanup()
	case "validate_browser_path":
		var in validatePathInput
		if err := decodeData(req.Data, &in); err != nil {
			return nil, err
		}
		path, err := security.ValidateBrowserExecutable(in.Path, "custom")
		if err != nil {
			return nil, fail("INVALID_BROWSER_PATH", "%v", err)
		}
		return map[string]any{"valid": true, "path": path}, nil
	default:
		return nil, fail("UNKNOWN_COMMAND", "command is not supported")
	}
}

func (h *Host) status() (any, error) {
	db, err := h.store.Load()
	if err != nil {
		return nil, err
	}
	return map[string]any{"hostVersion": HostVersion, "protocolVersion": protocol.Version, "dataDirectory": h.store.Root(), "containerCount": len(db.Containers), "detectedBrowsers": browser.Detect()}, nil
}

func (h *Host) reconcile() error {
	return h.store.Update(func(db *model.Database) error {
		for i := range db.Containers {
			c := &db.Containers[i]
			if c.Running && c.PID > 0 && !processExists(c.PID) {
				c.Running, c.PID = false, 0
				c.UpdatedAt = h.now()
			}
		}
		return nil
	})
}

func (h *Host) list(runningOnly bool) ([]model.Container, error) {
	if err := h.reconcile(); err != nil {
		return nil, err
	}
	db, err := h.store.Load()
	if err != nil {
		return nil, err
	}
	items := make([]model.Container, 0, len(db.Containers))
	for _, c := range db.Containers {
		if !runningOnly || c.Running {
			items = append(items, c)
		}
	}
	sort.Slice(items, func(i, j int) bool { return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name) })
	return items, nil
}

func validateContainerInput(in containerInput) (containerInput, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Icon = strings.TrimSpace(in.Icon)
	if err := security.ValidateName(in.Name); err != nil {
		return in, fail("INVALID_NAME", "%v", err)
	}
	if err := security.ValidateColor(in.Color); err != nil {
		return in, fail("INVALID_COLOR", "%v", err)
	}
	if err := security.ValidateIcon(in.Icon); err != nil {
		return in, fail("INVALID_ICON", "%v", err)
	}
	if err := security.ValidateBrowserType(in.BrowserType); err != nil {
		return in, fail("INVALID_BROWSER", "%v", err)
	}
	path, err := security.ValidateBrowserExecutable(in.BrowserExecutable, in.BrowserType)
	if err != nil {
		return in, fail("INVALID_BROWSER_PATH", "%v", err)
	}
	in.BrowserExecutable = path
	return in, nil
}

func (h *Host) create(in containerInput, temporary bool) (model.Container, error) {
	in, err := validateContainerInput(in)
	if err != nil {
		return model.Container{}, err
	}
	id, err := security.NewID()
	if err != nil {
		return model.Container{}, err
	}
	profile, err := h.store.EnsureProfile(id)
	if err != nil {
		return model.Container{}, err
	}
	now := h.now()
	c := model.Container{ID: id, Name: in.Name, Color: in.Color, Icon: in.Icon, CreatedAt: now, UpdatedAt: now, Temporary: temporary, ProfilePath: profile, BrowserType: in.BrowserType, BrowserExecutable: in.BrowserExecutable}
	if err := h.store.Update(func(db *model.Database) error { db.Containers = append(db.Containers, c); return nil }); err != nil {
		_ = h.store.RemoveContainerDirectory(id)
		return model.Container{}, err
	}
	return c, nil
}

func (h *Host) update(in updateInput) (model.Container, error) {
	if err := security.ValidateID(in.ID); err != nil {
		return model.Container{}, fail("INVALID_CONTAINER_ID", "%v", err)
	}
	validated, err := validateContainerInput(containerInput{Name: in.Name, Color: in.Color, Icon: in.Icon, BrowserType: in.BrowserType, BrowserExecutable: in.BrowserExecutable})
	if err != nil {
		return model.Container{}, err
	}
	var result model.Container
	err = h.store.Update(func(db *model.Database) error {
		for i := range db.Containers {
			if db.Containers[i].ID == in.ID {
				c := &db.Containers[i]
				c.Name, c.Color, c.Icon = validated.Name, validated.Color, validated.Icon
				c.BrowserType, c.BrowserExecutable = validated.BrowserType, validated.BrowserExecutable
				c.UpdatedAt, result = h.now(), *c
				return nil
			}
		}
		return fail("NOT_FOUND", "container was not found")
	})
	return result, err
}

func (h *Host) launch(in launchInput) (model.Container, error) {
	if err := security.ValidateID(in.ID); err != nil {
		return model.Container{}, fail("INVALID_CONTAINER_ID", "%v", err)
	}
	validatedURL, err := security.ValidateURL(in.URL)
	if err != nil {
		return model.Container{}, fail("INVALID_URL", "%v", err)
	}
	if err := h.reconcile(); err != nil {
		return model.Container{}, err
	}
	db, err := h.store.Load()
	if err != nil {
		return model.Container{}, err
	}
	var c *model.Container
	for i := range db.Containers {
		if db.Containers[i].ID == in.ID {
			copy := db.Containers[i]
			c = &copy
			break
		}
	}
	if c == nil {
		return model.Container{}, fail("NOT_FOUND", "container was not found")
	}
	if c.Running {
		return *c, fail("ALREADY_RUNNING", "container is already running")
	}
	profile, err := h.store.EnsureProfile(c.ID)
	if err != nil {
		return model.Container{}, err
	}
	executable, err := security.ValidateBrowserExecutable(c.BrowserExecutable, c.BrowserType)
	if err != nil {
		return model.Container{}, fail("INVALID_BROWSER_PATH", "%v", err)
	}
	args, err := browser.Arguments(profile, validatedURL)
	if err != nil {
		return model.Container{}, fail("INVALID_URL", "%v", err)
	}
	process, err := h.launcher.Start(executable, args)
	if err != nil {
		return model.Container{}, fail("LAUNCH_FAILED", "browser could not be started: %v", err)
	}
	h.mu.Lock()
	h.processes[c.ID] = process
	h.mu.Unlock()
	now := h.now()
	c.Running, c.PID, c.LastLaunchedAt, c.UpdatedAt = true, process.Pid, &now, now
	if err := h.store.Update(func(db *model.Database) error {
		for i := range db.Containers {
			if db.Containers[i].ID == c.ID {
				db.Containers[i] = *c
				return nil
			}
		}
		return fail("NOT_FOUND", "container was not found")
	}); err != nil {
		_ = process.Kill()
		return model.Container{}, err
	}
	go h.watch(c.ID, process)
	return *c, nil
}

func (h *Host) watch(id string, process *os.Process) {
	_, _ = process.Wait()
	h.mu.Lock()
	if h.processes[id] == process {
		delete(h.processes, id)
	}
	h.mu.Unlock()
	temporary := false
	_ = h.store.Update(func(db *model.Database) error {
		for i := range db.Containers {
			if db.Containers[i].ID == id {
				db.Containers[i].Running, db.Containers[i].PID = false, 0
				db.Containers[i].UpdatedAt = h.now()
				temporary = db.Containers[i].Temporary
				return nil
			}
		}
		return nil
	})
	if temporary {
		_, _ = h.deleteTemporary(id)
	}
}

func (h *Host) close(id string) (model.Container, error) {
	if err := security.ValidateID(id); err != nil {
		return model.Container{}, fail("INVALID_CONTAINER_ID", "%v", err)
	}
	h.mu.Lock()
	process := h.processes[id]
	h.mu.Unlock()
	if process == nil {
		return model.Container{}, fail("PROCESS_NOT_OWNED", "this host session did not launch the container process; close its browser window instead")
	}
	if err := process.Kill(); err != nil {
		return model.Container{}, fail("CLOSE_FAILED", "container process could not be terminated")
	}
	db, err := h.store.Load()
	if err != nil {
		return model.Container{}, err
	}
	for _, c := range db.Containers {
		if c.ID == id {
			return c, nil
		}
	}
	return model.Container{}, fail("NOT_FOUND", "container was not found")
}

func (h *Host) delete(id string) (map[string]any, error) {
	if err := security.ValidateID(id); err != nil {
		return nil, fail("INVALID_CONTAINER_ID", "%v", err)
	}
	db, err := h.store.Load()
	if err != nil {
		return nil, err
	}
	found := false
	for _, c := range db.Containers {
		if c.ID == id {
			found = true
			if c.Running {
				return nil, fail("CONTAINER_RUNNING", "close the container before deleting it")
			}
			break
		}
	}
	if !found {
		return nil, fail("NOT_FOUND", "container was not found")
	}
	inUse, err := h.store.ProfileInUse(id)
	if err != nil {
		return nil, err
	}
	if inUse {
		return nil, fail("PROFILE_IN_USE", "the browser profile is still in use")
	}
	if err := h.store.RemoveContainerDirectory(id); err != nil {
		return nil, fail("DELETE_FAILED", "container profile could not be removed: %v", err)
	}
	if err := h.store.Update(func(db *model.Database) error {
		items := db.Containers[:0]
		for _, c := range db.Containers {
			if c.ID != id {
				items = append(items, c)
			}
		}
		db.Containers = items
		return nil
	}); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": true, "id": id}, nil
}

func (h *Host) deleteTemporary(id string) (bool, error) {
	inUse, err := h.store.ProfileInUse(id)
	if err != nil {
		return false, err
	}
	if inUse {
		_ = h.store.Update(func(db *model.Database) error {
			for i := range db.Containers {
				if db.Containers[i].ID == id {
					db.Containers[i].PendingCleanup = true
				}
			}
			return nil
		})
		return false, fail("PROFILE_IN_USE", "temporary profile is still in use")
	}
	if err := h.store.RemoveContainerDirectory(id); err != nil {
		_ = h.store.Update(func(db *model.Database) error {
			for i := range db.Containers {
				if db.Containers[i].ID == id {
					db.Containers[i].PendingCleanup = true
				}
			}
			return nil
		})
		return false, err
	}
	err = h.store.Update(func(db *model.Database) error {
		items := db.Containers[:0]
		for _, c := range db.Containers {
			if c.ID != id {
				items = append(items, c)
			}
		}
		db.Containers = items
		return nil
	})
	return err == nil, err
}

func (h *Host) cleanup() (map[string]any, error) {
	if err := h.reconcile(); err != nil {
		return nil, err
	}
	db, err := h.store.Load()
	if err != nil {
		return nil, err
	}
	cleaned, pending := []string{}, []string{}
	for _, c := range db.Containers {
		if c.Temporary && !c.Running {
			ok, err := h.deleteTemporary(c.ID)
			if err == nil && ok {
				cleaned = append(cleaned, c.ID)
			} else {
				pending = append(pending, c.ID)
			}
		}
	}
	return map[string]any{"cleaned": cleaned, "pending": pending}, nil
}

func decodeData(raw json.RawMessage, target any) error {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fail("INVALID_DATA", "command data must be an object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return fail("INVALID_DATA", "invalid command data: %v", err)
	}
	if err := ensureEOF(dec); err != nil {
		return fail("INVALID_DATA", "command data must contain exactly one JSON object")
	}
	return nil
}

func requireEmptyData(raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte("{}")) {
		return nil
	}
	return fail("INVALID_DATA", "this command does not accept data")
}

func ensureEOF(dec *json.Decoder) error {
	var extra any
	err := dec.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("extra JSON value")
	}
	return err
}

func DefaultDataDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find user configuration directory: %w", err)
	}
	return filepath.Join(base, "ScopeNest"), nil
}
