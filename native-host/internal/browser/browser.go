package browser

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/scopenest/scopenest/native-host/internal/security"
)

type Candidate struct {
	Type string `json:"type"`
	Name string `json:"name"`
	Path string `json:"path"`
}

type Launcher interface {
	Start(executable string, args []string) (Process, error)
}

type ExecLauncher struct{}

type Process interface {
	PID() int
	Running() bool
	Wait() error
	Terminate() error
}

type ProxyOptions struct {
	Enabled     bool
	Protocol    string
	Host        string
	Port        int
	BypassRules []string
}

func Arguments(profilePath, rawURL string, proxy ProxyOptions) ([]string, error) {
	if profilePath == "" || !filepath.IsAbs(profilePath) {
		return nil, errors.New("profile path must be absolute")
	}
	args := []string{"--user-data-dir=" + profilePath, "--profile-directory=Default", "--new-window", "--no-first-run"}

	if proxy.Enabled {
		// e.g. --proxy-server="http=127.0.0.1:8080;https=127.0.0.1:8080"
		// or socks5://127.0.0.1:1080
		var serverArg string
		if proxy.Protocol == "socks4" || proxy.Protocol == "socks5" {
			serverArg = fmt.Sprintf("%s://%s:%d", proxy.Protocol, proxy.Host, proxy.Port)
		} else {
			addr := fmt.Sprintf("%s:%d", proxy.Host, proxy.Port)
			// Chrome supports scheme-specific proxy configuration
			serverArg = fmt.Sprintf("http=%s;https=%s", addr, addr)
		}

		args = append(args, "--proxy-server="+serverArg)

		// Chrome QUIC does not traverse proxies reliably, and many proxies intercepting traffic
		// do not support UDP/QUIC interception, so disable it when using a proxy.
		args = append(args, "--disable-quic")

		if len(proxy.BypassRules) > 0 {
			// e.g. --proxy-bypass-list="*.local,192.168.1.1/24"
			args = append(args, "--proxy-bypass-list="+strings.Join(proxy.BypassRules, ","))
		}
	}

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
