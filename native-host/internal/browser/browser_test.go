package browser

import (
	"path/filepath"
	"testing"
)

func TestArgumentsAreSeparatedAndStable(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "profile with spaces")
	args, err := Arguments(profile, "https://example.com/path?q=hello%20world")
	if err != nil {
		t.Fatal(err)
	}
	if args[0] != "--user-data-dir="+profile {
		t.Fatalf("profile argument was altered: %#v", args)
	}
	if args[len(args)-1] != "https://example.com/path?q=hello%20world" {
		t.Fatalf("URL argument was altered: %#v", args)
	}
}

func TestArgumentsRejectUnsafeURLSchemes(t *testing.T) {
	if _, err := Arguments(filepath.Join(t.TempDir(), "profile"), "file:///etc/passwd"); err == nil {
		t.Fatal("accepted file URL")
	}
}

func TestArgumentsRequireAbsoluteProfile(t *testing.T) {
	if _, err := Arguments("relative/profile", ""); err == nil {
		t.Fatal("accepted relative profile path")
	}
}
