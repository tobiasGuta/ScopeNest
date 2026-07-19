package security

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"unicode/utf8"
)

var supportedBrowserTypes = []string{"chrome", "chromium", "edge", "brave", "custom"}

var supportedNetworkModes = []string{"direct", "proxy", "template"}

var (
	idPattern    = regexp.MustCompile(`^[a-f0-9]{32}$`)
	colorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	browserTypes = func() map[string]bool {
		result := make(map[string]bool, len(supportedBrowserTypes))
		for _, browserType := range supportedBrowserTypes {
			result[browserType] = true
		}
		return result
	}()
	browserNames = map[string]map[string]bool{
		"chrome":   {"chrome": true, "google-chrome": true, "google-chrome-stable": true},
		"chromium": {"chromium": true, "chromium-browser": true},
		"edge":     {"msedge": true, "microsoft-edge": true, "microsoft-edge-stable": true},
		"brave":    {"brave": true, "brave-browser": true, "brave-browser-stable": true},
		"custom":   {"chrome": true, "google-chrome": true, "google-chrome-stable": true, "chromium": true, "chromium-browser": true, "msedge": true, "microsoft-edge": true, "microsoft-edge-stable": true, "brave": true, "brave-browser": true, "brave-browser-stable": true, "vivaldi": true, "opera": true, "opera-launcher": true, "arc": true},
	}
	errOutsideRoot = errors.New("path is outside the managed ScopeNest directory")
)

func SupportedBrowserTypes() []string { return append([]string(nil), supportedBrowserTypes...) }

func SupportedNetworkModes() []string { return append([]string(nil), supportedNetworkModes...) }

func NewID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate secure container id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func ValidateID(id string) error {
	if !idPattern.MatchString(id) {
		return errors.New("container id must be a 32-character lowercase hexadecimal identifier")
	}
	return nil
}

func ValidateName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 80 {
		return errors.New("container name must contain 1 to 80 characters")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return errors.New("container name contains control characters")
		}
		if IsBidiControl(r) {
			return errors.New("container name contains bidirectional formatting characters")
		}
	}
	return nil
}

func ValidateColor(color string) error {
	if !colorPattern.MatchString(color) {
		return errors.New("color must be a six-digit hexadecimal color")
	}
	return nil
}

func ValidateIcon(icon string) error {
	if utf8.RuneCountInString(icon) > 8 {
		return errors.New("icon must contain at most 8 characters")
	}
	for _, r := range icon {
		if r < 0x20 || r == 0x7f {
			return errors.New("icon contains control characters")
		}
		if IsBidiControl(r) {
			return errors.New("icon contains bidirectional formatting characters")
		}
	}
	return nil
}

// IsBidiControl identifies directional formatting characters that can make a
// trusted visual label appear in a different order. It intentionally does not
// reject all Cf characters because emoji sequences commonly require U+200D.
func IsBidiControl(r rune) bool {
	switch {
	case r == '\u061c', r == '\u200e', r == '\u200f':
		return true
	case r >= '\u202a' && r <= '\u202e':
		return true
	case r >= '\u2066' && r <= '\u2069':
		return true
	default:
		return false
	}
}

func ValidateBrowserType(browserType string) error {
	if !browserTypes[browserType] {
		return errors.New("unsupported browser type")
	}
	return nil
}

func ValidateNetworkMode(networkMode string) error {
	for _, supported := range supportedNetworkModes {
		if networkMode == supported {
			return nil
		}
	}
	return errors.New("network mode must be direct, proxy, or template")
}

func ValidateURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if len(raw) > 8192 {
		return "", errors.New("URL exceeds 8192 bytes")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return "", errors.New("URL must be an absolute http or https URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("only http and https URLs are supported")
	}
	if u.Hostname() == "" || u.User != nil {
		return "", errors.New("URL must have a host and must not include credentials")
	}
	return u.String(), nil
}

func ResolveWithin(root, candidate string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	rootEval := evalExisting(rootAbs)
	candidateEval := evalExisting(candidateAbs)
	rel, err := filepath.Rel(rootEval, candidateEval)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errOutsideRoot
	}
	return candidateAbs, nil
}

func evalExisting(path string) string {
	cleaned := filepath.Clean(path)
	current := cleaned
	missing := []string{}
	for {
		evaluated, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				evaluated = filepath.Join(evaluated, missing[i])
			}
			return evaluated
		}
		parent := filepath.Dir(current)
		if parent == current {
			return cleaned
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func ValidateExecutable(path string) (string, error) {
	if strings.TrimSpace(path) == "" || strings.ContainsRune(path, '\x00') {
		return "", errors.New("browser executable path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", errors.New("browser executable path is invalid")
	}
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() {
		return "", errors.New("browser executable does not exist or is not a file")
	}
	if runtime.GOOS != "windows" && info.Mode()&0111 == 0 {
		return "", errors.New("browser executable is not executable")
	}
	return abs, nil
}

func ValidateBrowserExecutable(path, browserType string) (string, error) {
	if err := ValidateBrowserType(browserType); err != nil {
		return "", err
	}
	abs, err := ValidateExecutable(path)
	if err != nil {
		return "", err
	}
	name := strings.ToLower(filepath.Base(abs))
	name = strings.TrimSuffix(name, filepath.Ext(name))
	name = strings.ReplaceAll(name, "_", "-")
	if !browserNames[browserType][name] {
		return "", errors.New("executable name does not match a recognized Chromium-family browser")
	}
	return abs, nil
}

func IsManagedPathError(err error) bool { return errors.Is(err, errOutsideRoot) }
