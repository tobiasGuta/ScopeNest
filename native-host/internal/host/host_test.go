package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/scopenest/scopenest/native-host/internal/model"
	"github.com/scopenest/scopenest/native-host/internal/protocol"
	"github.com/scopenest/scopenest/native-host/internal/store"
)

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
