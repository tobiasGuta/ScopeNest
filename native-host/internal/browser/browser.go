package browser

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/scopenest/scopenest/native-host/internal/security"
)

type Candidate struct {
	Type string `json:"type"`
	Name string `json:"name"`
	Path string `json:"path"`
}

type Launcher interface {
	Start(executable string, args []string) (*os.Process, error)
}

type ExecLauncher struct{}

func (ExecLauncher) Start(executable string, args []string) (*os.Process, error) {
	cmd := exec.Command(executable, args...)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd.Process, nil
}

func Arguments(profilePath, rawURL string) ([]string, error) {
	if profilePath == "" || !filepath.IsAbs(profilePath) {
		return nil, errors.New("profile path must be absolute")
	}
	args := []string{"--user-data-dir=" + profilePath, "--profile-directory=Default", "--new-window", "--no-first-run"}
	if rawURL != "" {
		validated, err := security.ValidateURL(rawURL)
		if err != nil {
			return nil, err
		}
		args = append(args, validated)
	}
	return args, nil
}

func Detect() []Candidate {
	seen := map[string]bool{}
	result := []Candidate{}
	add := func(browserType, name, path string) {
		if path == "" {
			return
		}
		if abs, err := security.ValidateExecutable(path); err == nil && !seen[abs] {
			seen[abs] = true
			result = append(result, Candidate{Type: browserType, Name: name, Path: abs})
		}
	}
	if runtime.GOOS == "windows" {
		programFiles := []string{os.Getenv("PROGRAMFILES"), os.Getenv("PROGRAMFILES(X86)"), os.Getenv("LOCALAPPDATA")}
		for _, base := range programFiles {
			add("chrome", "Google Chrome", filepath.Join(base, "Google", "Chrome", "Application", "chrome.exe"))
			add("edge", "Microsoft Edge", filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe"))
			add("brave", "Brave", filepath.Join(base, "BraveSoftware", "Brave-Browser", "Application", "brave.exe"))
		}
	} else {
		commands := []Candidate{
			{Type: "chrome", Name: "Google Chrome", Path: "google-chrome"},
			{Type: "chromium", Name: "Chromium", Path: "chromium"},
			{Type: "chromium", Name: "Chromium", Path: "chromium-browser"},
			{Type: "edge", Name: "Microsoft Edge", Path: "microsoft-edge"},
			{Type: "brave", Name: "Brave", Path: "brave-browser"},
		}
		for _, candidate := range commands {
			if path, err := exec.LookPath(candidate.Path); err == nil {
				add(candidate.Type, candidate.Name, path)
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}
