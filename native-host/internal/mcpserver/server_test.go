package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/scopenest/scopenest/native-host/internal/browser"
	"github.com/scopenest/scopenest/native-host/internal/host"
	"github.com/scopenest/scopenest/native-host/internal/model"
	"github.com/scopenest/scopenest/native-host/internal/protocol"
	"github.com/scopenest/scopenest/native-host/internal/store"
)

const testContainerID = "11111111111111111111111111111111"

type recordedCall struct {
	request protocol.Request
	data    map[string]any
}

type fakeHandler struct {
	mu            sync.Mutex
	calls         []recordedCall
	cleanupCalls  int
	failCommand   string
	maxActive     int32
	active        int32
	delay         time.Duration
	containerName string
	dataOverride  map[string]any
}

func (f *fakeHandler) Handle(request protocol.Request) protocol.Response {
	active := atomic.AddInt32(&f.active, 1)
	for {
		maximum := atomic.LoadInt32(&f.maxActive)
		if active <= maximum || atomic.CompareAndSwapInt32(&f.maxActive, maximum, active) {
			break
		}
	}
	defer atomic.AddInt32(&f.active, -1)
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	var data map[string]any
	_ = json.Unmarshal(request.Data, &data)
	f.mu.Lock()
	f.calls = append(f.calls, recordedCall{request: request, data: data})
	f.mu.Unlock()
	if request.Command == f.failCommand {
		return protocol.NewError(request, "PROXY_LISTENER_UNAVAILABLE", "sensitive internal detail")
	}
	return protocol.NewSuccess(request, f.responseData(request.Command))
}

func (f *fakeHandler) responseData(command string) any {
	if f.dataOverride != nil {
		if value, ok := f.dataOverride[command]; ok {
			return value
		}
	}
	name := f.containerName
	if name == "" {
		name = "Target - User A"
	}
	container := map[string]any{
		"id": testContainerID, "name": name, "color": "#725cff", "browserType": "chrome",
		"temporary": false, "pendingCleanup": false, "running": false, "state": "stopped",
		"createdAt": "2026-07-18T00:00:00Z", "updatedAt": "2026-07-18T00:00:00Z", "networkMode": "direct",
	}
	switch command {
	case "ping":
		return map[string]any{"hostVersion": host.HostVersion, "protocolVersion": protocol.Version, "startupCleanup": map[string]any{"state": "pending"}}
	case "get_status":
		return map[string]any{"hostVersion": host.HostVersion, "protocolVersion": protocol.Version, "platform": "windows", "containerCount": 1, "savedContainerCount": 1, "runningContainerCount": 0, "detectedBrowsers": []any{}, "startupCleanup": map[string]any{"state": "pending"}, "capabilities": map[string]any{}}
	case "list_containers", "get_running_containers":
		return []any{container}
	case "list_proxy_profiles", "list_environment_templates":
		return []any{}
	case "get_container_readiness":
		return map[string]any{"ready": true, "network": map[string]any{"requestedMode": "direct", "effectiveMode": "direct", "requiredCertificateIds": []any{}}, "listener": map[string]any{}, "certificates": []any{}, "warnings": []any{}}
	default:
		return container
	}
}

func (f *fakeHandler) StartStartupCleanup() {
	f.mu.Lock()
	f.cleanupCalls++
	f.mu.Unlock()
}

func (f *fakeHandler) snapshot() ([]recordedCall, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedCall(nil), f.calls...), f.cleanupCalls
}

func connectClient(t *testing.T, handler CommandHandler) (*mcp.ClientSession, func()) {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := New(handler).Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "scopenest-test", Version: "1"}, nil)
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	return clientSession, func() {
		_ = clientSession.Close()
		_ = serverSession.Wait()
	}
}

func callTool(t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestRegisteredToolsAndSchemas(t *testing.T) {
	handler := &fakeHandler{}
	session, closeSession := connectClient(t, handler)
	defer closeSession()
	type annotationExpectation struct {
		readOnly, destructive, idempotent, openWorld bool
	}
	expected := map[string]annotationExpectation{
		"scopenest_ping":                       {true, false, true, false},
		"scopenest_get_status":                 {true, false, true, false},
		"scopenest_list_containers":            {true, false, true, false},
		"scopenest_list_running_containers":    {true, false, true, false},
		"scopenest_list_proxy_profiles":        {true, false, true, false},
		"scopenest_list_environment_templates": {true, false, true, false},
		"scopenest_get_container_readiness":    {true, false, true, false},
		"scopenest_create_container":           {false, false, false, false},
		"scopenest_create_temporary_container": {false, false, false, false},
		"scopenest_launch_container":           {false, true, false, true},
		"scopenest_close_container":            {false, true, false, false},
	}
	seen := map[string]bool{}
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		seen[tool.Name] = true
		want, known := expected[tool.Name]
		if !known {
			t.Errorf("unexpected tool %q", tool.Name)
			continue
		}
		if !strings.HasPrefix(tool.Name, "scopenest_") || tool.Description == "" {
			t.Errorf("invalid tool metadata: %#v", tool)
		}
		schema, ok := tool.InputSchema.(map[string]any)
		if !ok || schema["additionalProperties"] != false {
			t.Errorf("tool %s is not closed-schema: %#v", tool.Name, tool.InputSchema)
		}
		if tool.Annotations == nil || tool.Annotations.DestructiveHint == nil || tool.Annotations.OpenWorldHint == nil {
			t.Errorf("tool %s has incomplete annotations: %#v", tool.Name, tool.Annotations)
		} else if tool.Annotations.ReadOnlyHint != want.readOnly || *tool.Annotations.DestructiveHint != want.destructive || tool.Annotations.IdempotentHint != want.idempotent || *tool.Annotations.OpenWorldHint != want.openWorld {
			t.Errorf("tool %s annotations = %#v, want %#v", tool.Name, tool.Annotations, want)
		}
		if tool.Name == "scopenest_create_container" || tool.Name == "scopenest_create_temporary_container" {
			assertRequired(t, tool.Name, schema, "name", "color", "browserType", "networkMode")
			properties, ok := schema["properties"].(map[string]any)
			if !ok {
				t.Fatalf("tool %s has invalid properties: %#v", tool.Name, schema)
			}
			if _, exposed := properties["browserExecutable"]; exposed {
				t.Errorf("tool %s exposes browserExecutable", tool.Name)
			}
			browserType, ok := properties["browserType"].(map[string]any)
			if !ok || !jsonEqual(browserType["enum"], stringsToAny(mcpBrowserTypes)) {
				t.Errorf("tool %s browser enum = %#v", tool.Name, browserType["enum"])
			}
		}
		if tool.Name == "scopenest_launch_container" || tool.Name == "scopenest_close_container" {
			assertRequired(t, tool.Name, schema, "id", "expectedName")
		}
	}
	if len(seen) != len(expected) {
		t.Fatalf("registered tools = %v", seen)
	}
	for name := range expected {
		if !seen[name] {
			t.Errorf("missing tool %s", name)
		}
	}
	for _, forbidden := range []string{"delete_container", "cleanup_temporary_containers", "update_container", "create_proxy_profile", "update_proxy_profile", "delete_proxy_profile", "import_certificate", "install_certificate_trust", "remove_certificate_trust", "delete_certificate", "acknowledge_manual_certificate_trust", "create_environment_template", "update_environment_template", "delete_environment_template", "validate_browser_path", "execute"} {
		for name := range seen {
			if strings.Contains(name, forbidden) {
				t.Errorf("forbidden tool registered: %s", name)
			}
		}
	}
}

func assertRequired(t *testing.T, toolName string, schema map[string]any, values ...string) {
	t.Helper()
	required, ok := schema["required"].([]any)
	if !ok {
		t.Fatalf("tool %s has no required array: %#v", toolName, schema)
	}
	seen := map[string]bool{}
	for _, item := range required {
		if value, ok := item.(string); ok {
			seen[value] = true
		}
	}
	for _, value := range values {
		if !seen[value] {
			t.Errorf("tool %s does not require %s", toolName, value)
		}
	}
}

func TestToolMappingsAndRequestEnvelope(t *testing.T) {
	tests := []struct {
		tool, command string
		args          map[string]any
		expectedData  map[string]any
	}{
		{"scopenest_ping", "ping", map[string]any{}, map[string]any{}},
		{"scopenest_get_status", "get_status", map[string]any{}, map[string]any{}},
		{"scopenest_list_containers", "list_containers", map[string]any{}, map[string]any{}},
		{"scopenest_list_running_containers", "get_running_containers", map[string]any{}, map[string]any{}},
		{"scopenest_list_proxy_profiles", "list_proxy_profiles", map[string]any{}, map[string]any{}},
		{"scopenest_list_environment_templates", "list_environment_templates", map[string]any{}, map[string]any{}},
		{"scopenest_get_container_readiness", "get_container_readiness", map[string]any{"id": testContainerID}, map[string]any{"id": testContainerID}},
		{"scopenest_create_container", "create_container", createArgs(), createArgs()},
		{"scopenest_create_temporary_container", "create_temporary_container", createArgs(), createArgs()},
		{"scopenest_launch_container", "launch_container", map[string]any{"id": testContainerID, "expectedName": "Target - User A", "url": "https://example.com"}, map[string]any{"id": testContainerID, "url": "https://example.com"}},
		{"scopenest_close_container", "close_container", map[string]any{"id": testContainerID, "expectedName": "Target - User A"}, map[string]any{"id": testContainerID}},
	}
	requestIDs := map[string]bool{}
	for _, test := range tests {
		t.Run(test.tool, func(t *testing.T) {
			handler := &fakeHandler{}
			session, closeSession := connectClient(t, handler)
			defer closeSession()
			result := callTool(t, session, test.tool, test.args)
			if result.IsError {
				t.Fatalf("tool failed: %#v", result.Content)
			}
			calls, cleanupCalls := handler.snapshot()
			if cleanupCalls != 1 {
				t.Fatalf("startup cleanup calls = %d", cleanupCalls)
			}
			var call recordedCall
			for _, candidate := range calls {
				if candidate.request.Command == test.command {
					call = candidate
				}
			}
			if call.request.Command == "" {
				t.Fatalf("command %s was not called: %#v", test.command, calls)
			}
			if call.request.Version != protocol.Version || call.request.RequestID == "" {
				t.Fatalf("invalid request: %#v", call.request)
			}
			if requestIDs[call.request.RequestID] {
				t.Fatalf("duplicate request ID %q", call.request.RequestID)
			}
			requestIDs[call.request.RequestID] = true
			if !jsonEqual(call.data, test.expectedData) {
				t.Fatalf("data = %#v, want %#v", call.data, test.expectedData)
			}
		})
	}
}

func createArgs() map[string]any {
	return map[string]any{"name": "Target - User A", "color": "#725cff", "browserType": "chrome", "networkMode": "direct"}
}

func jsonEqual(a, b any) bool {
	aJSON, _ := json.Marshal(a)
	bJSON, _ := json.Marshal(b)
	return string(aJSON) == string(bJSON)
}

func TestUnknownArgumentsRejectedBeforeHost(t *testing.T) {
	handler := &fakeHandler{}
	session, closeSession := connectClient(t, handler)
	defer closeSession()
	result := callTool(t, session, "scopenest_ping", map[string]any{"unexpected": true})
	if !result.IsError {
		t.Fatal("unknown property was accepted")
	}
	calls, cleanupCalls := handler.snapshot()
	if len(calls) != 0 {
		t.Fatalf("host was called: %#v", calls)
	}
	if cleanupCalls != 0 {
		t.Fatalf("cleanup scheduled for invalid input")
	}
}

func TestStrictDecodeRequiresJSONObject(t *testing.T) {
	for _, raw := range []string{"null", "[]", `"string"`, "123", "true"} {
		t.Run(raw, func(t *testing.T) {
			if err := strictDecode(json.RawMessage(raw), &emptyInput{}); err == nil {
				t.Fatalf("accepted non-object arguments %s", raw)
			}
		})
	}
	for _, raw := range []string{"", "  \t\r\n", "{}", " \n {} \t"} {
		if err := strictDecode(json.RawMessage(raw), &emptyInput{}); err != nil {
			t.Errorf("rejected object arguments %q: %v", raw, err)
		}
	}
}

func TestCustomBrowserCreationInputsRejectedBeforeHost(t *testing.T) {
	tests := []map[string]any{
		{"name": "Custom", "color": "#725cff", "browserType": "custom", "networkMode": "direct"},
		{"name": "Path", "color": "#725cff", "browserType": "chrome", "browserExecutable": "C:/local/chrome.exe", "networkMode": "direct"},
	}
	for _, args := range tests {
		handler := &fakeHandler{}
		session, closeSession := connectClient(t, handler)
		result := callTool(t, session, "scopenest_create_container", args)
		closeSession()
		if !result.IsError {
			t.Fatalf("unsafe browser input was accepted: %#v", args)
		}
		calls, cleanupCalls := handler.snapshot()
		if len(calls) != 0 || cleanupCalls != 0 {
			t.Fatalf("unsafe browser input reached host: calls=%d cleanup=%d", len(calls), cleanupCalls)
		}
	}
}

func TestMissingRequiredIdentityFieldRejectedBeforeHost(t *testing.T) {
	handler := &fakeHandler{}
	session, closeSession := connectClient(t, handler)
	defer closeSession()
	result := callTool(t, session, "scopenest_launch_container", map[string]any{"expectedName": "Target - User A"})
	if !result.IsError {
		t.Fatal("missing ID was accepted")
	}
	calls, _ := handler.snapshot()
	if len(calls) != 0 {
		t.Fatalf("host was called: %#v", calls)
	}
}

func TestNoCommandOutsideMCPAllowlistIsReachable(t *testing.T) {
	handler := &fakeHandler{}
	response := NewAdapter(handler).Execute("delete_container", map[string]any{"id": testContainerID})
	if response.Success || response.ErrorCode != "UNKNOWN_COMMAND" {
		t.Fatalf("unexpected response: %#v", response)
	}
	calls, cleanupCalls := handler.snapshot()
	if len(calls) != 0 || cleanupCalls != 0 {
		t.Fatalf("forbidden command reached host or cleanup: calls=%d cleanup=%d", len(calls), cleanupCalls)
	}
}

func TestHostErrorsRemainStructuredToolErrors(t *testing.T) {
	handler := &fakeHandler{failCommand: "get_container_readiness"}
	session, closeSession := connectClient(t, handler)
	defer closeSession()
	result := callTool(t, session, "scopenest_get_container_readiness", map[string]any{"id": testContainerID})
	if !result.IsError {
		t.Fatal("host failure was reported as success")
	}
	raw, _ := json.Marshal(result)
	text := string(raw)
	if !strings.Contains(text, "PROXY_LISTENER_UNAVAILABLE") || strings.Contains(text, "sensitive internal detail") {
		t.Fatalf("unsafe error output: %s", text)
	}
}

func TestOutputRedaction(t *testing.T) {
	container := map[string]any{
		"id": testContainerID, "name": "Redacted", "color": "#725cff", "browserType": "custom", "temporary": false,
		"pendingCleanup": false, "running": true, "state": "running", "createdAt": "2026-07-18T00:00:00Z", "updatedAt": "2026-07-18T00:00:00Z", "networkMode": "direct",
		"profilePath": "C:/secret/profile", "browserExecutable": "C:/secret/chrome.exe", "pid": 4242,
		"launchToken": "secret-launch-token", "launchReservedAt": "2026-07-18T00:00:00Z", "certificateDER": "secret-certificate", "dataDirectory": "C:/secret/data",
	}
	status := map[string]any{
		"hostVersion": host.HostVersion, "protocolVersion": protocol.Version, "platform": "windows", "dataDirectory": "C:/secret/data",
		"containerCount": 1, "detectedBrowsers": []any{map[string]any{"type": "chrome", "name": "Chrome", "path": "C:/secret/chrome.exe"}},
		"startupCleanup": map[string]any{"state": "pending"}, "capabilities": map[string]any{}, "rawMetadata": container,
	}
	handler := &fakeHandler{dataOverride: map[string]any{"list_containers": []any{container}, "get_status": status}}
	session, closeSession := connectClient(t, handler)
	defer closeSession()
	for _, tool := range []string{"scopenest_list_containers", "scopenest_get_status"} {
		result := callTool(t, session, tool, map[string]any{})
		raw, _ := json.Marshal(result)
		text := string(raw)
		for _, secret := range []string{"profilePath", "browserExecutable", "pid", "launchToken", "launchReservedAt", "certificateDER", "dataDirectory", "C:/secret"} {
			if strings.Contains(text, secret) {
				t.Errorf("%s leaked %q: %s", tool, secret, text)
			}
		}
		if !strings.Contains(text, testContainerID) && tool == "scopenest_list_containers" {
			t.Errorf("normal fields were removed: %s", text)
		}
	}
}

func TestNonLoopbackProxyMetadataIsNotExposed(t *testing.T) {
	profile := map[string]any{"id": testContainerID, "name": "Unsafe", "enabled": true, "protocol": "http", "host": "example.com", "port": 8080, "bypassRules": []any{}, "certificateIds": []any{}, "healthCheck": map[string]any{}, "unavailableBehavior": "block"}
	handler := &fakeHandler{dataOverride: map[string]any{"list_proxy_profiles": []any{profile}}}
	session, closeSession := connectClient(t, handler)
	defer closeSession()
	result := callTool(t, session, "scopenest_list_proxy_profiles", map[string]any{})
	if !result.IsError {
		t.Fatal("non-loopback proxy metadata was exposed")
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "example.com") {
		t.Fatalf("non-loopback host leaked: %s", raw)
	}
}

func TestProxyBypassRulesAreNotExposed(t *testing.T) {
	profile := map[string]any{"id": testContainerID, "name": "Local Proxy", "enabled": true, "protocol": "http", "host": "127.0.0.1", "port": 8080, "bypassRules": []any{"sensitive.internal.example"}, "certificateIds": []any{}, "healthCheck": map[string]any{}, "unavailableBehavior": "block"}
	handler := &fakeHandler{dataOverride: map[string]any{"list_proxy_profiles": []any{profile}}}
	session, closeSession := connectClient(t, handler)
	defer closeSession()
	result := callTool(t, session, "scopenest_list_proxy_profiles", map[string]any{})
	if result.IsError {
		t.Fatalf("proxy summary failed: %#v", result.Content)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "bypassRules") || strings.Contains(string(raw), "sensitive.internal.example") {
		t.Fatalf("proxy bypass rules leaked: %s", raw)
	}
}

func TestIdentityConfirmationBlocksMutation(t *testing.T) {
	tests := []struct {
		name, id, expected, wantCode string
		wantMutation                 bool
	}{
		{"match", testContainerID, "Target - User A", "", true},
		{"wrong name", testContainerID, "Target - User B", "CONTAINER_NAME_MISMATCH", false},
		{"unknown ID", "22222222222222222222222222222222", "Target - User A", "NOT_FOUND", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := &fakeHandler{}
			adapter := NewAdapter(handler)
			response := adapter.ExecuteWithIdentity("launch_container", test.id, test.expected, map[string]any{"id": test.id})
			if response.Success != test.wantMutation || response.ErrorCode != test.wantCode {
				t.Fatalf("response = %#v", response)
			}
			calls, _ := handler.snapshot()
			mutations := 0
			for _, call := range calls {
				if call.request.Command == "launch_container" {
					mutations++
				}
			}
			if (mutations == 1) != test.wantMutation {
				t.Fatalf("mutation calls = %d", mutations)
			}
		})
	}
}

func TestCustomBrowserRequiresHumanLaunch(t *testing.T) {
	custom := map[string]any{
		"id": testContainerID, "name": "Custom", "color": "#725cff", "browserType": "custom",
		"temporary": false, "pendingCleanup": false, "running": false, "state": "stopped",
		"createdAt": "2026-07-18T00:00:00Z", "updatedAt": "2026-07-18T00:00:00Z", "networkMode": "direct",
	}
	handler := &fakeHandler{dataOverride: map[string]any{"list_containers": []any{custom}}}
	session, closeSession := connectClient(t, handler)
	defer closeSession()
	result := callTool(t, session, "scopenest_launch_container", map[string]any{"id": testContainerID, "expectedName": "Custom"})
	raw, _ := json.Marshal(result)
	if !result.IsError || !strings.Contains(string(raw), "CUSTOM_BROWSER_REQUIRES_HUMAN_LAUNCH") {
		t.Fatalf("custom-browser launch response = %s", raw)
	}
	calls, cleanupCalls := handler.snapshot()
	if len(calls) != 1 || calls[0].request.Command != "list_containers" || cleanupCalls != 1 {
		t.Fatalf("custom-browser launch reached mutation: calls=%#v cleanup=%d", calls, cleanupCalls)
	}
}

func TestAdapterSerializesCallsAndSchedulesCleanupOnce(t *testing.T) {
	handler := &fakeHandler{delay: 2 * time.Millisecond}
	adapter := NewAdapter(handler)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); adapter.Execute("ping", struct{}{}) }()
	}
	wg.Wait()
	calls, cleanupCalls := handler.snapshot()
	if len(calls) != 20 || cleanupCalls != 1 {
		t.Fatalf("calls=%d cleanup=%d", len(calls), cleanupCalls)
	}
	if atomic.LoadInt32(&handler.maxActive) != 1 {
		t.Fatalf("concurrent host calls = %d", handler.maxActive)
	}
	seen := map[string]bool{}
	for _, call := range calls {
		if seen[call.request.RequestID] {
			t.Fatalf("duplicate request ID %s", call.request.RequestID)
		}
		seen[call.request.RequestID] = true
	}
}

type controlledProcess struct {
	pid      int
	done     chan struct{}
	once     sync.Once
	lockPath string
}

func (p *controlledProcess) PID() int { return p.pid }
func (p *controlledProcess) Running() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}
func (p *controlledProcess) Wait() error { <-p.done; return nil }
func (p *controlledProcess) Terminate() error {
	p.once.Do(func() { _ = os.Remove(p.lockPath); close(p.done) })
	return nil
}

type controlledLauncher struct{ process *controlledProcess }

func (l controlledLauncher) Start(_ string, args []string) (browser.Process, error) {
	for _, arg := range args {
		if strings.HasPrefix(arg, "--user-data-dir=") {
			l.process.lockPath = filepath.Join(strings.TrimPrefix(arg, "--user-data-dir="), "SingletonLock")
			if err := os.WriteFile(l.process.lockPath, []byte("owned"), 0600); err != nil {
				return nil, err
			}
		}
	}
	return l.process, nil
}

func TestProcessOwnershipBoundaryThroughAdapter(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(t.TempDir(), "chrome")
	if os.PathSeparator == '\\' {
		executable += ".exe"
	}
	if err := os.WriteFile(executable, []byte("placeholder"), 0700); err != nil {
		t.Fatal(err)
	}
	process := &controlledProcess{pid: 4242, done: make(chan struct{})}
	ownerHost := host.New(st, controlledLauncher{process}, nil)
	owner := NewAdapter(ownerHost)
	createData, err := json.Marshal(map[string]any{"name": "Owned", "color": "#725cff", "browserType": "chrome", "browserExecutable": executable, "networkMode": "direct"})
	if err != nil {
		t.Fatal(err)
	}
	created := ownerHost.Handle(protocol.Request{Version: protocol.Version, RequestID: "human-create", Command: "create_container", Data: createData})
	if !created.Success {
		t.Fatalf("create: %#v", created)
	}
	container := created.Data.(model.Container)
	launched := owner.ExecuteWithIdentity("launch_container", container.ID, container.Name, struct {
		ID string `json:"id"`
	}{container.ID})
	if !launched.Success {
		t.Fatalf("launch: %#v", launched)
	}

	nonOwner := NewAdapter(host.New(st, controlledLauncher{process: &controlledProcess{pid: 9999, done: make(chan struct{})}}, nil))
	rejected := nonOwner.ExecuteWithIdentity("close_container", container.ID, container.Name, struct {
		ID string `json:"id"`
	}{container.ID})
	if rejected.Success || rejected.ErrorCode != "PROCESS_NOT_OWNED" {
		t.Fatalf("non-owner close: %#v", rejected)
	}
	if !process.Running() {
		t.Fatal("non-owner terminated the process")
	}

	closed := owner.ExecuteWithIdentity("close_container", container.ID, container.Name, struct {
		ID string `json:"id"`
	}{container.ID})
	if !closed.Success {
		t.Fatalf("owner close: %#v", closed)
	}
	if process.Running() {
		t.Fatal("owner did not terminate its process")
	}
}

func TestPersistedPIDNeverGrantsCloseAuthority(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	profile, err := st.EnsureProfile(testContainerID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "SingletonLock"), []byte("external"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := st.Update(func(db *model.Database) error {
		db.Containers = append(db.Containers, model.Container{ID: testContainerID, Name: "Persisted", Color: "#725cff", ProfilePath: profile, BrowserType: "chrome", BrowserExecutable: "C:/redacted", PID: os.Getpid(), Running: true, State: model.StateRunning, NetworkMode: "direct", CreatedAt: now, UpdatedAt: now})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	adapter := NewAdapter(host.New(st, nil, nil))
	response := adapter.ExecuteWithIdentity("close_container", testContainerID, "Persisted", struct {
		ID string `json:"id"`
	}{testContainerID})
	if response.Success || response.ErrorCode != "PROCESS_NOT_OWNED" {
		t.Fatalf("persisted PID granted authority: %#v", response)
	}
}
