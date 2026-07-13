package store

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
