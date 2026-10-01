//go:build linux

package store

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/scopenest/scopenest/native-host/internal/security"
)

func TestProfileInUseIgnoresDefinitelyStaleLinuxChromiumLock(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, err := security.NewID()
	if err != nil {
		t.Fatal(err)
	}
	profile, err := st.EnsureProfile(id)
	if err != nil {
		t.Fatal(err)
	}
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}

	const definitelyMissingPID = 2147483647
	if err := os.Symlink(fmt.Sprintf("%s-%d", hostname, definitelyMissingPID), filepath.Join(profile, "SingletonLock")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/tmp/scopenest-stale-singleton-socket", filepath.Join(profile, "SingletonSocket")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("stale-cookie", filepath.Join(profile, "SingletonCookie")); err != nil {
		t.Fatal(err)
	}

	inUse, err := st.ProfileInUse(id)
	if err != nil {
		t.Fatal(err)
	}
	if inUse {
		t.Fatal("definitely stale same-host Chromium lock reported profile in use")
	}
}

func TestProfileInUseKeepsLiveLinuxChromiumLock(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, err := security.NewID()
	if err != nil {
		t.Fatal(err)
	}
	profile, err := st.EnsureProfile(id)
	if err != nil {
		t.Fatal(err)
	}
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fmt.Sprintf("%s-%d", hostname, os.Getpid()), filepath.Join(profile, "SingletonLock")); err != nil {
		t.Fatal(err)
	}

	inUse, err := st.ProfileInUse(id)
	if err != nil {
		t.Fatal(err)
	}
	if !inUse {
		t.Fatal("live same-host Chromium lock was not reported in use")
	}
}

func TestProfileInUseKeepsCrossHostLinuxChromiumLock(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, err := security.NewID()
	if err != nil {
		t.Fatal(err)
	}
	profile, err := st.EnsureProfile(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("other-host.example-2147483647", filepath.Join(profile, "SingletonLock")); err != nil {
		t.Fatal(err)
	}

	inUse, err := st.ProfileInUse(id)
	if err != nil {
		t.Fatal(err)
	}
	if !inUse {
		t.Fatal("cross-host Chromium lock was not conservatively reported in use")
	}
}
