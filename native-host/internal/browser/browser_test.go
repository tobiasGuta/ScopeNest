package browser

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestWindowLabel(t *testing.T) {
	tests := []struct {
		name     string
		identity VisualIdentity
		expected string
	}{
		{name: "icon and name", identity: VisualIdentity{Name: "Research", Icon: "🔬"}, expected: "[🔬] ScopeNest — Research"},
		{name: "name only", identity: VisualIdentity{Name: "Work"}, expected: "ScopeNest — Work"},
		{name: "safe default", identity: VisualIdentity{}, expected: "ScopeNest"},
		{name: "normalized whitespace", identity: VisualIdentity{Name: "  red\tteam\u2028window  ", Icon: "  🧪  "}, expected: "[🧪] ScopeNest — red team window"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if actual := WindowLabel(test.identity); actual != test.expected {
				t.Fatalf("WindowLabel() = %q, want %q", actual, test.expected)
			}
		})
	}
}

func TestWindowLabelIsRuneSafeAndBounded(t *testing.T) {
	label := WindowLabel(VisualIdentity{Name: strings.Repeat("界", 200), Icon: "🧪"})
	if !utf8.ValidString(label) {
		t.Fatal("label is not valid UTF-8")
	}
	if count := utf8.RuneCountInString(label); count != maxWindowLabelRunes {
		t.Fatalf("label contains %d runes, want %d", count, maxWindowLabelRunes)
	}
}

func TestWindowLabelDefensivelyRemovesControlsAndExcludesLaunchMetadata(t *testing.T) {
	label := WindowLabel(VisualIdentity{Name: "  Team\n\r\tWindow\u2029  ", Icon: "A\u0000"})
	if label != "[A] ScopeNest — Team Window" {
		t.Fatalf("defensively normalized label = %q", label)
	}
	for _, forbidden := range []string{
		"0123456789abcdef0123456789abcdef",
		`C:\\Users\\example\\ScopeNest\\containers`,
		"https://example.com/",
		"--proxy-server",
	} {
		if strings.Contains(label, forbidden) {
			t.Fatalf("label contains unrelated launch metadata %q: %q", forbidden, label)
		}
	}
}

func TestArgumentsIncludeOneSeparatedWindowName(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "profile")
	identity := VisualIdentity{Name: "Research --remote-debugging-port=9222", Color: "#123456", Icon: "🔬"}
	args, err := Arguments(ArgumentOptions{ProfilePath: profile, Identity: identity})
	if err != nil {
		t.Fatal(err)
	}
	want := "--window-name=[🔬] ScopeNest — Research --remote-debugging-port=9222"
	count := 0
	for _, arg := range args {
		if strings.HasPrefix(arg, "--window-name=") {
			count++
			if arg != want {
				t.Fatalf("window-name argument = %q, want %q", arg, want)
			}
		}
	}
	if count != 1 {
		t.Fatalf("found %d window-name arguments, want 1: %#v", count, args)
	}
}

func TestArgumentsRejectIdentityControlCharacterInjection(t *testing.T) {
	identities := []VisualIdentity{
		{Name: "Research\n--remote-debugging-port=9222", Color: "#123456"},
		{Name: "Research", Color: "#123456", Icon: "A\n--incognito"},
	}
	for _, identity := range identities {
		_, err := Arguments(ArgumentOptions{ProfilePath: filepath.Join(t.TempDir(), "profile"), Identity: identity})
		if err == nil {
			t.Fatalf("accepted a control character in visual identity %#v", identity)
		}
	}
}

func TestIdentityColorLuminanceAndContrast(t *testing.T) {
	tests := []struct {
		value         string
		wantLuminance float64
		wantText      rgbColor
	}{
		{value: "#000000", wantLuminance: 0, wantText: rgbColor{red: 0xff, green: 0xff, blue: 0xff}},
		{value: "#ffffff", wantLuminance: 1, wantText: rgbColor{}},
		{value: "#ff0000", wantLuminance: 0.2126, wantText: rgbColor{}},
		{value: "#00ff00", wantLuminance: 0.7152, wantText: rgbColor{}},
		{value: "#0000ff", wantLuminance: 0.0722, wantText: rgbColor{red: 0xff, green: 0xff, blue: 0xff}},
		{value: "#666666", wantLuminance: 0.132868, wantText: rgbColor{red: 0xff, green: 0xff, blue: 0xff}},
		{value: "#777777", wantLuminance: 0.184475, wantText: rgbColor{}},
	}
	for _, test := range tests {
		color, err := parseRGB(test.value)
		if err != nil {
			t.Fatal(err)
		}
		actual := relativeLuminance(color)
		if difference := actual - test.wantLuminance; difference < -0.00001 || difference > 0.00001 {
			t.Fatalf("relativeLuminance(%s) = %f, want %f", test.value, actual, test.wantLuminance)
		}
		if text := contrastingTextColor(color); text != test.wantText {
			t.Fatalf("contrastingTextColor(%s) = %#v, want %#v", test.value, text, test.wantText)
		}
	}
}

func TestArgumentsAreSeparatedAndStable(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "profile with spaces")
	args, err := Arguments(ArgumentOptions{ProfilePath: profile, URL: "https://example.com/path?q=hello%20world"})
	if err != nil {
		t.Fatal(err)
	}
	if args[0] != "--user-data-dir="+profile {
		t.Fatalf("profile argument was altered: %#v", args)
	}
	if args[len(args)-1] != "https://example.com/path?q=hello%20world" {
		t.Fatalf("URL argument was altered: %#v", args)
	}

	// Ensure proxy and quic flags are strictly absent in non-proxy mode
	for _, arg := range args {
		if arg == "--disable-quic" || strings.HasPrefix(arg, "--proxy-server") || strings.HasPrefix(arg, "--proxy-bypass-list") {
			t.Fatalf("unexpected proxy-related argument found in non-proxy mode: %s", arg)
		}
	}
}

func TestArgumentsRejectUnsafeURLSchemes(t *testing.T) {
	if _, err := Arguments(ArgumentOptions{ProfilePath: filepath.Join(t.TempDir(), "profile"), URL: "file:///etc/passwd"}); err == nil {
		t.Fatal("accepted file URL")
	}
}

func TestArgumentsRequireAbsoluteProfile(t *testing.T) {
	if _, err := Arguments(ArgumentOptions{ProfilePath: "relative/profile"}); err == nil {
		t.Fatal("accepted relative profile path")
	}
}

func TestArgumentsWithProxyOptions(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "proxy_profile")

	// Test HTTP proxy without bypass rules
	args, err := Arguments(ArgumentOptions{ProfilePath: profile, Proxy: ProxyOptions{
		Enabled:  true,
		Protocol: "http",
		Host:     "127.0.0.1",
		Port:     8080,
	}})
	if err != nil {
		t.Fatal(err)
	}

	foundServer := false
	foundQuic := false
	for _, arg := range args {
		if arg == "--proxy-server=http=http://127.0.0.1:8080;https=http://127.0.0.1:8080" {
			foundServer = true
		}
		if arg == "--disable-quic" {
			foundQuic = true
		}
	}
	if !foundServer {
		t.Fatalf("missing or incorrect proxy-server arg: %#v", args)
	}
	if !foundQuic {
		t.Fatalf("missing disable-quic arg: %#v", args)
	}

	// Test SOCKS5 with bypass rules
	args, err = Arguments(ArgumentOptions{ProfilePath: profile, Proxy: ProxyOptions{
		Enabled:     true,
		Protocol:    "socks5",
		Host:        "192.168.1.1",
		Port:        1080,
		BypassRules: []string{"*.local", "localhost"},
	}})
	if err != nil {
		t.Fatal(err)
	}

	foundServer = false
	foundBypass := false
	for _, arg := range args {
		if arg == "--proxy-server=socks5://192.168.1.1:1080" {
			foundServer = true
		}
		if arg == "--proxy-bypass-list=*.local,localhost" {
			foundBypass = true
		}
	}
	if !foundServer {
		t.Fatalf("missing or incorrect proxy-server arg for socks5: %#v", args)
	}
	if !foundBypass {
		t.Fatalf("missing or incorrect proxy-bypass-list arg: %#v", args)
	}
}

func TestArgumentsUseJoinHostPortForIPv6ProxyHosts(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "proxy_profile")
	cases := []struct {
		name     string
		options  ProxyOptions
		expected string
	}{
		{"http-ipv4", ProxyOptions{Enabled: true, Protocol: "http", Host: "127.0.0.1", Port: 8080}, "--proxy-server=http=http://127.0.0.1:8080;https=http://127.0.0.1:8080"},
		{"http-localhost", ProxyOptions{Enabled: true, Protocol: "http", Host: "localhost", Port: 8080}, "--proxy-server=http=http://localhost:8080;https=http://localhost:8080"},
		{"http-ipv6", ProxyOptions{Enabled: true, Protocol: "http", Host: "::1", Port: 8080}, "--proxy-server=http=http://[::1]:8080;https=http://[::1]:8080"},
		{"https-ipv4", ProxyOptions{Enabled: true, Protocol: "https", Host: "127.0.0.1", Port: 8443}, "--proxy-server=http=https://127.0.0.1:8443;https=https://127.0.0.1:8443"},
		{"socks5-ipv6", ProxyOptions{Enabled: true, Protocol: "socks5", Host: "::1", Port: 1080}, "--proxy-server=socks5://[::1]:1080"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args, err := Arguments(ArgumentOptions{ProfilePath: profile, Proxy: tc.options})
			if err != nil {
				t.Fatal(err)
			}
			if !containsArg(args, tc.expected) {
				t.Fatalf("missing %q in %#v", tc.expected, args)
			}
		})
	}
}

func containsArg(args []string, expected string) bool {
	for _, arg := range args {
		if arg == expected {
			return true
		}
	}
	return false
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
	process, err := (ExecLauncher{}).Start(LaunchSpec{
		Executable: os.Args[0],
		Arguments:  []string{"-test.run=TestManagedProcessTreeHelper", "--", "tree-root", marker},
	})
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
