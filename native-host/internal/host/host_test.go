package host

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/scopenest/scopenest/native-host/internal/browser"
	"github.com/scopenest/scopenest/native-host/internal/model"
	"github.com/scopenest/scopenest/native-host/internal/protocol"
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
	return New(st, nil), st, executable
}

func request(t *testing.T, command string, data any) protocol.Request {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return protocol.Request{Version: 1, RequestID: "test", Command: command, Data: raw}
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
	h := New(st, nil)
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

func TestLaunchSuccessPersistsRunningProcess(t *testing.T) {
	h, st, executable := testHost(t)
	process := newControlledProcess(os.Getpid(), false)
	launcher := &queuedLauncher{processes: []browser.Process{process}}
	h.launcher = launcher
	created := h.Handle(request(t, "create_container", containerInput{Name: "Launch success", Color: "#725cff", BrowserType: "custom", BrowserExecutable: executable}))
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
	h2 := New(secondStore, nil)

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
	container := created.Data.(model.Container)
	secondStore, err := store.New(st.Root())
	if err != nil {
		t.Fatal(err)
	}
	h2 := New(secondStore, nil)

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
	container := created.Data.(model.Container)
	_, firstToken, err := h1.reserveLaunch(container.ID)
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(launchReservationTTL + time.Second)
	h2 := New(st, nil)
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
