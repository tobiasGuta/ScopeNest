package security

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIDsAreSecureAndValid(t *testing.T) {
	a, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	if a == b || ValidateID(a) != nil {
		t.Fatalf("invalid generated IDs: %q %q", a, b)
	}
	if ValidateID("../../etc/passwd") == nil {
		t.Fatal("accepted traversal as an ID")
	}
}

func TestResolveWithinRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if _, err := ResolveWithin(root, filepath.Join(root, "containers", "safe")); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveWithin(root, filepath.Join(root, "..", "escape")); err == nil || !IsManagedPathError(err) {
		t.Fatalf("expected managed path error, got %v", err)
	}
}

func TestResolveWithinRejectsSymlinkEscape(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := ResolveWithin(root, filepath.Join(link, "data")); err == nil {
		t.Fatal("accepted symlink escape")
	}
}

func TestURLValidation(t *testing.T) {
	valid := []string{"", "https://example.com/a?b=c", "http://localhost:8080"}
	for _, value := range valid {
		if _, err := ValidateURL(value); err != nil {
			t.Errorf("rejected %q: %v", value, err)
		}
	}
	invalid := []string{"example.com", "file:///etc/passwd", "javascript:alert(1)", "https://user:pass@example.com"}
	for _, value := range invalid {
		if _, err := ValidateURL(value); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
}

func TestInputValidation(t *testing.T) {
	if ValidateName("\x00bad") == nil {
		t.Fatal("accepted control character")
	}
	if ValidateColor("red") == nil {
		t.Fatal("accepted invalid color")
	}
	if ValidateIcon("123456789") == nil {
		t.Fatal("accepted oversized icon")
	}
	if ValidateBrowserType("unknown") == nil {
		t.Fatal("accepted unknown browser")
	}
}

func TestBrowserExecutableRejectsUnrelatedPrograms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cmd")
	if os.PathSeparator == '\\' {
		path += ".exe"
	}
	if err := os.WriteFile(path, []byte("not a browser"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateBrowserExecutable(path, "custom"); err == nil {
		t.Fatal("accepted unrelated executable")
	}
	chrome := filepath.Join(dir, "chrome")
	if os.PathSeparator == '\\' {
		chrome += ".exe"
	}
	if err := os.WriteFile(chrome, []byte("browser placeholder"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateBrowserExecutable(chrome, "custom"); err != nil {
		t.Fatalf("rejected recognized browser name: %v", err)
	}
}
