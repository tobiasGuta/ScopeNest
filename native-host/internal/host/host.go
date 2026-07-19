package host

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/scopenest/scopenest/native-host/internal/browser"
	"github.com/scopenest/scopenest/native-host/internal/certstore"
	"github.com/scopenest/scopenest/native-host/internal/model"
	"github.com/scopenest/scopenest/native-host/internal/protocol"
	"github.com/scopenest/scopenest/native-host/internal/security"
	"github.com/scopenest/scopenest/native-host/internal/store"
)

const (
	HostVersion          = "1.0.4"
	launchReservationTTL = 2 * time.Minute
)

var allowedCommands = map[string]bool{
	"ping": true, "get_status": true, "list_containers": true,
	"create_container": true, "update_container": true, "launch_container": true,
	"close_container": true, "delete_container": true, "create_temporary_container": true,
	"cleanup_temporary_containers": true, "get_running_containers": true,
	"validate_browser_path": true,
	"list_proxy_profiles":   true, "create_proxy_profile": true, "update_proxy_profile": true,
	"delete_proxy_profile": true, "test_proxy_listener": true,
	"list_certificates": true, "import_certificate": true, "get_certificate": true,
	"install_certificate_trust": true, "remove_certificate_trust": true, "delete_certificate": true,
	"acknowledge_manual_certificate_trust": true,
	"get_container_readiness":              true,
	"list_environment_templates":           true, "create_environment_template": true,
	"update_environment_template": true, "delete_environment_template": true,
}

type Host struct {
	store                  *store.Store
	certManager            *certstore.Manager
	launcher               browser.Launcher
	processes              map[string]browser.Process
	watcher                func(string, browser.Process)
	mu                     sync.Mutex
	now                    func() time.Time
	platform               string
	proxyDial              func(string, string, time.Duration) (net.Conn, error)
	startupCleanup         func() error
	startupCleanupOnce     sync.Once
	startupCleanupMu       sync.RWMutex
	startupCleanupMetadata startupCleanupMetadata
}

type startupCleanupMetadata struct {
	State      string     `json:"state"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	ErrorCode  string     `json:"errorCode,omitempty"`
}

type containerInput struct {
	Name                  string `json:"name"`
	Color                 string `json:"color"`
	Icon                  string `json:"icon,omitempty"`
	BrowserType           string `json:"browserType"`
	BrowserExecutable     string `json:"browserExecutable,omitempty"`
	NetworkMode           string `json:"networkMode,omitempty"`
	ProxyProfileID        string `json:"proxyProfileId,omitempty"`
	EnvironmentTemplateID string `json:"environmentTemplateId,omitempty"`
}

type idInput struct {
	ID string `json:"id"`
}

type updateInput struct {
	ID                    string `json:"id"`
	Name                  string `json:"name"`
	Color                 string `json:"color"`
	Icon                  string `json:"icon,omitempty"`
	BrowserType           string `json:"browserType"`
	BrowserExecutable     string `json:"browserExecutable,omitempty"`
	NetworkMode           string `json:"networkMode,omitempty"`
	ProxyProfileID        string `json:"proxyProfileId,omitempty"`
	EnvironmentTemplateID string `json:"environmentTemplateId,omitempty"`
}

type launchInput struct {
	ID  string `json:"id"`
	URL string `json:"url,omitempty"`
}

type launchPolicy struct {
	expectedName        string
	requireStandardType bool
}

type validatePathInput struct {
	Path string `json:"path"`
}

func New(st *store.Store, launcher browser.Launcher, certManager *certstore.Manager) *Host {
	h := &Host{
		store:                  st,
		certManager:            certManager,
		launcher:               launcher,
		processes:              map[string]browser.Process{},
		now:                    func() time.Time { return time.Now().UTC() },
		platform:               runtime.GOOS,
		proxyDial:              net.DialTimeout,
		startupCleanupMetadata: startupCleanupMetadata{State: "pending"},
	}
	h.watcher = h.watch
	h.startupCleanup = func() error {
		if h.certManager != nil {
			h.certManager.CleanupStaging()
			if err := h.reconcileCertificateDeletions(); err != nil {
				return err
			}
			if err := h.reconcileTrustOperations(); err != nil {
				return err
			}
		}
		_, err := h.cleanup()
		return err
	}
	return h
}

// StartStartupCleanup schedules the startup cleanup exactly once and returns
// immediately so native messaging remains responsive while profiles are removed.
func (h *Host) StartStartupCleanup() {
	h.startupCleanupOnce.Do(func() {
		startedAt := h.now()
		h.startupCleanupMu.Lock()
		h.startupCleanupMetadata = startupCleanupMetadata{State: "running", StartedAt: &startedAt}
		h.startupCleanupMu.Unlock()

		go func() {
			err := h.startupCleanup()
			finishedAt := h.now()

			h.startupCleanupMu.Lock()
			h.startupCleanupMetadata.FinishedAt = &finishedAt
			if err != nil {
				h.startupCleanupMetadata.State = "failed"
				h.startupCleanupMetadata.ErrorCode = "INTERNAL_ERROR"
			} else {
				h.startupCleanupMetadata.State = "completed"
			}
			h.startupCleanupMu.Unlock()
		}()
	})
}

func (h *Host) startupCleanupStatus() startupCleanupMetadata {
	h.startupCleanupMu.RLock()
	defer h.startupCleanupMu.RUnlock()
	return h.startupCleanupMetadata
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
	return commandResponse(req, data, err)
}

// LaunchForMCP applies MCP-only identity and browser restrictions while the
// current container record is reserved for launch under the store lock.
func (h *Host) LaunchForMCP(id, expectedName, url string) protocol.Response {
	req := protocol.Request{Version: protocol.Version, Command: "launch_container"}
	data, err := h.launchWithPolicy(launchInput{ID: id, URL: url}, launchPolicy{
		expectedName:        expectedName,
		requireStandardType: true,
	})
	return commandResponse(req, data, err)
}

func commandResponse(req protocol.Request, data any, err error) protocol.Response {
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
		return map[string]any{"hostVersion": HostVersion, "protocolVersion": protocol.Version, "startupCleanup": h.startupCleanupStatus()}, nil
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
	case "list_certificates":
		if err := requireEmptyData(req.Data); err != nil {
			return nil, err
		}
		return h.listCertificates()
	case "import_certificate":
		return h.importCertificate(req.Data)
	case "get_certificate":
		var in idInput
		if err := decodeData(req.Data, &in); err != nil {
			return nil, err
		}
		return h.getCertificate(in.ID)
	case "install_certificate_trust":
		var in idInput
		if err := decodeData(req.Data, &in); err != nil {
			return nil, err
		}
		return h.installCertificateTrust(in.ID)
	case "remove_certificate_trust":
		var in idInput
		if err := decodeData(req.Data, &in); err != nil {
			return nil, err
		}
		return h.removeCertificateTrust(in.ID)
	case "acknowledge_manual_certificate_trust":
		return h.acknowledgeManualCertificateTrust(req.Data)
	case "delete_certificate":
		var in idInput
		if err := decodeData(req.Data, &in); err != nil {
			return nil, err
		}
		return h.deleteCertificate(in.ID)
	case "get_container_readiness":
		var in idInput
		if err := decodeData(req.Data, &in); err != nil {
			return nil, err
		}
		if err := security.ValidateID(in.ID); err != nil {
			return nil, fail("INVALID_CONTAINER_ID", "%v", err)
		}
		return h.getContainerReadiness(in.ID)
	case "list_proxy_profiles":
		if err := requireEmptyData(req.Data); err != nil {
			return nil, err
		}
		return h.listProxyProfiles()
	case "create_proxy_profile":
		return h.createProxyProfile(req.Data)
	case "update_proxy_profile":
		return h.updateProxyProfile(req.Data)
	case "delete_proxy_profile":
		var in idInput
		if err := decodeData(req.Data, &in); err != nil {
			return nil, err
		}
		return h.deleteProxyProfile(in.ID)
	case "test_proxy_listener":
		return h.testProxyListener(req.Data)
	case "list_environment_templates":
		if err := requireEmptyData(req.Data); err != nil {
			return nil, err
		}
		return h.listEnvironmentTemplates()
	case "create_environment_template":
		return h.createEnvironmentTemplate(req.Data)
	case "update_environment_template":
		return h.updateEnvironmentTemplate(req.Data)
	case "delete_environment_template":
		var in idInput
		if err := decodeData(req.Data, &in); err != nil {
			return nil, err
		}
		return h.deleteEnvironmentTemplate(in.ID)
	default:
		return nil, fail("UNKNOWN_COMMAND", "command is not supported")
	}
}

func (h *Host) status() (any, error) {
	if err := h.reconcile(); err != nil {
		return nil, err
	}
	db, err := h.store.Load()
	if err != nil {
		return nil, err
	}
	caps := map[string]any{
		"trustInstallation":         false,
		"manualTrustAcknowledgment": h.platform == "linux",
	}
	if h.certManager != nil && h.certManager.Trust.Supported() {
		caps["trustInstallation"] = true
	}
	savedCount, temporaryCount, runningCount, pendingCleanupCount := 0, 0, 0, 0
	for _, container := range db.Containers {
		if container.Temporary {
			temporaryCount++
		} else {
			savedCount++
		}
		if container.State == model.StateRunning {
			runningCount++
		}
		if container.PendingCleanup {
			pendingCleanupCount++
		}
	}
	result := map[string]any{
		"hostVersion":             HostVersion,
		"protocolVersion":         protocol.Version,
		"dataDirectory":           h.store.Root(),
		"containerCount":          len(db.Containers),
		"savedContainerCount":     savedCount,
		"temporaryContainerCount": temporaryCount,
		"runningContainerCount":   runningCount,
		"pendingCleanupCount":     pendingCleanupCount,
		"detectedBrowsers":        browser.Detect(),
		"startupCleanup":          h.startupCleanupStatus(),
		"capabilities":            caps,
		"platform":                h.platform,
		"brokenReferences":        store.BrokenReferences(db),
	}
	if h.certManager != nil {
		if issues, auditErr := h.certManager.AuditResources(); auditErr == nil {
			result["certificateResourceIssues"] = issues
		}
	}
	return result, nil
}

func (h *Host) reconcile() error {
	return h.store.Update(func(db *model.Database) error {
		now := h.now()
		for i := range db.Containers {
			c := &db.Containers[i]
			switch c.State {
			case model.StateLaunching:
				reservedAt := c.UpdatedAt
				if c.LaunchReservedAt != nil {
					reservedAt = *c.LaunchReservedAt
				}
				if now.Sub(reservedAt) > launchReservationTTL {
					inUse, err := h.store.ProfileInUse(c.ID)
					if err != nil {
						return err
					}
					if !inUse {
						setStopped(c, now)
					}
				}
			case model.StateRunning:
				if h.ownsMatchingProcess(c.ID, c.PID) {
					continue
				}
				inUse, err := h.store.ProfileInUse(c.ID)
				if err != nil {
					return err
				}
				if !inUse {
					setStopped(c, now)
				}
			}
		}
		return nil
	})
}

func (h *Host) ownsMatchingProcess(id string, pid int) bool {
	if pid <= 0 {
		return false
	}
	h.mu.Lock()
	process := h.processes[id]
	h.mu.Unlock()
	return process != nil && process.PID() == pid
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
		if !runningOnly || c.State == model.StateRunning {
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
	if strings.TrimSpace(in.BrowserExecutable) == "" && in.BrowserType != "custom" {
		for _, candidate := range browser.Detect() {
			if candidate.Type == in.BrowserType {
				in.BrowserExecutable = candidate.Path
				break
			}
		}
	}
	path, err := security.ValidateBrowserExecutable(in.BrowserExecutable, in.BrowserType)
	if err != nil {
		return in, fail("INVALID_BROWSER_PATH", "%v", err)
	}
	in.BrowserExecutable = path

	if in.NetworkMode == "" {
		in.NetworkMode = "direct"
	}
	if err := security.ValidateNetworkMode(in.NetworkMode); err != nil {
		return in, fail("INVALID_NETWORK_MODE", "%v", err)
	}
	if in.ProxyProfileID != "" {
		if err := security.ValidateID(in.ProxyProfileID); err != nil {
			return in, fail("INVALID_PROXY_PROFILE_ID", "%v", err)
		}
	}
	if in.EnvironmentTemplateID != "" {
		if err := security.ValidateID(in.EnvironmentTemplateID); err != nil {
			return in, fail("INVALID_TEMPLATE_ID", "%v", err)
		}
	}

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
	c := model.Container{ID: id, Name: in.Name, Color: in.Color, Icon: in.Icon, CreatedAt: now, UpdatedAt: now, Temporary: temporary, ProfilePath: profile, BrowserType: in.BrowserType, BrowserExecutable: in.BrowserExecutable, State: model.StateStopped, NetworkMode: in.NetworkMode, ProxyProfileID: in.ProxyProfileID, EnvironmentTemplateID: in.EnvironmentTemplateID}
	if err := h.store.Update(func(db *model.Database) error {
		if err := validateContainerReferences(db, in); err != nil {
			return err
		}
		db.Containers = append(db.Containers, c)
		return nil
	}); err != nil {
		_ = h.store.RemoveContainerDirectory(id)
		return model.Container{}, err
	}
	return c, nil
}

func (h *Host) update(in updateInput) (model.Container, error) {
	if err := security.ValidateID(in.ID); err != nil {
		return model.Container{}, fail("INVALID_CONTAINER_ID", "%v", err)
	}
	validated, err := validateContainerInput(containerInput{Name: in.Name, Color: in.Color, Icon: in.Icon, BrowserType: in.BrowserType, BrowserExecutable: in.BrowserExecutable, NetworkMode: in.NetworkMode, ProxyProfileID: in.ProxyProfileID, EnvironmentTemplateID: in.EnvironmentTemplateID})
	if err != nil {
		return model.Container{}, err
	}
	var result model.Container
	err = h.store.Update(func(db *model.Database) error {
		if err := validateContainerReferences(db, containerInput{Name: validated.Name, Color: validated.Color, Icon: validated.Icon, BrowserType: validated.BrowserType, BrowserExecutable: validated.BrowserExecutable, NetworkMode: validated.NetworkMode, ProxyProfileID: validated.ProxyProfileID, EnvironmentTemplateID: validated.EnvironmentTemplateID}); err != nil {
			return err
		}
		for i := range db.Containers {
			if db.Containers[i].ID == in.ID {
				c := &db.Containers[i]
				c.Name, c.Color, c.Icon = validated.Name, validated.Color, validated.Icon
				c.BrowserType, c.BrowserExecutable = validated.BrowserType, validated.BrowserExecutable
				c.NetworkMode, c.ProxyProfileID, c.EnvironmentTemplateID = validated.NetworkMode, validated.ProxyProfileID, validated.EnvironmentTemplateID
				c.UpdatedAt, result = h.now(), *c
				return nil
			}
		}
		return fail("NOT_FOUND", "container was not found")
	})
	return result, err
}

func (h *Host) launch(in launchInput) (model.Container, error) {
	return h.launchWithPolicy(in, launchPolicy{})
}

func (h *Host) launchWithPolicy(in launchInput, policy launchPolicy) (model.Container, error) {
	if err := security.ValidateID(in.ID); err != nil {
		return model.Container{}, fail("INVALID_CONTAINER_ID", "%v", err)
	}
	validatedURL, err := security.ValidateURL(in.URL)
	if err != nil {
		return model.Container{}, fail("INVALID_URL", "%v", err)
	}
	c, token, effective, certReadiness, err := h.reserveLaunchWithEnvironment(in.ID, policy)
	if err != nil {
		return model.Container{}, err
	}
	releaseReservation := func() { _ = h.releaseLaunchReservation(c.ID, token) }

	profile, err := h.store.EnsureProfile(c.ID)
	if err != nil {
		releaseReservation()
		return model.Container{}, err
	}
	executable, err := security.ValidateBrowserExecutable(c.BrowserExecutable, c.BrowserType)
	if err != nil {
		releaseReservation()
		return model.Container{}, fail("INVALID_BROWSER_PATH", "%v", err)
	}

	proxyOpts := browser.ProxyOptions{Enabled: false}
	var proxyWarning *model.ProxyLaunchWarning
	directFallbackUsed := false
	_ = certReadiness
	if effective.EffectiveMode == "proxy" && effective.ProxyProfile != nil {
		proxyProfile := effective.ProxyProfile
		proxyOpts = browser.ProxyOptions{Enabled: true, Protocol: proxyProfile.Protocol, Host: proxyProfile.Host, Port: proxyProfile.Port, BypassRules: proxyProfile.BypassRules}
		if proxyProfile.HealthCheck.Enabled {
			listener := h.checkProxyListener(proxyProfile.Host, proxyProfile.Port, durationFromTimeout(proxyProfile.HealthCheck.TimeoutMs))
			if !listener.Reachable {
				switch proxyProfile.UnavailableBehavior {
				case "warn":
					proxyWarning = &model.ProxyLaunchWarning{Code: "PROXY_LISTENER_UNAVAILABLE", Message: "proxy listener is unavailable; launching with the configured proxy and no direct fallback", Host: proxyProfile.Host, Port: proxyProfile.Port}
				case "block":
					releaseReservation()
					return model.Container{}, fail("PROXY_LISTENER_UNAVAILABLE", "proxy listener %s:%d is unavailable (%s)", proxyProfile.Host, proxyProfile.Port, listener.ErrorCode)
				case "direct":
					proxyOpts = browser.ProxyOptions{Enabled: false}
					directFallbackUsed = true
				default:
					releaseReservation()
					return model.Container{}, fail("INVALID_PROXY_PROFILE", "proxy unavailable behavior is invalid")
				}
			}
		}
	}

	identity := browser.VisualIdentity{Name: c.Name, Color: c.Color, Icon: c.Icon}
	args, err := browser.Arguments(browser.ArgumentOptions{
		ProfilePath: profile,
		URL:         validatedURL,
		Proxy:       proxyOpts,
		Identity:    identity,
	})
	if err != nil {
		releaseReservation()
		return model.Container{}, fail("INVALID_BROWSER_ARGUMENTS", "%v", err)
	}
	process, err := h.launcher.Start(browser.LaunchSpec{
		Executable: executable,
		Arguments:  args,
		Identity:   identity,
	})
	if err != nil {
		releaseReservation()
		return model.Container{}, fail("LAUNCH_FAILED", "browser could not be started: %v", err)
	}
	h.mu.Lock()
	h.processes[c.ID] = process
	h.mu.Unlock()
	now := h.now()
	var launched model.Container
	if err := h.store.Update(func(db *model.Database) error {
		for i := range db.Containers {
			container := &db.Containers[i]
			if container.ID == c.ID {
				if container.State != model.StateLaunching || container.LaunchToken != token {
					return fail("LAUNCH_RESERVATION_LOST", "container launch reservation is no longer valid")
				}
				container.State = model.StateRunning
				container.Running = true
				container.PID = process.PID()
				container.LaunchToken = ""
				container.LaunchReservedAt = nil
				container.LastLaunchedAt = &now
				container.UpdatedAt = now
				container.PendingCleanup = false
				launched = *container
				return nil
			}
		}
		return fail("NOT_FOUND", "container was not found")
	}); err != nil {
		h.mu.Lock()
		if h.processes[c.ID] == process {
			delete(h.processes, c.ID)
		}
		h.mu.Unlock()
		_ = process.Terminate()
		_ = process.Wait()
		return model.Container{}, err
	}
	go h.watcher(c.ID, process)
	launched.ProxyWarning = proxyWarning
	launched.DirectFallbackUsed = directFallbackUsed
	return launched, nil
}

func (h *Host) reserveLaunch(id string) (model.Container, string, error) {
	container, token, _, _, err := h.reserveLaunchWithEnvironment(id, launchPolicy{})
	return container, token, err
}

func (h *Host) reserveLaunchWithEnvironment(id string, policy launchPolicy) (model.Container, string, effectiveEnvironment, []certificateReadiness, error) {
	h.mu.Lock()
	ownedProcess := h.processes[id]
	h.mu.Unlock()
	if ownedProcess != nil && ownedProcess.Running() {
		return model.Container{}, "", effectiveEnvironment{}, nil, fail("ALREADY_RUNNING", "container is already running")
	}
	token, err := security.NewID()
	if err != nil {
		return model.Container{}, "", effectiveEnvironment{}, nil, err
	}
	now := h.now()
	var reserved model.Container
	var effective effectiveEnvironment
	var certs []certificateReadiness
	err = h.store.Update(func(db *model.Database) error {
		for i := range db.Containers {
			container := &db.Containers[i]
			if container.ID != id {
				continue
			}
			if policy.expectedName != "" && container.Name != policy.expectedName {
				return fail("CONTAINER_NAME_MISMATCH", "container name changed")
			}
			if policy.requireStandardType && !isStandardBrowserType(container.BrowserType) {
				return fail("CUSTOM_BROWSER_REQUIRES_HUMAN_LAUNCH", "custom browser requires human launch")
			}
			if container.State == model.StateLaunching {
				reservedAt := container.UpdatedAt
				if container.LaunchReservedAt != nil {
					reservedAt = *container.LaunchReservedAt
				}
				if now.Sub(reservedAt) <= launchReservationTTL {
					return fail("ALREADY_LAUNCHING", "container launch is already in progress")
				}
				setStopped(container, now)
			}
			resolved, err := resolveEffectiveEnvironment(db, *container)
			if err != nil {
				return err
			}
			readyCerts, err := h.certificateReadiness(db, resolved.RequiredCertificateIDs)
			if err != nil {
				return err
			}
			wasRunning := container.State == model.StateRunning || container.Running
			inUse, err := h.store.ProfileInUse(container.ID)
			if err != nil {
				return err
			}
			if inUse {
				if wasRunning {
					return fail("ALREADY_RUNNING", "container profile is already in use")
				}
				return fail("PROFILE_IN_USE", "container profile is already in use")
			}
			if wasRunning {
				setStopped(container, now)
			}
			container.State = model.StateLaunching
			container.Running = false
			container.PID = 0
			container.LaunchToken = token
			container.LaunchReservedAt = &now
			container.UpdatedAt = now
			container.PendingCleanup = false
			reserved = *container
			effective = resolved
			certs = readyCerts
			return nil
		}
		return fail("NOT_FOUND", "container was not found")
	})
	return reserved, token, effective, certs, err
}

func isStandardBrowserType(browserType string) bool {
	switch browserType {
	case "chrome", "chromium", "edge", "brave":
		return true
	default:
		return false
	}
}

func (h *Host) releaseLaunchReservation(id, token string) error {
	return h.store.Update(func(db *model.Database) error {
		for i := range db.Containers {
			container := &db.Containers[i]
			if container.ID == id && container.State == model.StateLaunching && container.LaunchToken == token {
				setStopped(container, h.now())
				return nil
			}
		}
		return nil
	})
}

func setStopped(container *model.Container, now time.Time) {
	container.State = model.StateStopped
	container.Running = false
	container.PID = 0
	container.LaunchToken = ""
	container.LaunchReservedAt = nil
	container.UpdatedAt = now
}

func (h *Host) watch(id string, process browser.Process) {
	_ = process.Wait()

	h.mu.Lock()
	if h.processes[id] != process {
		h.mu.Unlock()
		return
	}
	delete(h.processes, id)
	h.mu.Unlock()

	temporary := false
	_ = h.store.Update(func(db *model.Database) error {
		for i := range db.Containers {
			container := &db.Containers[i]
			if container.ID == id && container.State == model.StateRunning && container.PID == process.PID() {
				setStopped(container, h.now())
				temporary = container.Temporary
				return nil
			}
			if container.ID == id && container.State == model.StateStopped && container.PID == 0 {
				temporary = container.Temporary
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
	if err := process.Terminate(); err != nil {
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
	if err := h.store.Update(func(db *model.Database) error {
		for i := range db.Containers {
			container := &db.Containers[i]
			if container.ID != id {
				continue
			}
			if container.State == model.StateLaunching {
				return fail("CONTAINER_BUSY", "container launch is in progress")
			}
			wasRunning := container.State == model.StateRunning || container.Running
			if wasRunning && h.ownsMatchingProcess(container.ID, container.PID) {
				return fail("CONTAINER_RUNNING", "close the container before deleting it")
			}
			inUse, err := h.store.ProfileInUse(id)
			if err != nil {
				return err
			}
			if inUse {
				if wasRunning {
					return fail("CONTAINER_RUNNING", "close the container before deleting it")
				}
				return fail("PROFILE_IN_USE", "the browser profile is still in use")
			}
			if wasRunning {
				setStopped(container, h.now())
			}
			if err := h.store.RemoveContainerDirectory(id); err != nil {
				return fail("DELETE_FAILED", "container profile could not be removed: %v", err)
			}
			db.Containers = append(db.Containers[:i], db.Containers[i+1:]...)
			return nil
		}
		return fail("NOT_FOUND", "container was not found")
	}); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": true, "id": id}, nil
}

func (h *Host) deleteTemporary(id string) (bool, error) {
	removed := false
	var outcomeErr error
	err := h.store.Update(func(db *model.Database) error {
		for i := range db.Containers {
			container := &db.Containers[i]
			if container.ID != id {
				continue
			}
			if container.State != model.StateStopped || container.Running {
				return fail("CONTAINER_BUSY", "temporary container is not stopped")
			}
			inUse, err := h.store.ProfileInUse(id)
			if err != nil {
				return err
			}
			if inUse {
				container.PendingCleanup = true
				outcomeErr = fail("PROFILE_IN_USE", "temporary profile is still in use")
				return nil
			}
			if err := h.store.RemoveContainerDirectory(id); err != nil {
				container.PendingCleanup = true
				outcomeErr = err
				return nil
			}
			db.Containers = append(db.Containers[:i], db.Containers[i+1:]...)
			removed = true
			return nil
		}
		// Another host may already have completed cleanup.
		removed = true
		return nil
	})
	if err != nil {
		return false, err
	}
	if outcomeErr != nil {
		return false, outcomeErr
	}
	return removed, nil
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
		if c.Temporary && c.State == model.StateStopped && !c.Running {
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
