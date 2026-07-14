package store

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scopenest/scopenest/native-host/internal/model"
	"github.com/scopenest/scopenest/native-host/internal/security"
)

func TestMetadataPersistsThroughAtomicUpdate(t *testing.T) {
	root := t.TempDir()
	st, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := security.NewID()
	profile, err := st.EnsureProfile(id)
	if err != nil {
		t.Fatal(err)
	}
	want := model.Container{ID: id, Name: "Admin", Color: "#725cff", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), ProfilePath: profile, BrowserType: "custom", BrowserExecutable: os.Args[0], State: model.StateStopped}
	if err := st.Update(func(db *model.Database) error { db.Containers = append(db.Containers, want); return nil }); err != nil {
		t.Fatal(err)
	}
	loaded, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Containers) != 1 || loaded.Containers[0].ID != id {
		t.Fatalf("metadata did not persist: %#v", loaded)
	}
	if _, err := os.Stat(filepath.Join(root, "containers.json")); err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(filepath.Join(root, ".containers-*.tmp"))
	if len(matches) != 0 {
		t.Fatalf("temporary metadata left behind: %v", matches)
	}
}

func TestProfilePathCannotUseUntrustedID(t *testing.T) {
	st, _ := New(t.TempDir())
	if _, err := st.ProfilePath("../../escape"); err == nil {
		t.Fatal("accepted path traversal ID")
	}
}

func TestRemoveContainerDirectoryStaysManaged(t *testing.T) {
	root := t.TempDir()
	st, _ := New(root)
	id, _ := security.NewID()
	profile, _ := st.EnsureProfile(id)
	outside := filepath.Join(t.TempDir(), "keep")
	_ = os.WriteFile(outside, []byte("safe"), 0600)
	if err := st.RemoveContainerDirectory(id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Fatalf("profile still exists: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside file was touched: %v", err)
	}
}

func TestProfileInUseDetectsChromiumLockMarkers(t *testing.T) {
	st, _ := New(t.TempDir())
	id, _ := security.NewID()
	profile, _ := st.EnsureProfile(id)
	if inUse, err := st.ProfileInUse(id); err != nil || inUse {
		t.Fatalf("fresh profile reported in use: %v %v", inUse, err)
	}
	if err := os.WriteFile(filepath.Join(profile, "SingletonCookie"), []byte("lock"), 0600); err != nil {
		t.Fatal(err)
	}
	if inUse, err := st.ProfileInUse(id); err != nil || !inUse {
		t.Fatalf("profile lock was not detected: %v %v", inUse, err)
	}
}

func TestMetadataLockTimesOut(t *testing.T) {
	root := t.TempDir()
	st, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	held, err := acquireFileLock(st.lockPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()

	contender, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	contender.lockTimeout = 100 * time.Millisecond
	if _, err := contender.Load(); !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("expected metadata lock timeout, got %v", err)
	}
}

func TestStoreProcessHelper(t *testing.T) {
	root := os.Getenv("SCOPENEST_STORE_HELPER_ROOT")
	if root == "" {
		return
	}
	st, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	id, err := security.NewID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	name := os.Getenv("SCOPENEST_STORE_HELPER_NAME")
	if err := st.Update(func(db *model.Database) error {
		// Make an unlocked load-modify-write race deterministic enough to lose updates.
		time.Sleep(75 * time.Millisecond)
		db.Containers = append(db.Containers, model.Container{ID: id, Name: name, Color: "#725cff", CreatedAt: now, UpdatedAt: now, State: model.StateStopped})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCrossProcessUpdatesDoNotLoseData(t *testing.T) {
	root := t.TempDir()
	const processCount = 6
	commands := make([]*exec.Cmd, 0, processCount)
	for i := 0; i < processCount; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=TestStoreProcessHelper")
		cmd.Env = append(os.Environ(),
			"SCOPENEST_STORE_HELPER_ROOT="+root,
			"SCOPENEST_STORE_HELPER_NAME=worker-"+string(rune('A'+i)),
		)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, cmd)
	}
	for _, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("metadata helper failed: %v", err)
		}
	}

	st, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	db, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(db.Containers) != processCount {
		t.Fatalf("cross-process updates were lost: got %d containers, want %d", len(db.Containers), processCount)
	}
	names := map[string]bool{}
	for _, container := range db.Containers {
		names[container.Name] = true
	}
	if len(names) != processCount {
		t.Fatalf("cross-process metadata contains duplicates or missing entries: %#v", names)
	}
}

func TestMigrationToV2(t *testing.T) {
	root := t.TempDir()
	st, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	// Write a raw v1 fixture
	v1JSON := `{"version": 1, "containers": [{"id": "c1", "name": "Test1"}]}`
	if err := os.WriteFile(st.metaPath, []byte(v1JSON), 0600); err != nil {
		t.Fatal(err)
	}

	// Run migration
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}

	// Check backup
	backupPath := filepath.Join(root, "containers.v1.backup.json")
	if _, err := os.Stat(backupPath); errors.Is(err, os.ErrNotExist) {
		t.Fatalf("v1 backup was not created")
	}

	// Load DB and check results
	db, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}

	if db.Version != 2 {
		t.Fatalf("expected version 2, got %d", db.Version)
	}
	if len(db.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(db.Containers))
	}
	if db.Containers[0].NetworkMode != "direct" {
		t.Fatalf("expected networkMode 'direct', got '%s'", db.Containers[0].NetworkMode)
	}

	// Re-run migration should be idempotent
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	db2, _ := st.Load()
	if db2.Version != 2 {
		t.Fatalf("idempotent migration failed")
	}
}

func writeV1Fixture(t *testing.T, st *Store, fixture string) {
	t.Helper()
	if err := os.WriteFile(st.metaPath, []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestVersion1MigrationPreservesContainerFieldsAndDefaultsDirectMode(t *testing.T) {
	st, _ := New(t.TempDir())
	fixture := `{"version":1,"containers":[{"id":"c1","name":"Preserved","color":"#123456","icon":"x","temporary":true,"pendingCleanup":true,"profilePath":"C:/profile","browserType":"custom","browserExecutable":"C:/browser.exe","pid":42,"running":true}]}`
	writeV1Fixture(t, st, fixture)
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	db, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	c := db.Containers[0]
	if db.Version != CurrentVersion || c.NetworkMode != "direct" || c.Name != "Preserved" || c.Color != "#123456" || c.Icon != "x" || !c.Temporary || !c.PendingCleanup || c.ProfilePath != "C:/profile" || c.BrowserExecutable != "C:/browser.exe" || c.PID != 42 || !c.Running || c.State != model.StateRunning {
		t.Fatalf("fields changed: %#v", c)
	}
}

func TestVersion1MigrationCreatesOneTimeBackup(t *testing.T) {
	st, _ := New(t.TempDir())
	original := `{"version":1,"containers":[{"id":"original"}]}`
	writeV1Fixture(t, st, original)
	st.FaultInject = func(point string) error {
		if point == "before-atomic-replace" {
			return errors.New("interrupted")
		}
		return nil
	}
	if err := st.Migrate(); err == nil {
		t.Fatal("expected interruption")
	}
	backupPath := filepath.Join(st.root, "containers.v1.backup.json")
	first, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	changed := `{"version":1,"containers":[{"id":"changed"}]}`
	writeV1Fixture(t, st, changed)
	st.FaultInject = nil
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(backupPath)
	if string(first) != original || string(second) != original {
		t.Fatalf("backup was replaced: %s", second)
	}
}

func TestRepeatedMigrationIsIdempotent(t *testing.T) {
	st, _ := New(t.TempDir())
	writeV1Fixture(t, st, `{"version":1,"containers":[{"id":"c1","name":"same"}]}`)
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(st.metaPath)
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(st.metaPath)
	if string(first) != string(second) {
		t.Fatal("repeated migration changed metadata")
	}
}

func TestInterruptedMigrationRecoversFromOriginalAndExistingBackup(t *testing.T) {
	st, _ := New(t.TempDir())
	writeV1Fixture(t, st, `{"version":1,"containers":[{"id":"c1"}]}`)
	st.FaultInject = func(point string) error {
		if point == "before-atomic-replace" {
			return errors.New("power loss")
		}
		return nil
	}
	if err := st.Migrate(); err == nil {
		t.Fatal("expected interrupted migration")
	}
	raw, _ := os.ReadFile(st.metaPath)
	if !strings.Contains(string(raw), `"version":1`) {
		t.Fatalf("original was replaced: %s", raw)
	}
	st.FaultInject = nil
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	db, err := st.Load()
	if err != nil || db.Version != CurrentVersion || db.Containers[0].NetworkMode != "direct" {
		t.Fatalf("recovery failed: %#v %v", db, err)
	}
}

func TestMigrationRejectsFutureVersion(t *testing.T) {
	st, _ := New(t.TempDir())
	if err := os.WriteFile(st.metaPath, []byte(`{"version":999,"containers":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(); err == nil || !strings.Contains(err.Error(), "unsupported metadata version 999") {
		t.Fatalf("migration error: %v", err)
	}
	if _, err := st.Load(); err == nil || !strings.Contains(err.Error(), "unsupported metadata version 999") {
		t.Fatalf("load error: %v", err)
	}
}

func TestMigrationRunsUnderOSLock(t *testing.T) {
	st, _ := New(t.TempDir())
	writeV1Fixture(t, st, `{"version":1,"containers":[]}`)
	held, err := acquireFileLock(st.lockPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	st.lockTimeout = 100 * time.Millisecond
	if err := st.Migrate(); !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("migration did not honor OS lock: %v", err)
	}
}

func TestMigrationPersistsAtomicallyWithoutTemporaryFiles(t *testing.T) {
	st, _ := New(t.TempDir())
	writeV1Fixture(t, st, `{"version":1,"containers":[]}`)
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	if matches, _ := filepath.Glob(filepath.Join(st.root, ".containers-*.tmp")); len(matches) != 0 {
		t.Fatalf("temporary files remain: %v", matches)
	}
	if db, err := st.Load(); err != nil || db.Version != CurrentVersion {
		t.Fatalf("atomic metadata unreadable: %#v %v", db, err)
	}
}

func TestBrokenReferenceReporting(t *testing.T) {
	db := model.Database{Containers: []model.Container{{ID: "container", ProxyProfileID: "missing-proxy", EnvironmentTemplateID: "missing-template"}}, ProxyProfiles: []model.ProxyProfile{{ID: "proxy", CertificateIDs: []string{"missing-cert"}}}, EnvironmentTemplates: []model.EnvironmentTemplate{{ID: "template", ProxyProfileID: "missing-proxy-2", CertificateIDs: []string{"missing-cert-2"}}}}
	issues := BrokenReferences(db)
	if len(issues) != 5 {
		t.Fatalf("got %d issues: %#v", len(issues), issues)
	}
}
