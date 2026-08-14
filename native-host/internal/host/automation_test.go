package host

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp/kb"
	"github.com/scopenest/scopenest/native-host/internal/security"
)

func TestParseDevToolsActivePortStrictlyAcceptsOnlyFreshLoopbackEndpointParts(t *testing.T) {
	port, endpoint, err := parseDevToolsActivePort([]byte("43123\n/devtools/browser/abcdef\n"))
	if err != nil || port != 43123 || endpoint != "ws://127.0.0.1:43123/devtools/browser/abcdef" {
		t.Fatalf("valid endpoint = (%d, %q, %v)", port, endpoint, err)
	}
	for _, invalid := range [][]byte{
		[]byte("0\n/devtools/browser/x\n"),
		[]byte("80\n/devtools/browser/x\n"),
		[]byte("70000\n/devtools/browser/x\n"),
		[]byte("43123\nhttp://example.test\n"),
		[]byte("43123\n/devtools/browser/x?token=secret\n"),
		[]byte("43123\n/devtools/browser/x\nextra\n"),
		[]byte("43123\r\n/devtools/browser/x\n"),
	} {
		if _, _, err := parseDevToolsActivePort(invalid); err == nil {
			t.Fatalf("accepted invalid DevToolsActivePort content %q", invalid)
		}
	}
}

func TestAutomationEndpointWaitHandlesMissingEmptyTimeoutAndProcessExit(t *testing.T) {
	process := newControlledProcess(1001, false)
	if _, err := waitForAutomationEndpoint(t.TempDir(), process, 30*time.Millisecond); err == nil {
		t.Fatal("missing DevToolsActivePort did not time out")
	}
	profile := t.TempDir()
	if err := os.WriteFile(filepath.Join(profile, "DevToolsActivePort"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := waitForAutomationEndpoint(profile, process, 30*time.Millisecond); err == nil {
		t.Fatal("empty DevToolsActivePort was accepted")
	}
	exited := newControlledProcess(1002, false)
	exited.Exit()
	if _, err := waitForAutomationEndpoint(t.TempDir(), exited, time.Second); err == nil {
		t.Fatal("browser exit before readiness was accepted")
	}
}

func TestOpaquePageReferencesCannotCrossAutomationRuntimes(t *testing.T) {
	pageID, err := security.NewID()
	if err != nil {
		t.Fatal(err)
	}
	first := &automationRuntime{pages: map[string]target.ID{pageID: target.ID("target-a")}}
	second := &automationRuntime{pages: map[string]target.ID{}}
	if got, err := first.targetForPage(pageID); err != nil || got != target.ID("target-a") {
		t.Fatalf("own page reference = (%q, %v)", got, err)
	}
	if _, err := second.targetForPage(pageID); ErrorCode(err) != "PAGE_NOT_FOUND" {
		t.Fatalf("cross-runtime page reference error = %v", err)
	}
}

func TestRemoveStaleDevToolsActivePortRejectsNonRegularFile(t *testing.T) {
	profile := t.TempDir()
	path := filepath.Join(profile, "DevToolsActivePort")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := removeStaleDevToolsActivePort(profile); err == nil {
		t.Fatal("accepted a non-regular stale DevToolsActivePort path")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := removeStaleDevToolsActivePort(profile); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stale port file remains: %v", err)
	}
}

func TestFriendlyKeyNamesMapToSingleChromedpKeys(t *testing.T) {
	want := map[string]string{"Enter": kb.Enter, "ArrowUp": kb.ArrowUp, "Space": " "}
	for name, encoded := range want {
		if got := allowedKeys[name]; got != encoded {
			t.Fatalf("key %s maps to %q, want %q", name, got, encoded)
		}
	}
}
