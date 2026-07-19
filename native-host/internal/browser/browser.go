package browser

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/scopenest/scopenest/native-host/internal/security"
)

type Candidate struct {
	Type string `json:"type"`
	Name string `json:"name"`
	Path string `json:"path"`
}

type Launcher interface {
	Start(spec LaunchSpec) (Process, error)
}

type ExecLauncher struct{}

type Process interface {
	PID() int
	Running() bool
	Wait() error
	Terminate() error
}

// VisualIdentity is the non-sensitive, user-configured identity applied to the
// browser's initial top-level window.
type VisualIdentity struct {
	Name  string
	Color string
	Icon  string
}

// LaunchSpec keeps the executable, arguments, and visual identity together so
// every launch caller uses the same browser implementation.
type LaunchSpec struct {
	Executable string
	Arguments  []string
	Identity   VisualIdentity
}

type ProxyOptions struct {
	Enabled     bool
	Protocol    string
	Host        string
	Port        int
	BypassRules []string
}

type ArgumentOptions struct {
	ProfilePath string
	URL         string
	Proxy       ProxyOptions
	Identity    VisualIdentity
}

func Arguments(options ArgumentOptions) ([]string, error) {
	if options.ProfilePath == "" || !filepath.IsAbs(options.ProfilePath) {
		return nil, errors.New("profile path must be absolute")
	}
	if err := validateVisualIdentity(options.Identity); err != nil {
		return nil, err
	}
	args := []string{
		"--user-data-dir=" + options.ProfilePath,
		"--profile-directory=Default",
		"--new-window",
		"--no-first-run",
		"--window-name=" + WindowLabel(options.Identity),
	}

	if options.Proxy.Enabled {
		// e.g. --proxy-server="http=http://127.0.0.1:8080;https=http://127.0.0.1:8080"
		// or socks5://127.0.0.1:1080
		addr := net.JoinHostPort(options.Proxy.Host, strconv.Itoa(options.Proxy.Port))
		var serverArg string
		if options.Proxy.Protocol == "socks4" || options.Proxy.Protocol == "socks5" {
			serverArg = fmt.Sprintf("%s://%s", options.Proxy.Protocol, addr)
		} else {
			scheme := "http"
			if options.Proxy.Protocol == "https" {
				scheme = "https"
			}
			proxyURL := fmt.Sprintf("%s://%s", scheme, addr)
			// Chrome supports scheme-specific proxy configuration
			serverArg = fmt.Sprintf("http=%s;https=%s", proxyURL, proxyURL)
		}

		args = append(args, "--proxy-server="+serverArg)

		// Chrome QUIC does not traverse proxies reliably, and many proxies intercepting traffic
		// do not support UDP/QUIC interception, so disable it when using a proxy.
		args = append(args, "--disable-quic")

		if len(options.Proxy.BypassRules) > 0 {
			// e.g. --proxy-bypass-list="*.local,192.168.1.1/24"
			args = append(args, "--proxy-bypass-list="+strings.Join(options.Proxy.BypassRules, ","))
		}
	}

	if options.URL != "" {
		validated, err := security.ValidateURL(options.URL)
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
