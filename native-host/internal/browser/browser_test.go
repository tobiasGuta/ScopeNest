package browser

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
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

func TestManagedProcessTreeHelper(t *testing.T) {
	if len(os.Args) < 3 {
		return
	}
	role := os.Args[len(os.Args)-2]
	marker := os.Args[len(os.Args)-1]
	switch role {
	case "tree-root":
		cmd := exec.Command(os.Args[0], "-test.run=TestManagedProcessTreeHelper", "--", "tree-leaf", marker)
		cmd.Stdin = nil
		cmd.Stdout = nil
		cmd.Stderr = nil
		if err := cmd.Start(); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	case "tree-leaf":
		if err := os.WriteFile(marker, []byte("started"), 0600); err != nil {
			os.Exit(3)
		}
		time.Sleep(30 * time.Second)
		_ = os.WriteFile(marker+".completed", []byte("not terminated"), 0600)
		os.Exit(0)
	}
}

func TestExecLauncherTerminatesOwnedProcessTree(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "child-started")
	process, err := (ExecLauncher{}).Start(os.Args[0], []string{"-test.run=TestManagedProcessTreeHelper", "--", "tree-root", marker})
	if err != nil {
		t.Fatal(err)
	}
	defer process.Terminate()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("managed child process did not start")
		}
		time.Sleep(25 * time.Millisecond)
	}

	if err := process.Terminate(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- process.Wait() }()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("owned process tree did not terminate")
	}
	if _, err := os.Stat(marker + ".completed"); !os.IsNotExist(err) {
		t.Fatalf("managed child escaped termination: %v", err)
	}
}
