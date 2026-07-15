package host

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scopenest/scopenest/native-host/internal/browser"
	"github.com/scopenest/scopenest/native-host/internal/certstore"
	"github.com/scopenest/scopenest/native-host/internal/model"
	"github.com/scopenest/scopenest/native-host/internal/protocol"
	"github.com/scopenest/scopenest/native-host/internal/security"
	"github.com/scopenest/scopenest/native-host/internal/store"
)

type failingLauncher struct{ err error }

func (f failingLauncher) Start(_ string, _ []string) (browser.Process, error) {
	return nil, f.err
}

type testProcess struct{ process *os.Process }

func (p *testProcess) PID() int         { return p.process.Pid }
func (p *testProcess) Running() bool    { return true }
func (p *testProcess) Wait() error      { _, err := p.process.Wait(); return err }
func (p *testProcess) Terminate() error { return p.process.Kill() }

type controlledProcess struct {
	pid            int
	exited         chan struct{}
	waitGate       chan struct{}
	waitStarted    chan struct{}
	waitReturned   chan struct{}
	terminated     chan struct{}
	exitOnce       sync.Once
	waitGateOnce   sync.Once
	waitStartOnce  sync.Once
	waitReturnOnce sync.Once
	terminateOnce  sync.Once
}

func newControlledProcess(pid int, holdWait bool) *controlledProcess {
	p := &controlledProcess{
		pid:          pid,
		exited:       make(chan struct{}),
		waitGate:     make(chan struct{}),
		waitStarted:  make(chan struct{}),
		waitReturned: make(chan struct{}),
		terminated:   make(chan struct{}),
	}
	if !holdWait {
		p.ReleaseWait()
	}
	return p
}

func (p *controlledProcess) PID() int { return p.pid }

func (p *controlledProcess) Running() bool {
	select {
	case <-p.exited:
		return false
	default:
		return true
	}
}

func (p *controlledProcess) Wait() error {
	p.waitStartOnce.Do(func() { close(p.waitStarted) })
	<-p.exited
	<-p.waitGate
	p.waitReturnOnce.Do(func() { close(p.waitReturned) })
	return nil
}

func (p *controlledProcess) Terminate() error {
	p.terminateOnce.Do(func() { close(p.terminated) })
	p.Exit()
	p.ReleaseWait()
	return nil
}

func (p *controlledProcess) Exit() {
	p.exitOnce.Do(func() { close(p.exited) })
}

func (p *controlledProcess) ReleaseWait() {
	p.waitGateOnce.Do(func() { close(p.waitGate) })
}

type queuedLauncher struct {
	mu        sync.Mutex
	processes []browser.Process
	calls     int
}

func (l *queuedLauncher) Start(_ string, _ []string) (browser.Process, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	if len(l.processes) == 0 {
		return nil, errors.New("no fake process queued")
	}
	process := l.processes[0]
	l.processes = l.processes[1:]
	return process, nil
}

func (l *queuedLauncher) CallCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

type gatedLauncher struct {
	process browser.Process
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (l *gatedLauncher) Start(_ string, _ []string) (browser.Process, error) {
	l.once.Do(func() { close(l.started) })
	<-l.release
	return l.process, nil
}

func waitForSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func waitForContainer(t *testing.T, st *store.Store, id string, predicate func(model.Container) bool, description string) model.Container {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		db, err := st.Load()
		if err != nil {
			t.Fatal(err)
		}
		for _, container := range db.Containers {
			if container.ID == id && predicate(container) {
				return container
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func testHost(t *testing.T) (*Host, *store.Store, string) {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(t.TempDir(), "chrome")
	if os.PathSeparator == '\\' {
		executable += ".exe"
	}
	if err := os.WriteFile(executable, []byte("test browser placeholder"), 0700); err != nil {
		t.Fatal(err)
	}
	return New(st, nil, nil), st, executable
}

func request(t *testing.T, command string, data any) protocol.Request {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return protocol.Request{Version: 1, RequestID: "test", Command: command, Data: raw}
}

func cleanupState(t *testing.T, response protocol.Response) string {
	t.Helper()
	if !response.Success {
		t.Fatalf("status request failed: %#v", response)
	}
	data, ok := response.Data.(map[string]any)
	if !ok {
		t.Fatalf("unexpected status data: %#v", response.Data)
	}
	metadata, ok := data["startupCleanup"].(startupCleanupMetadata)
	if !ok {
		t.Fatalf("missing startup cleanup metadata: %#v", data)
	}
	return metadata.State
}

func TestPingRemainsResponsiveDuringStartupCleanup(t *testing.T) {
	h, _, _ := testHost(t)
	if state := cleanupState(t, h.Handle(protocol.Request{Version: 1, RequestID: "pending", Command: "ping"})); state != "pending" {
		t.Fatalf("initial cleanup state = %q, want pending", state)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	h.startupCleanup = func() error {
		close(started)
		<-release
		return nil
	}
	startReturned := make(chan struct{})
	go func() {
		h.StartStartupCleanup()
		close(startReturned)
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("startup cleanup did not start")
	}
	select {
	case <-startReturned:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("startup cleanup scheduling did not return promptly")
	}

	response := make(chan protocol.Response, 1)
	go func() {
		response <- h.Handle(protocol.Request{Version: 1, RequestID: "during-cleanup", Command: "ping"})
	}()
	select {
	case ping := <-response:
		if state := cleanupState(t, ping); state != "running" {
			t.Fatalf("cleanup state during cleanup = %q, want running", state)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("ping blocked behind startup cleanup")
	}

	releaseOnce.Do(func() { close(release) })
	deadline := time.Now().Add(time.Second)
	for {
		state := cleanupState(t, h.Handle(protocol.Request{Version: 1, RequestID: "after-cleanup", Command: "ping"}))
		if state == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cleanup state = %q, want completed", state)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Scheduling is idempotent after completion.
	h.StartStartupCleanup()
}

func exitedHelperProcess(t *testing.T) browser.Process {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestWatcherHelperProcess")
	cmd.Env = append(os.Environ(), "SCOPENEST_WATCHER_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return &testProcess{process: cmd.Process}
}

func TestWatcherHelperProcess(t *testing.T) {
	if os.Getenv("SCOPENEST_WATCHER_HELPER") != "1" {
		return
	}
	os.Exit(0)
}

func TestHostProcessLaunchReservationHelper(t *testing.T) {
	root := os.Getenv("SCOPENEST_HOST_HELPER_ROOT")
	if root == "" {
		return
	}
	startPath := os.Getenv("SCOPENEST_HOST_HELPER_START")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(startPath); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for cross-process launch gate")
		}
		time.Sleep(10 * time.Millisecond)
	}
	st, err := store.New(root)
	if err != nil {
		t.Fatal(err)
	}
	h := New(st, nil, nil)
	_, _, reserveErr := h.reserveLaunch(os.Getenv("SCOPENEST_HOST_HELPER_ID"))
	result := "reserved"
	if reserveErr != nil {
		result = ErrorCode(reserveErr)
	}
	if err := os.WriteFile(os.Getenv("SCOPENEST_HOST_HELPER_RESULT"), []byte(result), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRequestValidationRejectsUnknownAndMalformedCommands(t *testing.T) {
	if _, err := DecodeRequest([]byte(`{"version":1,"requestId":"x","command":"run_anything"}`)); ErrorCode(err) != "UNKNOWN_COMMAND" {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := DecodeRequest([]byte(`{"version":1,"requestId":"x","command":"ping","extra":true}`)); ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := DecodeRequest([]byte(`{"version":2,"requestId":"x","command":"ping"}`)); ErrorCode(err) != "UNSUPPORTED_VERSION" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreateContainerAndPersistMetadata(t *testing.T) {
	h, st, executable := testHost(t)
	response := h.Handle(request(t, "create_container", containerInput{Name: "Target — Admin", Color: "#725cff", Icon: "A", BrowserType: "custom", BrowserExecutable: executable}))
	if !response.Success {
		t.Fatalf("create failed: %#v", response.Error)
	}
	c := response.Data.(model.Container)
	if c.ID == "" || c.Temporary || !filepath.IsAbs(c.ProfilePath) {
		t.Fatalf("invalid container: %#v", c)
	}
	db, _ := st.Load()
	if len(db.Containers) != 1 || db.Containers[0].ID != c.ID {
		t.Fatalf("container not persisted: %#v", db)
	}
}

func TestCreateContainerRejectsMissingNetworkReferences(t *testing.T) {
	h, _, executable := testHost(t)
	missingID := "11111111111111111111111111111111"
	for _, tc := range []struct {
		name      string
		input     containerInput
		errorCode string
	}{
		{"missing proxy", containerInput{Name: "Missing proxy", Color: "#725cff", BrowserType: "custom", BrowserExecutable: executable, NetworkMode: "proxy", ProxyProfileID: missingID}, "PROXY_PROFILE_NOT_FOUND"},
		{"missing template", containerInput{Name: "Missing template", Color: "#725cff", BrowserType: "custom", BrowserExecutable: executable, NetworkMode: "template", EnvironmentTemplateID: missingID}, "ENVIRONMENT_TEMPLATE_NOT_FOUND"},
		{"ambiguous direct", containerInput{Name: "Ambiguous", Color: "#725cff", BrowserType: "custom", BrowserExecutable: executable, NetworkMode: "direct", ProxyProfileID: missingID}, "INVALID_NETWORK_CONFIGURATION"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := h.Handle(request(t, "create_container", tc.input))
			if response.Success || response.ErrorCode != tc.errorCode {
				t.Fatalf("unexpected response: %#v", response)
			}
		})
	}
}

func TestStrictCommandDataTypes(t *testing.T) {
	h, _, executable := testHost(t)
	response := h.Handle(request(t, "create_container", map[string]any{"name": "Admin", "color": "#725cff", "icon": "", "browserType": "custom", "browserExecutable": executable, "unexpected": true}))
	if response.Success || response.ErrorCode != "INVALID_DATA" {
		t.Fatalf("accepted unknown field: %#v", response)
	}
}

func TestTemporaryCleanupRemovesProfileAndMetadata(t *testing.T) {
	h, st, executable := testHost(t)
	created := h.Handle(request(t, "create_temporary_container", containerInput{Name: "Temporary", Color: "#d28b26", Icon: "", BrowserType: "custom", BrowserExecutable: executable}))
	if !created.Success {
		t.Fatalf("create failed: %#v", created)
	}
	c := created.Data.(model.Container)
	cleaned := h.Handle(protocol.Request{Version: 1, RequestID: "cleanup", Command: "cleanup_temporary_containers"})
	if !cleaned.Success {
		t.Fatalf("cleanup failed: %#v", cleaned)
	}
	if _, err := os.Stat(c.ProfilePath); !os.IsNotExist(err) {
		t.Fatalf("profile was not removed: %v", err)
	}
	db, _ := st.Load()
	if len(db.Containers) != 0 {
		t.Fatalf("metadata remained: %#v", db)
	}
}

func TestTemporaryCleanupDefersWhenProfileLockExists(t *testing.T) {
	h, st, executable := testHost(t)
	created := h.Handle(request(t, "create_temporary_container", containerInput{Name: "Locked", Color: "#d28b26", BrowserType: "custom", BrowserExecutable: executable}))
	if !created.Success {
		t.Fatalf("create failed: %#v", created)
	}
	c := created.Data.(model.Container)
	if err := os.WriteFile(filepath.Join(c.ProfilePath, "SingletonLock"), []byte("owned"), 0600); err != nil {
		t.Fatal(err)
	}
	cleaned := h.Handle(protocol.Request{Version: 1, RequestID: "cleanup-locked", Command: "cleanup_temporary_containers"})
	if !cleaned.Success {
		t.Fatalf("cleanup command failed: %#v", cleaned)
	}
	if _, err := os.Stat(c.ProfilePath); err != nil {
		t.Fatalf("locked profile was removed: %v", err)
	}
	db, _ := st.Load()
	if len(db.Containers) != 1 || !db.Containers[0].PendingCleanup {
		t.Fatalf("cleanup was not deferred: %#v", db)
	}
}

func TestProcessStateReconciliationClearsStalePID(t *testing.T) {
	h, st, executable := testHost(t)
	created := h.Handle(request(t, "create_container", containerInput{Name: "Stale", Color: "#725cff", BrowserType: "custom", BrowserExecutable: executable}))
	if !created.Success {
		t.Fatalf("create failed: %#v", created)
	}
	c := created.Data.(model.Container)
	_ = st.Update(func(db *model.Database) error {
		db.Containers[0].Running = true
		db.Containers[0].State = model.StateRunning
		db.Containers[0].PID = 99999999
		return nil
	})
	response := h.Handle(protocol.Request{Version: 1, RequestID: "list", Command: "list_containers"})
	if !response.Success {
		t.Fatalf("list failed: %#v", response)
	}
	items := response.Data.([]model.Container)
	if len(items) != 1 || items[0].ID != c.ID || items[0].Running || items[0].PID != 0 {
		t.Fatalf("stale state not reconciled: %#v", items)
	}
}

func TestReconcileClearsUnownedReusedPIDWithoutProfileLock(t *testing.T) {
	h, st, executable := testHost(t)
	created := h.Handle(request(t, "create_container", containerInput{Name: "Reused PID", Color: "#725cff", BrowserType: "custom", BrowserExecutable: executable}))
	if !created.Success {
		t.Fatalf("create failed: %#v", created)
	}
	container := created.Data.(model.Container)
	if err := st.Update(func(db *model.Database) error {
		db.Containers[0].State = model.StateRunning
		db.Containers[0].Running = true
		db.Containers[0].PID = os.Getpid()
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := h.reconcile(); err != nil {
		t.Fatal(err)
	}
	db, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(db.Containers) != 1 || db.Containers[0].ID != container.ID || db.Containers[0].State != model.StateStopped || db.Containers[0].Running || db.Containers[0].PID != 0 {
		t.Fatalf("unowned reused PID preserved false running state: %#v", db)
	}
}

func TestReconcileLeavesMatchingOwnedProcessToWatcher(t *testing.T) {
	h, st, executable := testHost(t)
	created := h.Handle(request(t, "create_container", containerInput{Name: "Owned", Color: "#725cff", BrowserType: "custom", BrowserExecutable: executable}))
	if !created.Success {
		t.Fatalf("create failed: %#v", created)
	}
	container := created.Data.(model.Container)
	process := newControlledProcess(os.Getpid(), false)
	h.mu.Lock()
	h.processes[container.ID] = process
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.processes, container.ID)
		h.mu.Unlock()
	}()
	if err := st.Update(func(db *model.Database) error {
		db.Containers[0].State = model.StateRunning
		db.Containers[0].Running = true
		db.Containers[0].PID = process.PID()
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := h.reconcile(); err != nil {
		t.Fatal(err)
	}
	db, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(db.Containers) != 1 || db.Containers[0].State != model.StateRunning || !db.Containers[0].Running || db.Containers[0].PID != process.PID() {
		t.Fatalf("reconciliation overrode matching owned process: %#v", db)
	}
}

func TestReconcileKeepsUnownedContainerWhileProfileIsLocked(t *testing.T) {
	h, st, executable := testHost(t)
	created := h.Handle(request(t, "create_container", containerInput{Name: "Externally locked", Color: "#725cff", BrowserType: "custom", BrowserExecutable: executable}))
	if !created.Success {
		t.Fatalf("create failed: %#v", created)
	}
	container := created.Data.(model.Container)
	if err := os.WriteFile(filepath.Join(container.ProfilePath, "SingletonLock"), []byte("owned"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := st.Update(func(db *model.Database) error {
		db.Containers[0].State = model.StateRunning
		db.Containers[0].Running = true
		db.Containers[0].PID = 2_000_000_002
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := h.reconcile(); err != nil {
		t.Fatal(err)
	}
	db, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(db.Containers) != 1 || db.Containers[0].State != model.StateRunning || !db.Containers[0].Running {
		t.Fatalf("reconciliation cleared profile-locked container: %#v", db)
	}
}

func TestReusedUnownedPIDDoesNotBlockRelaunch(t *testing.T) {
	h, st, executable := testHost(t)
	process := newControlledProcess(os.Getpid(), false)
	h.launcher = &queuedLauncher{processes: []browser.Process{process}}
	created := h.Handle(request(t, "create_container", containerInput{Name: "Relaunch reused PID", Color: "#725cff", BrowserType: "custom", BrowserExecutable: executable}))
	if !created.Success {
		t.Fatalf("create failed: %#v", created)
	}
	container := created.Data.(model.Container)
	if err := st.Update(func(db *model.Database) error {
		db.Containers[0].State = model.StateRunning
		db.Containers[0].Running = true
		db.Containers[0].PID = os.Getpid()
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	launched := h.Handle(request(t, "launch_container", launchInput{ID: container.ID}))
	if !launched.Success {
		t.Fatalf("unowned reused PID blocked relaunch: %#v", launched)
	}
	_ = process.Terminate()
	waitForContainer(t, st, container.ID, func(c model.Container) bool { return c.State == model.StateStopped }, "reused PID relaunch cleanup")
}

func TestReusedUnownedPIDDoesNotBlockDeletion(t *testing.T) {
	h, st, executable := testHost(t)
	created := h.Handle(request(t, "create_container", containerInput{Name: "Delete reused PID", Color: "#725cff", BrowserType: "custom", BrowserExecutable: executable}))
	if !created.Success {
		t.Fatalf("create failed: %#v", created)
	}
	container := created.Data.(model.Container)
	if err := st.Update(func(db *model.Database) error {
		db.Containers[0].State = model.StateRunning
		db.Containers[0].Running = true
		db.Containers[0].PID = os.Getpid()
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	deleted := h.Handle(request(t, "delete_container", idInput{ID: container.ID}))
	if !deleted.Success {
		t.Fatalf("unowned reused PID blocked deletion: %#v", deleted)
	}
	if _, err := os.Stat(container.ProfilePath); !os.IsNotExist(err) {
		t.Fatalf("deleted stale container profile still exists: %v", err)
	}
}

func TestLaunchSuccessPersistsRunningProcess(t *testing.T) {
	h, st, executable := testHost(t)
	process := newControlledProcess(os.Getpid(), false)
	launcher := &queuedLauncher{processes: []browser.Process{process}}
	h.launcher = launcher
	created := h.Handle(request(t, "create_container", containerInput{Name: "Launch success", Color: "#725cff", BrowserType: "custom", BrowserExecutable: executable}))
	if !created.Success {
		t.Fatalf("create failed: %#v", created)
	}
	container := created.Data.(model.Container)

	response := h.Handle(request(t, "launch_container", launchInput{ID: container.ID, URL: "https://example.com"}))
	if !response.Success {
		t.Fatalf("launch failed: %#v", response)
	}
	launched := response.Data.(model.Container)
	if launched.State != model.StateRunning || !launched.Running || launched.PID != process.PID() || launched.LaunchToken != "" || launched.LaunchReservedAt != nil || launched.LastLaunchedAt == nil {
		t.Fatalf("invalid launched metadata: %#v", launched)
	}
	h.mu.Lock()
	owned := h.processes[container.ID]
	h.mu.Unlock()
	if owned != process || launcher.CallCount() != 1 {
		t.Fatalf("launched process was not owned exactly once: owned=%v calls=%d", owned == process, launcher.CallCount())
	}

	_ = process.Terminate()
	waitForContainer(t, st, container.ID, func(c model.Container) bool {
		return c.State == model.StateStopped && !c.Running && c.PID == 0
	}, "successful launch watcher cleanup")
}

func TestLaunchFailureAfterProcessCreationTerminatesProcess(t *testing.T) {
	h, st, executable := testHost(t)
	process := newControlledProcess(os.Getpid(), false)
	launcher := &gatedLauncher{process: process, started: make(chan struct{}), release: make(chan struct{})}
	h.launcher = launcher
	unexpectedWatcher := make(chan struct{})
	h.watcher = func(string, browser.Process) { close(unexpectedWatcher) }
	created := h.Handle(request(t, "create_container", containerInput{Name: "Lost reservation", Color: "#725cff", BrowserType: "custom", BrowserExecutable: executable}))
	if !created.Success {
		t.Fatalf("create failed: %#v", created)
	}
	container := created.Data.(model.Container)
	launchRequest := request(t, "launch_container", launchInput{ID: container.ID, URL: "https://example.com"})

	responses := make(chan protocol.Response, 1)
	go func() {
		responses <- h.Handle(launchRequest)
	}()
	waitForSignal(t, launcher.started, "fake process creation")
	if err := st.Update(func(db *model.Database) error {
		for i := range db.Containers {
			if db.Containers[i].ID == container.ID {
				db.Containers[i].LaunchToken = "replacement-launch-token"
				return nil
			}
		}
		return errors.New("container missing")
	}); err != nil {
		t.Fatal(err)
	}
	close(launcher.release)

	var response protocol.Response
	select {
	case response = <-responses:
	case <-time.After(5 * time.Second):
		t.Fatal("launch did not return after reservation loss")
	}
	if response.Success || response.ErrorCode != "LAUNCH_RESERVATION_LOST" {
		t.Fatalf("unexpected launch response: %#v", response)
	}
	waitForSignal(t, process.terminated, "orphan process termination")
	waitForSignal(t, process.waitReturned, "orphan process wait")
	h.mu.Lock()
	owned := h.processes[container.ID]
	h.mu.Unlock()
	if owned != nil {
		t.Fatal("failed launch left a process in the ownership map")
	}
	select {
	case <-unexpectedWatcher:
		t.Fatal("failed launch started a process watcher")
	default:
	}
	db, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if db.Containers[0].State != model.StateLaunching || db.Containers[0].LaunchToken != "replacement-launch-token" || db.Containers[0].PID != 0 {
		t.Fatalf("failed launch overwrote the replacement reservation: %#v", db.Containers[0])
	}
}

func TestCloseTerminatesOwnedProcessAndWatcherStopsContainer(t *testing.T) {
	h, st, executable := testHost(t)
	process := newControlledProcess(os.Getpid(), false)
	h.launcher = &queuedLauncher{processes: []browser.Process{process}}
	created := h.Handle(request(t, "create_container", containerInput{Name: "Close", Color: "#725cff", BrowserType: "custom", BrowserExecutable: executable}))
	if !created.Success {
		t.Fatalf("create failed: %#v", created)
	}
	container := created.Data.(model.Container)
	launched := h.Handle(request(t, "launch_container", launchInput{ID: container.ID}))
	if !launched.Success {
		t.Fatalf("launch failed: %#v", launched)
	}

	closed := h.Handle(request(t, "close_container", idInput{ID: container.ID}))
	if !closed.Success {
		t.Fatalf("close failed: %#v", closed)
	}
	waitForSignal(t, process.terminated, "owned process termination")
	waitForContainer(t, st, container.ID, func(c model.Container) bool {
		return c.State == model.StateStopped && !c.Running && c.PID == 0
	}, "close watcher metadata update")
	h.mu.Lock()
	owned := h.processes[container.ID]
	h.mu.Unlock()
	if owned != nil {
		t.Fatal("closed process remained in the ownership map")
	}
}

func TestDuplicateLaunchAttemptIsRejected(t *testing.T) {
	h, st, executable := testHost(t)
	process := newControlledProcess(os.Getpid(), false)
	launcher := &queuedLauncher{processes: []browser.Process{process}}
	h.launcher = launcher
	created := h.Handle(request(t, "create_container", containerInput{Name: "Duplicate", Color: "#725cff", BrowserType: "custom", BrowserExecutable: executable}))
	if !created.Success {
		t.Fatalf("create failed: %#v", created)
	}
	container := created.Data.(model.Container)
	first := h.Handle(request(t, "launch_container", launchInput{ID: container.ID}))
	if !first.Success {
		t.Fatalf("first launch failed: %#v", first)
	}
	second := h.Handle(request(t, "launch_container", launchInput{ID: container.ID}))
	if second.Success || second.ErrorCode != "ALREADY_RUNNING" {
		t.Fatalf("duplicate launch was not rejected: %#v", second)
	}
	if launcher.CallCount() != 1 {
		t.Fatalf("duplicate launch reached launcher: calls=%d", launcher.CallCount())
	}
	_ = process.Terminate()
	waitForContainer(t, st, container.ID, func(c model.Container) bool { return c.State == model.StateStopped }, "duplicate test cleanup")
}

func TestOldWatcherCannotOverwriteRelaunchedProcess(t *testing.T) {
	h, st, executable := testHost(t)
	oldProcess := newControlledProcess(2_000_000_001, true)
	newProcess := newControlledProcess(os.Getpid(), false)
	launcher := &queuedLauncher{processes: []browser.Process{oldProcess, newProcess}}
	h.launcher = launcher
	oldWatcherDone := make(chan struct{})
	baseWatcher := h.watcher
	h.watcher = func(id string, process browser.Process) {
		baseWatcher(id, process)
		if process == oldProcess {
			close(oldWatcherDone)
		}
	}
	created := h.Handle(request(t, "create_container", containerInput{Name: "Relaunch", Color: "#725cff", BrowserType: "custom", BrowserExecutable: executable}))
	if !created.Success {
		t.Fatalf("create failed: %#v", created)
	}
	container := created.Data.(model.Container)
	first := h.Handle(request(t, "launch_container", launchInput{ID: container.ID}))
	if !first.Success {
		t.Fatalf("P1 launch failed: %#v", first)
	}
	waitForSignal(t, oldProcess.waitStarted, "P1 watcher")
	oldProcess.Exit()

	second := h.Handle(request(t, "launch_container", launchInput{ID: container.ID}))
	if !second.Success {
		t.Fatalf("P2 launch failed before P1 watcher completed: %#v", second)
	}
	oldProcess.ReleaseWait()
	waitForSignal(t, oldWatcherDone, "stale P1 watcher completion")

	db, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(db.Containers) != 1 || db.Containers[0].State != model.StateRunning || !db.Containers[0].Running || db.Containers[0].PID != newProcess.PID() {
		t.Fatalf("P1 watcher overwrote P2 metadata: %#v", db)
	}
	h.mu.Lock()
	owned := h.processes[container.ID]
	h.mu.Unlock()
	if owned != newProcess {
		t.Fatal("P1 watcher removed P2 from the ownership map")
	}

	_ = newProcess.Terminate()
	waitForContainer(t, st, container.ID, func(c model.Container) bool { return c.State == model.StateStopped }, "relaunch test cleanup")
}

func TestOldWatcherCannotOverwriteReplacementProcess(t *testing.T) {
	h, st, executable := testHost(t)
	created := h.Handle(request(t, "create_temporary_container", containerInput{Name: "Replacement", Color: "#d28b26", BrowserType: "custom", BrowserExecutable: executable}))
	if !created.Success {
		t.Fatalf("create failed: %#v", created)
	}
	container := created.Data.(model.Container)
	oldProcess := exitedHelperProcess(t)
	replacementProcess, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	replacement := &testProcess{process: replacementProcess}

	h.mu.Lock()
	h.processes[container.ID] = replacement
	h.mu.Unlock()
	if err := st.Update(func(db *model.Database) error {
		db.Containers[0].Running = true
		db.Containers[0].State = model.StateRunning
		db.Containers[0].PID = replacement.PID()
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	h.watch(container.ID, oldProcess)

	h.mu.Lock()
	owned := h.processes[container.ID]
	h.mu.Unlock()
	if owned != replacement {
		t.Fatal("old watcher removed the replacement process from the ownership map")
	}
	db, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(db.Containers) != 1 || !db.Containers[0].Running || db.Containers[0].PID != replacement.PID() || db.Containers[0].PendingCleanup {
		t.Fatalf("old watcher changed replacement metadata: %#v", db)
	}
	if _, err := os.Stat(container.ProfilePath); err != nil {
		t.Fatalf("old watcher removed the replacement profile: %v", err)
	}
}

func TestWatcherRequiresMatchingPersistedPID(t *testing.T) {
	h, st, executable := testHost(t)
	created := h.Handle(request(t, "create_temporary_container", containerInput{Name: "PID guard", Color: "#d28b26", BrowserType: "custom", BrowserExecutable: executable}))
	if !created.Success {
		t.Fatalf("create failed: %#v", created)
	}
	container := created.Data.(model.Container)
	oldProcess := exitedHelperProcess(t)
	replacementPID := os.Getpid()

	h.mu.Lock()
	h.processes[container.ID] = oldProcess
	h.mu.Unlock()
	if err := st.Update(func(db *model.Database) error {
		db.Containers[0].Running = true
		db.Containers[0].State = model.StateRunning
		db.Containers[0].PID = replacementPID
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	h.watch(container.ID, oldProcess)

	db, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(db.Containers) != 1 || !db.Containers[0].Running || db.Containers[0].PID != replacementPID || db.Containers[0].PendingCleanup {
		t.Fatalf("watcher changed metadata owned by another PID: %#v", db)
	}
	if _, err := os.Stat(container.ProfilePath); err != nil {
		t.Fatalf("watcher removed a profile owned by another PID: %v", err)
	}
}

func TestCloseNeverUsesPersistedPIDAsProcessAuthority(t *testing.T) {
	h, st, executable := testHost(t)
	created := h.Handle(request(t, "create_container", containerInput{Name: "Persisted PID", Color: "#725cff", BrowserType: "custom", BrowserExecutable: executable}))
	if !created.Success {
		t.Fatalf("create failed: %#v", created)
	}
	container := created.Data.(model.Container)
	if err := st.Update(func(db *model.Database) error {
		db.Containers[0].Running = true
		db.Containers[0].State = model.StateRunning
		db.Containers[0].PID = os.Getpid()
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := h.close(container.ID); ErrorCode(err) != "PROCESS_NOT_OWNED" {
		t.Fatalf("persisted PID was treated as process authority: %v", err)
	}
	db, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !db.Containers[0].Running || db.Containers[0].PID != os.Getpid() {
		t.Fatalf("refused close changed persisted ownership metadata: %#v", db.Containers[0])
	}
}

func TestLaunchReservationBlocksAnotherHost(t *testing.T) {
	h1, st, executable := testHost(t)
	created := h1.Handle(request(t, "create_container", containerInput{Name: "Reserved", Color: "#725cff", BrowserType: "custom", BrowserExecutable: executable}))
	if !created.Success {
		t.Fatalf("create failed: %#v", created)
	}
	container := created.Data.(model.Container)
	secondStore, err := store.New(st.Root())
	if err != nil {
		t.Fatal(err)
	}
	h2 := New(secondStore, nil, nil)

	reserved, token, err := h1.reserveLaunch(container.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reserved.State != model.StateLaunching || token == "" || reserved.LaunchToken != token {
		t.Fatalf("invalid launch reservation: %#v", reserved)
	}
	if _, _, err := h2.reserveLaunch(container.ID); ErrorCode(err) != "ALREADY_LAUNCHING" {
		t.Fatalf("second host was not blocked by reservation: %v", err)
	}
	if err := h2.releaseLaunchReservation(container.ID, "wrong-token"); err != nil {
		t.Fatal(err)
	}
	db, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if db.Containers[0].State != model.StateLaunching || db.Containers[0].LaunchToken != token {
		t.Fatalf("wrong token released reservation: %#v", db.Containers[0])
	}
	if err := h1.releaseLaunchReservation(container.ID, token); err != nil {
		t.Fatal(err)
	}
	db, _ = st.Load()
	if db.Containers[0].State != model.StateStopped || db.Containers[0].LaunchToken != "" || db.Containers[0].LaunchReservedAt != nil {
		t.Fatalf("matching token did not release reservation: %#v", db.Containers[0])
	}
}

func TestConcurrentLaunchReservationsHaveSingleWinner(t *testing.T) {
	h1, st, executable := testHost(t)
	created := h1.Handle(request(t, "create_container", containerInput{Name: "Concurrent", Color: "#725cff", BrowserType: "custom", BrowserExecutable: executable}))
	if !created.Success {
		t.Fatalf("create failed: %#v", created)
	}
	container := created.Data.(model.Container)
	secondStore, err := store.New(st.Root())
	if err != nil {
		t.Fatal(err)
	}
	h2 := New(secondStore, nil, nil)

	type result struct {
		token string
		err   error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, candidate := range []*Host{h1, h2} {
		go func(host *Host) {
			<-start
			_, token, err := host.reserveLaunch(container.ID)
			results <- result{token: token, err: err}
		}(candidate)
	}
	close(start)

	successes, blocked := 0, 0
	winnerToken := ""
	for i := 0; i < 2; i++ {
		outcome := <-results
		if outcome.err == nil {
			successes++
			winnerToken = outcome.token
		} else if ErrorCode(outcome.err) == "ALREADY_LAUNCHING" {
			blocked++
		} else {
			t.Fatalf("unexpected reservation error: %v", outcome.err)
		}
	}
	if successes != 1 || blocked != 1 || winnerToken == "" {
		t.Fatalf("expected one reservation winner and one blocked host; successes=%d blocked=%d", successes, blocked)
	}
	db, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if db.Containers[0].State != model.StateLaunching || db.Containers[0].LaunchToken != winnerToken {
		t.Fatalf("persisted reservation does not match winner: %#v", db.Containers[0])
	}
}

func TestCrossProcessLaunchReservationsHaveSingleWinner(t *testing.T) {
	h, st, executable := testHost(t)
	created := h.Handle(request(t, "create_container", containerInput{Name: "Host processes", Color: "#725cff", BrowserType: "custom", BrowserExecutable: executable}))
	if !created.Success {
		t.Fatalf("create failed: %#v", created)
	}
	container := created.Data.(model.Container)
	startPath := filepath.Join(t.TempDir(), "start")
	const processCount = 6
	commands := make([]*exec.Cmd, 0, processCount)
	resultPaths := make([]string, 0, processCount)
	for i := 0; i < processCount; i++ {
		resultPath := filepath.Join(t.TempDir(), "result")
		cmd := exec.Command(os.Args[0], "-test.run=^TestHostProcessLaunchReservationHelper$")
		cmd.Env = append(os.Environ(),
			"SCOPENEST_HOST_HELPER_ROOT="+st.Root(),
			"SCOPENEST_HOST_HELPER_ID="+container.ID,
			"SCOPENEST_HOST_HELPER_START="+startPath,
			"SCOPENEST_HOST_HELPER_RESULT="+resultPath,
		)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, cmd)
		resultPaths = append(resultPaths, resultPath)
	}
	if err := os.WriteFile(startPath, []byte("go"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("native-host helper failed: %v", err)
		}
	}

	reserved, blocked := 0, 0
	for _, resultPath := range resultPaths {
		result, err := os.ReadFile(resultPath)
		if err != nil {
			t.Fatal(err)
		}
		switch string(result) {
		case "reserved":
			reserved++
		case "ALREADY_LAUNCHING":
			blocked++
		default:
			t.Fatalf("unexpected cross-process reservation result: %q", result)
		}
	}
	if reserved != 1 || blocked != processCount-1 {
		t.Fatalf("expected one process winner and %d blocked processes; reserved=%d blocked=%d", processCount-1, reserved, blocked)
	}
	db, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if db.Containers[0].State != model.StateLaunching || db.Containers[0].LaunchToken == "" {
		t.Fatalf("cross-process winner did not persist its reservation: %#v", db.Containers[0])
	}
}

func TestStaleLaunchReservationCanBeRecovered(t *testing.T) {
	h1, st, executable := testHost(t)
	clock := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	h1.now = func() time.Time { return clock }
	created := h1.Handle(request(t, "create_container", containerInput{Name: "Stale reservation", Color: "#725cff", BrowserType: "custom", BrowserExecutable: executable}))
	if !created.Success {
		t.Fatalf("create failed: %#v", created)
	}
	container := created.Data.(model.Container)
	_, firstToken, err := h1.reserveLaunch(container.ID)
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(launchReservationTTL + time.Second)
	h2 := New(st, nil, nil)
	h2.now = func() time.Time { return clock }
	reserved, secondToken, err := h2.reserveLaunch(container.ID)
	if err != nil {
		t.Fatal(err)
	}
	if secondToken == firstToken || reserved.LaunchToken != secondToken || reserved.State != model.StateLaunching {
		t.Fatalf("stale reservation was not replaced safely: %#v", reserved)
	}
}

func TestLaunchReservationRefusesProfileAlreadyInUse(t *testing.T) {
	h, _, executable := testHost(t)
	created := h.Handle(request(t, "create_container", containerInput{Name: "In use", Color: "#725cff", BrowserType: "custom", BrowserExecutable: executable}))
	if !created.Success {
		t.Fatalf("create failed: %#v", created)
	}
	container := created.Data.(model.Container)
	if err := os.WriteFile(filepath.Join(container.ProfilePath, "SingletonLock"), []byte("owned"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.reserveLaunch(container.ID); ErrorCode(err) != "PROFILE_IN_USE" {
		t.Fatalf("profile lock did not block launch reservation: %v", err)
	}
	db, err := h.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if db.Containers[0].State != model.StateStopped || db.Containers[0].LaunchToken != "" {
		t.Fatalf("blocked launch left a reservation: %#v", db.Containers[0])
	}
}

func TestLaunchFailureReleasesReservation(t *testing.T) {
	h, st, executable := testHost(t)
	h.launcher = failingLauncher{err: errors.New("expected launch failure")}
	created := h.Handle(request(t, "create_container", containerInput{Name: "Failure", Color: "#725cff", BrowserType: "custom", BrowserExecutable: executable}))
	if !created.Success {
		t.Fatalf("create failed: %#v", created)
	}
	container := created.Data.(model.Container)
	response := h.Handle(request(t, "launch_container", launchInput{ID: container.ID, URL: "https://example.com"}))
	if response.Success || response.ErrorCode != "LAUNCH_FAILED" {
		t.Fatalf("unexpected launch response: %#v", response)
	}
	db, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if db.Containers[0].State != model.StateStopped || db.Containers[0].LaunchToken != "" || db.Containers[0].LaunchReservedAt != nil || db.Containers[0].Running {
		t.Fatalf("failed launch left a reservation behind: %#v", db.Containers[0])
	}
}

type recordingLauncher struct {
	mu      sync.Mutex
	args    []string
	calls   int
	process browser.Process
}

func (l *recordingLauncher) Start(_ string, args []string) (browser.Process, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	l.args = append([]string(nil), args...)
	return l.process, nil
}

func (l *recordingLauncher) snapshot() (int, []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls, append([]string(nil), l.args...)
}

func proxyLaunchHost(t *testing.T, behavior string) (*Host, *store.Store, *recordingLauncher, string, *int) {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(t.TempDir(), "chrome")
	if os.PathSeparator == '\\' {
		executable += ".exe"
	}
	if err := os.WriteFile(executable, []byte("test browser placeholder"), 0700); err != nil {
		t.Fatal(err)
	}
	containerID, _ := security.NewID()
	proxyID, _ := security.NewID()
	profile, _ := st.EnsureProfile(containerID)
	now := time.Now().UTC()
	err = st.Update(func(db *model.Database) error {
		db.ProxyProfiles = append(db.ProxyProfiles, model.ProxyProfile{ID: proxyID, Name: "Proxy", Enabled: true, Protocol: "http", Host: "127.0.0.1", Port: 65530, UnavailableBehavior: behavior, HealthCheck: model.ProxyHealthCheck{Enabled: true, TimeoutMs: 100}})
		db.Containers = append(db.Containers, model.Container{ID: containerID, Name: "Proxy launch", Color: "#725cff", CreatedAt: now, UpdatedAt: now, ProfilePath: profile, BrowserType: "custom", BrowserExecutable: executable, State: model.StateStopped, NetworkMode: "proxy", ProxyProfileID: proxyID})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	launcher := &recordingLauncher{process: newControlledProcess(9010, true)}
	h := New(st, launcher, nil)
	h.watcher = func(string, browser.Process) {}
	checks := 0
	h.proxyDial = func(string, string, time.Duration) (net.Conn, error) {
		checks++
		return nil, errors.New("listener unavailable")
	}
	return h, st, launcher, containerID, &checks
}

func containsArgument(args []string, prefix string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, prefix) {
			return true
		}
	}
	return false
}

func TestProxyUnavailableWarnChecksListenerWarnsAndLaunchesWithProxyNoDirectFallback(t *testing.T) {
	h, _, launcher, id, checks := proxyLaunchHost(t, "warn")
	launched, err := h.launch(launchInput{ID: id, URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	calls, args := launcher.snapshot()
	if *checks != 1 || calls != 1 {
		t.Fatalf("checks=%d launches=%d", *checks, calls)
	}
	if launched.ProxyWarning == nil || launched.ProxyWarning.Code != "PROXY_LISTENER_UNAVAILABLE" || launched.DirectFallbackUsed {
		t.Fatalf("unexpected launch metadata: %#v", launched)
	}
	if !containsArgument(args, "--proxy-server=") || !containsArgument(args, "--disable-quic") {
		t.Fatalf("proxy arguments missing: %v", args)
	}
}

func TestProxyUnavailableBlockChecksListenerPreventsStartupAndReturnsStructuredError(t *testing.T) {
	h, st, launcher, id, checks := proxyLaunchHost(t, "block")
	response := h.Handle(request(t, "launch_container", launchInput{ID: id, URL: "https://example.com"}))
	if response.Success || response.ErrorCode != "PROXY_LISTENER_UNAVAILABLE" || response.Error == nil {
		t.Fatalf("unexpected structured response: %#v", response)
	}
	calls, _ := launcher.snapshot()
	if *checks != 1 || calls != 0 {
		t.Fatalf("checks=%d launches=%d", *checks, calls)
	}
	db, _ := st.Load()
	if db.Containers[0].State != model.StateStopped {
		t.Fatalf("reservation not released: %#v", db.Containers[0])
	}
}

func TestProxyUnavailableDirectChecksListenerLaunchesWithoutProxyOrQUICAndReportsFallback(t *testing.T) {
	h, _, launcher, id, checks := proxyLaunchHost(t, "direct")
	launched, err := h.launch(launchInput{ID: id, URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	calls, args := launcher.snapshot()
	if *checks != 1 || calls != 1 {
		t.Fatalf("checks=%d launches=%d", *checks, calls)
	}
	if !launched.DirectFallbackUsed || launched.ProxyWarning != nil {
		t.Fatalf("fallback not reported: %#v", launched)
	}
	if containsArgument(args, "--proxy-server=") || containsArgument(args, "--disable-quic") {
		t.Fatalf("direct fallback retained proxy arguments: %v", args)
	}
}

func TestProxyHealthCheckDisabledLaunchesWithProxyWithoutListenerCheck(t *testing.T) {
	h, _, launcher, id, checks := proxyLaunchHost(t, "block")
	if err := h.store.Update(func(db *model.Database) error {
		db.ProxyProfiles[0].HealthCheck.Enabled = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	launched, err := h.launch(launchInput{ID: id, URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	calls, args := launcher.snapshot()
	if *checks != 0 || calls != 1 {
		t.Fatalf("checks=%d launches=%d", *checks, calls)
	}
	if launched.DirectFallbackUsed || launched.ProxyWarning != nil || !containsArgument(args, "--proxy-server=") {
		t.Fatalf("unexpected launch metadata or args: %#v %v", launched, args)
	}
}

func TestDisabledProxyProfileBlocksLaunchWithoutDirectFallback(t *testing.T) {
	h, _, launcher, id, _ := proxyLaunchHost(t, "warn")
	if err := h.store.Update(func(db *model.Database) error {
		db.ProxyProfiles[0].Enabled = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	response := h.Handle(request(t, "launch_container", launchInput{ID: id}))
	if response.Success || response.ErrorCode != "PROXY_PROFILE_DISABLED" {
		t.Fatalf("unexpected response: %#v", response)
	}
	calls, _ := launcher.snapshot()
	if calls != 0 {
		t.Fatalf("disabled proxy launched browser: %d", calls)
	}
}

func TestProxyInputNormalizesOnlyStrictLocalHosts(t *testing.T) {
	proxy := model.ProxyProfile{Name: "Local", Enabled: true, Protocol: "http", Host: "localhost", Port: 8080, UnavailableBehavior: "warn", HealthCheck: model.ProxyHealthCheck{Enabled: true, TimeoutMs: 1500}}
	if err := validateProxyInput(&proxy); err != nil {
		t.Fatal(err)
	}
	if proxy.Host != "127.0.0.1" {
		t.Fatalf("localhost was not frozen to loopback literal: %q", proxy.Host)
	}
	for _, host := range []string{"example.test", "192.168.1.10", "10.0.0.1"} {
		proxy := model.ProxyProfile{Name: "Local", Enabled: true, Protocol: "http", Host: host, Port: 8080, UnavailableBehavior: "warn", HealthCheck: model.ProxyHealthCheck{Enabled: true, TimeoutMs: 1500}}
		if err := validateProxyInput(&proxy); err == nil {
			t.Fatalf("accepted non-strict-local host %q", host)
		}
	}
}

func TestTemplateInheritanceLaunchUsesTemplateProxyAndCertificates(t *testing.T) {
	st, _ := store.New(t.TempDir())
	trust := &ownershipTrustStore{}
	h, certificate := importHostTestCertificate(t, st, trust)
	if _, err := h.installCertificateTrust(certificate.ID); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(t.TempDir(), "chrome")
	if os.PathSeparator == '\\' {
		executable += ".exe"
	}
	if err := os.WriteFile(executable, []byte("test browser placeholder"), 0700); err != nil {
		t.Fatal(err)
	}
	containerID, _ := security.NewID()
	proxyID, _ := security.NewID()
	templateID, _ := security.NewID()
	profile, _ := st.EnsureProfile(containerID)
	now := time.Now().UTC()
	if err := st.Update(func(db *model.Database) error {
		db.ProxyProfiles = append(db.ProxyProfiles, model.ProxyProfile{ID: proxyID, Name: "Template proxy", Enabled: true, Protocol: "http", Host: "127.0.0.1", Port: 8080, CertificateIDs: []string{certificate.ID}, UnavailableBehavior: "warn", HealthCheck: model.ProxyHealthCheck{Enabled: false, TimeoutMs: 1500}})
		db.EnvironmentTemplates = append(db.EnvironmentTemplates, model.EnvironmentTemplate{ID: templateID, Name: "Web Pentest", ProxyProfileID: proxyID, CertificateIDs: []string{certificate.ID}, CreatedAt: now, UpdatedAt: now})
		db.Containers = append(db.Containers, model.Container{ID: containerID, Name: "Template launch", Color: "#725cff", CreatedAt: now, UpdatedAt: now, ProfilePath: profile, BrowserType: "custom", BrowserExecutable: executable, State: model.StateStopped, NetworkMode: "template", EnvironmentTemplateID: templateID})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	launcher := &recordingLauncher{process: newControlledProcess(9100, true)}
	h.launcher = launcher
	h.watcher = func(string, browser.Process) {}
	launched, err := h.launch(launchInput{ID: containerID})
	if err != nil {
		t.Fatal(err)
	}
	calls, args := launcher.snapshot()
	if calls != 1 || launched.DirectFallbackUsed || !containsExactArgument(args, "--proxy-server=http=http://127.0.0.1:8080;https=http://127.0.0.1:8080") {
		t.Fatalf("template proxy was not used: calls=%d launched=%#v args=%v", calls, launched, args)
	}
}

func TestContainerProxyOverrideUsesProxyInsteadOfTemplateProxy(t *testing.T) {
	h, st, executable := testHost(t)
	h.launcher = &recordingLauncher{process: newControlledProcess(9200, true)}
	h.watcher = func(string, browser.Process) {}
	containerID, _ := security.NewID()
	proxyA, _ := security.NewID()
	proxyB, _ := security.NewID()
	templateID, _ := security.NewID()
	profile, _ := st.EnsureProfile(containerID)
	now := time.Now().UTC()
	if err := st.Update(func(db *model.Database) error {
		db.ProxyProfiles = append(db.ProxyProfiles,
			model.ProxyProfile{ID: proxyA, Name: "Proxy A", Enabled: true, Protocol: "http", Host: "127.0.0.1", Port: 8080, UnavailableBehavior: "warn", HealthCheck: model.ProxyHealthCheck{Enabled: false, TimeoutMs: 1500}},
			model.ProxyProfile{ID: proxyB, Name: "Proxy B", Enabled: true, Protocol: "socks5", Host: "::1", Port: 1080, UnavailableBehavior: "warn", HealthCheck: model.ProxyHealthCheck{Enabled: false, TimeoutMs: 1500}},
		)
		db.EnvironmentTemplates = append(db.EnvironmentTemplates, model.EnvironmentTemplate{ID: templateID, Name: "Template", ProxyProfileID: proxyA, CreatedAt: now, UpdatedAt: now})
		db.Containers = append(db.Containers, model.Container{ID: containerID, Name: "Override launch", Color: "#725cff", CreatedAt: now, UpdatedAt: now, ProfilePath: profile, BrowserType: "custom", BrowserExecutable: executable, State: model.StateStopped, NetworkMode: "proxy", ProxyProfileID: proxyB, EnvironmentTemplateID: templateID})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.launch(launchInput{ID: containerID}); err != nil {
		t.Fatal(err)
	}
	_, args := h.launcher.(*recordingLauncher).snapshot()
	if !containsExactArgument(args, "--proxy-server=socks5://[::1]:1080") {
		t.Fatalf("override proxy was not used: %v", args)
	}
}

func containsExactArgument(args []string, expected string) bool {
	for _, arg := range args {
		if arg == expected {
			return true
		}
	}
	return false
}

func TestLinuxManualTrustAcknowledgmentBindsCertificateFingerprintPlatformAndTimestamp(t *testing.T) {
	st, _ := store.New(t.TempDir())
	id, _ := security.NewID()
	fingerprint := "AA:BB"
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	if err := st.Update(func(db *model.Database) error {
		db.Certificates = append(db.Certificates, model.Certificate{ID: id, SHA256Fingerprint: fingerprint, TrustState: model.CertificateTrustUntrusted})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h := New(st, failingLauncher{}, nil)
	h.platform = "linux"
	h.now = func() time.Time { return now }
	response := h.Handle(request(t, "acknowledge_manual_certificate_trust", acknowledgeManualTrustInput{ID: id, SHA256Fingerprint: fingerprint, Platform: "linux"}))
	if !response.Success {
		t.Fatalf("acknowledgment failed: %#v", response)
	}
	certificate := response.Data.(model.Certificate)
	ack := certificate.ManualTrustAcknowledgment
	if certificate.Trusted || certificate.TrustState != model.CertificateTrustManualAcknowledgedUnverified || ack == nil || ack.CertificateID != id || ack.SHA256Fingerprint != fingerprint || ack.Platform != "linux" || !ack.AcknowledgedAt.Equal(now) {
		t.Fatalf("invalid acknowledgment: %#v", certificate)
	}
}

func TestLinuxManualTrustAcknowledgmentFingerprintChangeInvalidatesState(t *testing.T) {
	st, _ := store.New(t.TempDir())
	id, _ := security.NewID()
	now := time.Now().UTC()
	if err := st.Update(func(db *model.Database) error {
		db.Certificates = append(db.Certificates, model.Certificate{ID: id, SHA256Fingerprint: "AA", TrustState: model.CertificateTrustManualAcknowledgedUnverified, ManualTrustAcknowledgment: &model.ManualTrustAcknowledgment{CertificateID: id, SHA256Fingerprint: "AA", Platform: "linux", AcknowledgedAt: now}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Update(func(db *model.Database) error { db.Certificates[0].SHA256Fingerprint = "BB"; return nil }); err != nil {
		t.Fatal(err)
	}
	db, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	certificate := db.Certificates[0]
	if certificate.ManualTrustAcknowledgment != nil || certificate.TrustState != model.CertificateTrustUntrusted || certificate.Trusted {
		t.Fatalf("stale acknowledgment remained valid: %#v", certificate)
	}
}

type ownershipTrustStore struct {
	already  bool
	installs int
	removals int
}

func (*ownershipTrustStore) Scope() string   { return "test" }
func (*ownershipTrustStore) Supported() bool { return true }
func (s *ownershipTrustStore) Verify([]byte, string) (bool, error) {
	return s.already || s.installs > s.removals, nil
}
func (s *ownershipTrustStore) Install([]byte, string) (bool, error) {
	s.installs++
	return s.already, nil
}
func (s *ownershipTrustStore) Remove([]byte, string) error { s.removals++; return nil }

type blockingTrustStore struct {
	mu        sync.Mutex
	installed bool
	started   chan struct{}
	release   chan struct{}
	once      sync.Once
}

func (s *blockingTrustStore) Scope() string   { return "test" }
func (s *blockingTrustStore) Supported() bool { return true }
func (s *blockingTrustStore) Verify([]byte, string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.installed, nil
}
func (s *blockingTrustStore) Install([]byte, string) (bool, error) {
	s.once.Do(func() { close(s.started) })
	<-s.release
	s.mu.Lock()
	defer s.mu.Unlock()
	already := s.installed
	s.installed = true
	return already, nil
}
func (s *blockingTrustStore) Remove([]byte, string) error {
	s.once.Do(func() { close(s.started) })
	<-s.release
	s.mu.Lock()
	defer s.mu.Unlock()
	s.installed = false
	return nil
}

type faultTrustStore struct {
	installed     bool
	installErr    error
	removeErr     error
	installMutate bool
	removeMutate  bool
}

func (s *faultTrustStore) Scope() string   { return "test" }
func (s *faultTrustStore) Supported() bool { return true }
func (s *faultTrustStore) Verify([]byte, string) (bool, error) {
	return s.installed, nil
}
func (s *faultTrustStore) Install([]byte, string) (bool, error) {
	already := s.installed
	if s.installMutate {
		s.installed = true
	}
	return already, s.installErr
}
func (s *faultTrustStore) Remove([]byte, string) error {
	if s.removeMutate {
		s.installed = false
	}
	return s.removeErr
}

func importHostTestCertificate(t *testing.T, st *store.Store, trust certstore.TrustStore) (*Host, model.Certificate) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Host test"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), BasicConstraintsValid: true, IsCA: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	manager := certstore.NewManager(st, trust)
	staged, err := manager.Import("Host CA", base64.StdEncoding.EncodeToString(der), len(der))
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := manager.CommitImport(staged)
	if err != nil {
		t.Fatal(err)
	}
	return New(st, failingLauncher{}, manager), certificate
}

func TestInstallCertificateTrustNeverClaimsPreexistingCertificate(t *testing.T) {
	st, _ := store.New(t.TempDir())
	trust := &ownershipTrustStore{already: true}
	h, certificate := importHostTestCertificate(t, st, trust)
	updated, err := h.installCertificateTrust(certificate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Trusted || updated.InstalledByScopeNest {
		t.Fatalf("pre-existing certificate ownership was claimed: %#v", updated)
	}
}

func TestRepeatedCertificateTrustInstallPreservesScopeNestOwnership(t *testing.T) {
	st, _ := store.New(t.TempDir())
	trust := &ownershipTrustStore{}
	h, certificate := importHostTestCertificate(t, st, trust)
	first, err := h.installCertificateTrust(certificate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !first.InstalledByScopeNest {
		t.Fatalf("new installation did not record ownership: %#v", first)
	}
	trust.already = true
	second, err := h.installCertificateTrust(certificate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !second.InstalledByScopeNest {
		t.Fatalf("repeated installation downgraded ownership: %#v", second)
	}
}

func TestConcurrentCertificateTrustInstallHasSingleWinner(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	trust := &blockingTrustStore{started: make(chan struct{}), release: make(chan struct{})}
	h1, certificate := importHostTestCertificate(t, st, trust)
	secondStore, err := store.New(st.Root())
	if err != nil {
		t.Fatal(err)
	}
	h2 := New(secondStore, failingLauncher{}, certstore.NewManager(secondStore, trust))
	result := make(chan error, 1)
	go func() {
		_, err := h1.installCertificateTrust(certificate.ID)
		result <- err
	}()
	waitForSignal(t, trust.started, "blocked certificate trust install")
	if _, err := h2.installCertificateTrust(certificate.ID); ErrorCode(err) != "CERTIFICATE_TRUST_OPERATION_PENDING" {
		t.Fatalf("second install did not see pending operation: %v", err)
	}
	close(trust.release)
	if err := <-result; err != nil {
		t.Fatalf("winning install failed: %v", err)
	}
}

func TestTrustedCertificateDeletionIsRejectedUntilTrustRemoved(t *testing.T) {
	st, _ := store.New(t.TempDir())
	trust := &ownershipTrustStore{}
	h, certificate := importHostTestCertificate(t, st, trust)
	if _, err := h.installCertificateTrust(certificate.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.deleteCertificate(certificate.ID); ErrorCode(err) != "CERTIFICATE_TRUST_MUST_BE_REMOVED_FIRST" {
		t.Fatalf("unexpected delete error: %v", err)
	}
	if _, err := h.removeCertificateTrust(certificate.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.deleteCertificate(certificate.ID); err != nil {
		t.Fatal(err)
	}
}

func TestTrustedUnownedCertificateCanBeDeletedFromLibraryOnly(t *testing.T) {
	st, _ := store.New(t.TempDir())
	trust := &ownershipTrustStore{already: true}
	h, certificate := importHostTestCertificate(t, st, trust)
	updated, err := h.installCertificateTrust(certificate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Trusted || updated.InstalledByScopeNest {
		t.Fatalf("expected trusted unowned certificate: %#v", updated)
	}
	deleted, err := h.deleteCertificate(certificate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deleted["windowsTrustUnchanged"] != true {
		t.Fatalf("library-only deletion was not reported: %#v", deleted)
	}
	if trust.removals != 0 {
		t.Fatalf("library-only deletion removed Windows trust: %d", trust.removals)
	}
}

func TestCertificateDeletionTombstoneRestoresWhenMetadataStillExists(t *testing.T) {
	st, _ := store.New(t.TempDir())
	trust := &ownershipTrustStore{}
	h, certificate := importHostTestCertificate(t, st, trust)
	certRoot := filepath.Join(st.Root(), "resources", "certificates")
	source := filepath.Join(certRoot, certificate.ID)
	staged := filepath.Join(certRoot, ".delete-"+certificate.ID+"-restore")
	if err := os.Rename(source, staged); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := st.Update(func(db *model.Database) error {
		db.CertificateDeletionOps = append(db.CertificateDeletionOps, model.CertificateDeletionOperation{CertificateID: certificate.ID, OperationID: "restore", SourceDirectory: source, StagedDirectory: staged, State: "deleting", CreatedAt: now, UpdatedAt: now})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.reconcileCertificateDeletions(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(source, "certificate.der")); err != nil {
		t.Fatalf("certificate directory was not restored: %v", err)
	}
	if _, err := os.Stat(staged); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged directory remained: %v", err)
	}
	db, _ := st.Load()
	if len(db.CertificateDeletionOps) != 0 || len(db.Certificates) != 1 {
		t.Fatalf("tombstone not cleared after restore: %#v", db)
	}
}

func TestCertificateDeletionTombstoneFinishesWhenMetadataAbsent(t *testing.T) {
	st, _ := store.New(t.TempDir())
	trust := &ownershipTrustStore{}
	h, certificate := importHostTestCertificate(t, st, trust)
	certRoot := filepath.Join(st.Root(), "resources", "certificates")
	source := filepath.Join(certRoot, certificate.ID)
	staged := filepath.Join(certRoot, ".delete-"+certificate.ID+"-finish")
	if err := os.Rename(source, staged); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := st.Update(func(db *model.Database) error {
		db.Certificates = nil
		db.CertificateDeletionOps = append(db.CertificateDeletionOps, model.CertificateDeletionOperation{CertificateID: certificate.ID, OperationID: "finish", SourceDirectory: source, StagedDirectory: staged, State: "deleting", CreatedAt: now, UpdatedAt: now})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.reconcileCertificateDeletions(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staged); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged directory remained: %v", err)
	}
	db, _ := st.Load()
	if len(db.CertificateDeletionOps) != 0 || len(db.Certificates) != 0 {
		t.Fatalf("tombstone not cleared after finish: %#v", db)
	}
}

func TestCertificateDeletionErrorIsRetriedOnStartup(t *testing.T) {
	st, _ := store.New(t.TempDir())
	trust := &ownershipTrustStore{}
	h, certificate := importHostTestCertificate(t, st, trust)
	certRoot := filepath.Join(st.Root(), "resources", "certificates")
	source := filepath.Join(certRoot, certificate.ID)
	staged := filepath.Join(certRoot, ".delete-"+certificate.ID+"-retry")
	if err := os.Rename(source, staged); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Hour)
	if err := st.Update(func(db *model.Database) error {
		db.CertificateDeletionOps = append(db.CertificateDeletionOps, model.CertificateDeletionOperation{
			CertificateID: certificate.ID, OperationID: "retry", SourceDirectory: source,
			StagedDirectory: staged, State: "deletion_error", Error: "temporary failure", CreatedAt: now, UpdatedAt: now,
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.startupCleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(source, "certificate.der")); err != nil {
		t.Fatalf("startup did not restore the certificate directory: %v", err)
	}
	db, _ := st.Load()
	if len(db.CertificateDeletionOps) != 0 {
		t.Fatalf("retried deletion tombstone remains: %#v", db.CertificateDeletionOps)
	}
}

func TestCertificateDeletionErrorBlocksSecondDeletion(t *testing.T) {
	st, _ := store.New(t.TempDir())
	trust := &ownershipTrustStore{}
	h, certificate := importHostTestCertificate(t, st, trust)
	certRoot := filepath.Join(st.Root(), "resources", "certificates")
	now := time.Now().UTC()
	if err := st.Update(func(db *model.Database) error {
		db.CertificateDeletionOps = append(db.CertificateDeletionOps, model.CertificateDeletionOperation{
			CertificateID: certificate.ID, OperationID: "unresolved", SourceDirectory: filepath.Join(certRoot, certificate.ID),
			StagedDirectory: filepath.Join(certRoot, ".delete-unresolved"), State: "deletion_error", Error: "temporary failure", CreatedAt: now, UpdatedAt: now,
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.deleteCertificate(certificate.ID); ErrorCode(err) != "CERTIFICATE_DELETE_OPERATION_PENDING" {
		t.Fatalf("second deletion was not blocked: %v", err)
	}
	db, _ := st.Load()
	if len(db.CertificateDeletionOps) != 1 {
		t.Fatalf("second deletion operation was created: %#v", db.CertificateDeletionOps)
	}
}

func TestReconciliationContinuesAfterOneDeletionError(t *testing.T) {
	st, _ := store.New(t.TempDir())
	trust := &ownershipTrustStore{}
	_, first := importHostTestCertificate(t, st, trust)
	h, second := importHostTestCertificate(t, st, trust)
	certRoot := filepath.Join(st.Root(), "resources", "certificates")
	secondSource := filepath.Join(certRoot, second.ID)
	secondStaged := filepath.Join(certRoot, ".delete-"+second.ID+"-continue")
	if err := os.Rename(secondSource, secondStaged); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-time.Hour)
	if err := st.Update(func(db *model.Database) error {
		db.CertificateDeletionOps = append(db.CertificateDeletionOps,
			model.CertificateDeletionOperation{CertificateID: first.ID, OperationID: "broken", SourceDirectory: filepath.Join(t.TempDir(), "outside"), StagedDirectory: filepath.Join(certRoot, ".delete-broken"), State: "deletion_error", Error: "old error", CreatedAt: old, UpdatedAt: old},
			model.CertificateDeletionOperation{CertificateID: second.ID, OperationID: "recoverable", SourceDirectory: secondSource, StagedDirectory: secondStaged, State: "deleting", CreatedAt: old, UpdatedAt: old},
		)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.reconcileCertificateDeletions(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(secondSource, "certificate.der")); err != nil {
		t.Fatalf("later deletion operation was not reconciled: %v", err)
	}
	db, _ := st.Load()
	if len(db.CertificateDeletionOps) != 1 || db.CertificateDeletionOps[0].OperationID != "broken" || db.CertificateDeletionOps[0].Error == "old error" || !db.CertificateDeletionOps[0].UpdatedAt.After(old) {
		t.Fatalf("reconciliation did not process both operations: %#v", db.CertificateDeletionOps)
	}
}

func TestInterruptedInstallingOperationClearsOwnershipWhenTrustStoreWasNotMutated(t *testing.T) {
	st, _ := store.New(t.TempDir())
	trust := &faultTrustStore{installed: false}
	h, certificate := importHostTestCertificate(t, st, trust)
	if err := st.Update(func(db *model.Database) error {
		managed := &db.Certificates[0]
		managed.Trusted = false
		managed.TrustState = model.CertificateTrustInstalling
		managed.InstalledByScopeNest = true
		managed.TrustOperationID = "interrupted-install"
		managed.TrustOperationFingerprint = managed.SHA256Fingerprint
		managed.TrustOperationWasTrusted = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.reconcileTrustOperations(); err != nil {
		t.Fatal(err)
	}
	db, _ := st.Load()
	managed := db.Certificates[0]
	if managed.ID != certificate.ID || managed.Trusted || managed.TrustState != model.CertificateTrustUntrusted || managed.InstalledByScopeNest || managed.TrustOperationID != "" {
		t.Fatalf("absent failed installation retained false ownership: %#v", managed)
	}
}

func TestTrustErrorReconciliationRecoversInstallThatActuallyInstalled(t *testing.T) {
	st, _ := store.New(t.TempDir())
	trust := &faultTrustStore{installErr: errors.New("after install"), installMutate: true}
	h, certificate := importHostTestCertificate(t, st, trust)
	if _, err := h.installCertificateTrust(certificate.ID); ErrorCode(err) != "CERTIFICATE_TRUST_INSTALL_FAILED" {
		t.Fatalf("unexpected install error: %v", err)
	}
	if err := h.reconcileTrustOperations(); err != nil {
		t.Fatal(err)
	}
	if err := h.reconcileTrustOperations(); err != nil {
		t.Fatal(err)
	}
	db, _ := st.Load()
	cert := db.Certificates[0]
	if !cert.Trusted || cert.TrustState != model.CertificateTrustTrusted || !cert.InstalledByScopeNest || cert.TrustOperationID != "" || cert.TrustError != "" {
		t.Fatalf("trust_error install was not recovered: %#v", cert)
	}
}

func TestTrustErrorReconciliationRecoversRemovalOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name          string
		removeMutate  bool
		wantTrusted   bool
		wantState     string
		wantOwnership bool
	}{
		{"left-present", false, true, model.CertificateTrustTrusted, true},
		{"removed", true, false, model.CertificateTrustUntrusted, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, _ := store.New(t.TempDir())
			trust := &faultTrustStore{installed: true, removeErr: errors.New("remove report"), removeMutate: tc.removeMutate}
			h, certificate := importHostTestCertificate(t, st, trust)
			if err := st.Update(func(db *model.Database) error {
				db.Certificates[0].Trusted = true
				db.Certificates[0].InstalledByScopeNest = true
				db.Certificates[0].TrustState = model.CertificateTrustTrusted
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := h.removeCertificateTrust(certificate.ID); ErrorCode(err) != "CERTIFICATE_TRUST_REMOVE_FAILED" {
				t.Fatalf("unexpected remove error: %v", err)
			}
			if err := h.reconcileTrustOperations(); err != nil {
				t.Fatal(err)
			}
			if err := h.reconcileTrustOperations(); err != nil {
				t.Fatal(err)
			}
			db, _ := st.Load()
			cert := db.Certificates[0]
			if cert.Trusted != tc.wantTrusted || cert.TrustState != tc.wantState || cert.InstalledByScopeNest != tc.wantOwnership || cert.TrustOperationID != "" || cert.TrustError != "" {
				t.Fatalf("trust_error removal was not recovered: %#v", cert)
			}
		})
	}
}

func TestRemoveCertificateTrustNeverRemovesUnownedOrReferencedCertificate(t *testing.T) {
	st, _ := store.New(t.TempDir())
	trust := &ownershipTrustStore{}
	h, certificate := importHostTestCertificate(t, st, trust)
	if _, err := h.removeCertificateTrust(certificate.ID); ErrorCode(err) != "CERTIFICATE_NOT_INSTALLED_BY_SCOPENEST" {
		t.Fatalf("unexpected unowned error: %v", err)
	}
	if err := st.Update(func(db *model.Database) error {
		db.Certificates[0].InstalledByScopeNest = true
		db.ProxyProfiles = append(db.ProxyProfiles, model.ProxyProfile{ID: "proxy", CertificateIDs: []string{certificate.ID}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.removeCertificateTrust(certificate.ID); ErrorCode(err) != "CERTIFICATE_REFERENCE_IN_USE" {
		t.Fatalf("unexpected reference error: %v", err)
	}
	if trust.removals != 0 {
		t.Fatal("trust store removal was called")
	}
}

func TestRemoveCertificateTrustReReadsAndReFingerprintsManagedDER(t *testing.T) {
	st, _ := store.New(t.TempDir())
	trust := &ownershipTrustStore{}
	h, certificate := importHostTestCertificate(t, st, trust)
	if err := st.Update(func(db *model.Database) error { db.Certificates[0].InstalledByScopeNest = true; return nil }); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(st.Root(), "resources", "certificates", certificate.ID, "certificate.der")
	if err := os.WriteFile(path, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.removeCertificateTrust(certificate.ID); ErrorCode(err) != "CERTIFICATE_READ_FAILED" {
		t.Fatalf("unexpected error: %v", err)
	}
	if trust.removals != 0 {
		t.Fatal("tampered DER reached trust store removal")
	}
}
