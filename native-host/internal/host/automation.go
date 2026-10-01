package host

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	browserpkg "github.com/scopenest/scopenest/native-host/internal/browser"
	"github.com/scopenest/scopenest/native-host/internal/protocol"
	"github.com/scopenest/scopenest/native-host/internal/security"
)

const (
	automationStartupTimeout = 10 * time.Second
	automationActionTimeout  = 15 * time.Second
	maxSnapshotRunes         = 20_000
	maxScreenshotBytes       = 4 << 20
)

type automationRuntime struct {
	endpoint string
	port     int
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	pages    map[string]target.ID
	contexts map[target.ID]*automationPageContext
}

type automationPageContext struct {
	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
	err    error
}

type CDPEndpoint struct {
	Port         int    `json:"port"`
	WSEndpoint   string `json:"wsEndpoint"`
	HTTPEndpoint string `json:"httpEndpoint"`
}

type automationStatus struct {
	ID                string `json:"id"`
	AutomationEnabled bool   `json:"automationEnabled"`
	AutomationReady   bool   `json:"automationReady"`
	Running           bool   `json:"running"`
	PageCount         int    `json:"pageCount"`
}

type automationPage struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

type automationSnapshot struct {
	PageID    string `json:"pageId"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

type automationScreenshot struct {
	PageID string `json:"pageId"`
	PNG    string `json:"pngDataUrl"`
}

func removeStaleDevToolsActivePort(profile string) error {
	path := filepath.Join(profile, "DevToolsActivePort")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("DevToolsActivePort is not a regular file")
	}
	return os.Remove(path)
}

type automationConnector func(int, string, time.Duration) (*automationRuntime, error)

func waitForAutomationEndpoint(profile string, process browserpkg.Process, timeout time.Duration) (*automationRuntime, error) {
	return waitForAutomationEndpointWithConnector(profile, process, timeout, connectAutomationRuntime)
}

func waitForAutomationEndpointWithConnector(profile string, process browserpkg.Process, timeout time.Duration, connector automationConnector) (*automationRuntime, error) {
	path := filepath.Join(profile, "DevToolsActivePort")
	deadline := time.Now().Add(timeout)
	var lastConnectErr error
	for time.Now().Before(deadline) {
		if !process.Running() {
			return nil, errors.New("browser exited before automation became ready")
		}
		info, err := os.Lstat(path)
		if err == nil {
			if !info.Mode().IsRegular() || info.Size() > 4096 {
				return nil, errors.New("invalid DevToolsActivePort file")
			}
			data, readErr := os.ReadFile(path)
			if info.Size() > 0 && readErr == nil {
				port, endpoint, parseErr := parseDevToolsActivePort(data)
				if parseErr == nil {
					remaining := time.Until(deadline)
					if remaining <= 0 {
						break
					}
					attemptTimeout := automationActionTimeout
					if remaining < attemptTimeout {
						attemptTimeout = remaining
					}
					runtime, connectErr := connector(port, endpoint, attemptTimeout)
					if connectErr == nil {
						return runtime, nil
					}
					lastConnectErr = connectErr
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		time.Sleep(25 * time.Millisecond)
	}
	if lastConnectErr != nil {
		return nil, fmt.Errorf("timed out connecting to DevTools endpoint: %w", lastConnectErr)
	}
	return nil, errors.New("timed out waiting for DevToolsActivePort")
}

func parseDevToolsActivePort(data []byte) (int, string, error) {
	if !utf8.Valid(data) {
		return 0, "", errors.New("invalid DevToolsActivePort encoding")
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 2 || strings.ContainsAny(lines[0], "\r \t") || strings.ContainsAny(lines[1], "\r \t") {
		return 0, "", errors.New("invalid DevToolsActivePort format")
	}
	port, err := strconv.Atoi(lines[0])
	if err != nil || port < 1024 || port > 65535 {
		return 0, "", errors.New("invalid DevTools port")
	}
	const browserPathPrefix = "/devtools/browser/"
	browserID := strings.TrimPrefix(lines[1], browserPathPrefix)
	if !strings.HasPrefix(lines[1], browserPathPrefix) || browserID == "" || len(browserID) > 200 || !isSafeDevToolsBrowserID(browserID) {
		return 0, "", errors.New("invalid DevTools browser path")
	}
	return port, "ws://127.0.0.1:" + strconv.Itoa(port) + lines[1], nil
}

func isSafeDevToolsBrowserID(value string) bool {
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}

func connectAutomationRuntime(port int, endpoint string, timeout time.Duration) (*automationRuntime, error) {
	allocatorCtx, cancelAllocator := chromedp.NewRemoteAllocator(context.Background(), endpoint)
	ctx, cancel := chromedp.NewContext(allocatorCtx)
	runtime := &automationRuntime{endpoint: endpoint, port: port, ctx: ctx, pages: map[string]target.ID{}, contexts: map[target.ID]*automationPageContext{}}
	runtime.cancel = func() { cancel(); cancelAllocator() }
	err := runInitialChromedp(ctx, timeout, chromedp.ActionFunc(func(actionCtx context.Context) error {
		_, _, _, _, _, err := browser.GetVersion().Do(actionCtx)
		return err
	}))
	if err != nil {
		runtime.close()
		return nil, err
	}
	return runtime, nil
}

func runInitialChromedp(ctx context.Context, timeout time.Duration, actions ...chromedp.Action) error {
	ready := make(chan error, 1)
	go func() {
		ready <- chromedp.Run(ctx, actions...)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-ready:
		return err
	case <-timer.C:
		return context.DeadlineExceeded
	}
}

func (r *automationRuntime) close() {
	if r != nil && r.cancel != nil {
		r.cancel()
	}
}

func (h *Host) clearAutomation(id string) {
	h.mu.Lock()
	runtime := h.automations[id]
	delete(h.automations, id)
	h.mu.Unlock()
	runtime.close()
}

func (h *Host) automationForMCP(id, expectedName string) (*automationRuntime, error) {
	if err := security.ValidateID(id); err != nil {
		return nil, fail("INVALID_CONTAINER_ID", "%v", err)
	}
	if strings.TrimSpace(expectedName) == "" {
		return nil, fail("INVALID_ARGUMENT", "an expected container name is required")
	}
	db, err := h.store.Load()
	if err != nil {
		return nil, err
	}
	for _, container := range db.Containers {
		if container.ID != id {
			continue
		}
		if container.Name != expectedName {
			return nil, fail("CONTAINER_NAME_MISMATCH", "container name changed")
		}
		if !isStandardBrowserType(container.BrowserType) {
			return nil, fail("AUTOMATION_REQUIRES_STANDARD_BROWSER", "automation requires a standard Chromium-family browser")
		}
		if container.State != "running" {
			return nil, fail("AUTOMATION_NOT_RUNNING", "container automation is not running")
		}
		h.mu.Lock()
		process := h.processes[id]
		runtime := h.automations[id]
		h.mu.Unlock()
		if process == nil || !process.Running() {
			if !container.AutomationEnabled {
				return nil, fail("AUTOMATION_DISABLED", "browser automation is not enabled for this container")
			}
			return nil, fail("AUTOMATION_NOT_READY", "the owned browser automation endpoint is not ready")
		}
		if runtime == nil {
			return nil, fail("AUTOMATION_DISABLED", "browser automation is not enabled for this container")
		}
		return runtime, nil
	}
	return nil, fail("NOT_FOUND", "container was not found")
}

func (h *Host) BrowserStatusForMCP(id, expectedName string) protocol.Response {
	runtime, err := h.automationForMCP(id, expectedName)
	if err != nil {
		return commandResponse(protocol.Request{Version: protocol.Version, Command: "browser_status"}, nil, err)
	}
	pages, err := runtime.listPages()
	if err != nil {
		return commandResponse(protocol.Request{Version: protocol.Version, Command: "browser_status"}, nil, automationError(err))
	}
	return commandResponse(protocol.Request{Version: protocol.Version, Command: "browser_status"}, automationStatus{ID: id, AutomationEnabled: true, AutomationReady: true, Running: true, PageCount: len(pages)}, nil)
}

func (h *Host) BrowserCDPEndpointForMCP(id, expectedName string) protocol.Response {
	runtime, err := h.automationForMCP(id, expectedName)
	if err != nil {
		return commandResponse(protocol.Request{Version: protocol.Version, Command: "get_cdp_endpoint"}, nil, err)
	}
	return commandResponse(protocol.Request{Version: protocol.Version, Command: "get_cdp_endpoint"}, CDPEndpoint{
		Port:         runtime.port,
		WSEndpoint:   runtime.endpoint,
		HTTPEndpoint: fmt.Sprintf("http://127.0.0.1:%d", runtime.port),
	}, nil)
}

func (h *Host) BrowserListPagesForMCP(id, expectedName string) protocol.Response {
	runtime, err := h.automationForMCP(id, expectedName)
	if err == nil {
		var pages []automationPage
		pages, err = runtime.listPages()
		if err == nil {
			return commandResponse(protocol.Request{Version: protocol.Version, Command: "browser_list_pages"}, pages, nil)
		}
	}
	return commandResponse(protocol.Request{Version: protocol.Version, Command: "browser_list_pages"}, nil, automationError(err))
}

func (h *Host) BrowserOpenPageForMCP(id, expectedName, rawURL string) protocol.Response {
	runtime, err := h.automationForMCP(id, expectedName)
	if err == nil {
		var page automationPage
		page, err = runtime.openPage(rawURL)
		if err == nil {
			return commandResponse(protocol.Request{Version: protocol.Version, Command: "browser_open_page"}, page, nil)
		}
	}
	return commandResponse(protocol.Request{Version: protocol.Version, Command: "browser_open_page"}, nil, automationError(err))
}

func (h *Host) BrowserNavigateForMCP(id, expectedName, pageID, rawURL string) protocol.Response {
	runtime, err := h.automationForMCP(id, expectedName)
	if err == nil {
		err = runtime.navigate(pageID, rawURL)
		if err == nil {
			return commandResponse(protocol.Request{Version: protocol.Version, Command: "browser_navigate"}, map[string]any{"pageId": pageID, "navigated": true}, nil)
		}
	}
	return commandResponse(protocol.Request{Version: protocol.Version, Command: "browser_navigate"}, nil, automationError(err))
}

func (h *Host) BrowserSnapshotForMCP(id, expectedName, pageID string) protocol.Response {
	runtime, err := h.automationForMCP(id, expectedName)
	if err == nil {
		var snapshot automationSnapshot
		snapshot, err = runtime.snapshot(pageID)
		if err == nil {
			return commandResponse(protocol.Request{Version: protocol.Version, Command: "browser_snapshot"}, snapshot, nil)
		}
	}
	return commandResponse(protocol.Request{Version: protocol.Version, Command: "browser_snapshot"}, nil, automationError(err))
}

func (h *Host) BrowserClickForMCP(id, expectedName, pageID, selector string) protocol.Response {
	runtime, err := h.automationForMCP(id, expectedName)
	if err == nil {
		err = runtime.click(pageID, selector)
		if err == nil {
			return commandResponse(protocol.Request{Version: protocol.Version, Command: "browser_click"}, map[string]any{"pageId": pageID, "clicked": true}, nil)
		}
	}
	return commandResponse(protocol.Request{Version: protocol.Version, Command: "browser_click"}, nil, automationError(err))
}

func (h *Host) BrowserTypeForMCP(id, expectedName, pageID, selector, text string) protocol.Response {
	runtime, err := h.automationForMCP(id, expectedName)
	if err == nil {
		err = runtime.typeText(pageID, selector, text)
		if err == nil {
			return commandResponse(protocol.Request{Version: protocol.Version, Command: "browser_type"}, map[string]any{"pageId": pageID, "typed": true}, nil)
		}
	}
	return commandResponse(protocol.Request{Version: protocol.Version, Command: "browser_type"}, nil, automationError(err))
}

func (h *Host) BrowserPressKeyForMCP(id, expectedName, pageID, key string) protocol.Response {
	runtime, err := h.automationForMCP(id, expectedName)
	if err == nil {
		err = runtime.pressKey(pageID, key)
		if err == nil {
			return commandResponse(protocol.Request{Version: protocol.Version, Command: "browser_press_key"}, map[string]any{"pageId": pageID, "pressed": key}, nil)
		}
	}
	return commandResponse(protocol.Request{Version: protocol.Version, Command: "browser_press_key"}, nil, automationError(err))
}

func (h *Host) BrowserScreenshotForMCP(id, expectedName, pageID string) protocol.Response {
	runtime, err := h.automationForMCP(id, expectedName)
	if err == nil {
		var screenshot automationScreenshot
		screenshot, err = runtime.screenshot(pageID)
		if err == nil {
			return commandResponse(protocol.Request{Version: protocol.Version, Command: "browser_screenshot"}, screenshot, nil)
		}
	}
	return commandResponse(protocol.Request{Version: protocol.Version, Command: "browser_screenshot"}, nil, automationError(err))
}

func automationError(err error) error {
	if err == nil {
		return nil
	}
	var commandErr *commandError
	if errors.As(err, &commandErr) {
		return err
	}
	return fail("AUTOMATION_ACTION_FAILED", "the requested browser action could not be completed")
}

func (r *automationRuntime) listPages() ([]automationPage, error) {
	ctx, cancel := context.WithTimeout(r.ctx, automationActionTimeout)
	defer cancel()
	targets, err := chromedp.Targets(ctx)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	live := map[target.ID]bool{}
	pages := make([]automationPage, 0, len(targets))
	for _, info := range targets {
		if info.Type != "page" {
			continue
		}
		live[info.TargetID] = true
		ref, err := r.pageRefLocked(info.TargetID)
		if err != nil {
			r.mu.Unlock()
			return nil, err
		}
		pages = append(pages, automationPage{ID: ref, Title: info.Title, URL: info.URL})
	}
	staleContexts := make([]*automationPageContext, 0)
	for ref, targetID := range r.pages {
		if !live[targetID] {
			delete(r.pages, ref)
			if pageCtx := r.contexts[targetID]; pageCtx != nil {
				delete(r.contexts, targetID)
				staleContexts = append(staleContexts, pageCtx)
			}
		}
	}
	r.mu.Unlock()
	for _, pageCtx := range staleContexts {
		pageCtx.cancel()
	}
	return pages, nil
}

func (r *automationRuntime) pageRefLocked(targetID target.ID) (string, error) {
	for ref, existing := range r.pages {
		if existing == targetID {
			return ref, nil
		}
	}
	ref, err := security.NewID()
	if err != nil {
		return "", err
	}
	r.pages[ref] = targetID
	return ref, nil
}

func (r *automationRuntime) targetForPage(pageID string) (target.ID, error) {
	if security.ValidateID(pageID) != nil {
		return "", fail("INVALID_PAGE_ID", "page identifier is invalid")
	}
	r.mu.Lock()
	targetID, ok := r.pages[pageID]
	r.mu.Unlock()
	if !ok {
		return "", fail("PAGE_NOT_FOUND", "page does not belong to this container")
	}
	return targetID, nil
}

func (r *automationRuntime) pageContext(pageID string) (context.Context, context.CancelFunc, error) {
	targetID, err := r.targetForPage(pageID)
	if err != nil {
		return nil, nil, err
	}
	r.mu.Lock()
	if r.contexts == nil {
		r.contexts = map[target.ID]*automationPageContext{}
	}
	pageCtx := r.contexts[targetID]
	if pageCtx == nil {
		ctx, cancel := chromedp.NewContext(r.ctx, chromedp.WithTargetID(targetID))
		pageCtx = &automationPageContext{ctx: ctx, cancel: cancel}
		r.contexts[targetID] = pageCtx
	}
	r.mu.Unlock()

	pageCtx.once.Do(func() {
		pageCtx.err = runInitialChromedp(pageCtx.ctx)
		if pageCtx.err != nil {
			pageCtx.cancel()
		}
	})
	if pageCtx.err != nil {
		return nil, nil, pageCtx.err
	}
	ctx, cancel := context.WithTimeout(pageCtx.ctx, automationActionTimeout)
	return ctx, cancel, nil
}

func (r *automationRuntime) openPage(rawURL string) (automationPage, error) {
	url, err := security.ValidateURL(rawURL)
	if err != nil {
		return automationPage{}, fail("INVALID_URL", "%v", err)
	}
	ctx, cancel := context.WithTimeout(r.ctx, automationActionTimeout)
	defer cancel()
	var targetID target.ID
	err = chromedp.Run(ctx, chromedp.ActionFunc(func(actionCtx context.Context) error {
		chromedpContext := chromedp.FromContext(actionCtx)
		if chromedpContext == nil || chromedpContext.Browser == nil {
			return errors.New("browser executor is unavailable")
		}
		var createErr error
		targetID, createErr = target.CreateTarget(url).Do(cdp.WithExecutor(actionCtx, chromedpContext.Browser))
		return createErr
	}))
	if err != nil {
		return automationPage{}, err
	}
	r.mu.Lock()
	ref, err := r.pageRefLocked(targetID)
	r.mu.Unlock()
	if err != nil {
		return automationPage{}, err
	}
	return automationPage{ID: ref, URL: url}, nil
}

func (r *automationRuntime) navigate(pageID, rawURL string) error {
	url, err := security.ValidateURL(rawURL)
	if err != nil {
		return fail("INVALID_URL", "%v", err)
	}
	ctx, cancel, err := r.pageContext(pageID)
	if err != nil {
		return err
	}
	defer cancel()
	return chromedp.Run(ctx, chromedp.Navigate(url))
}

func (r *automationRuntime) snapshot(pageID string) (automationSnapshot, error) {
	ctx, cancel, err := r.pageContext(pageID)
	if err != nil {
		return automationSnapshot{}, err
	}
	defer cancel()
	var title, url, text string
	if err := chromedp.Run(ctx, chromedp.Title(&title), chromedp.Location(&url), chromedp.Text("body", &text, chromedp.ByQuery)); err != nil {
		return automationSnapshot{}, err
	}
	truncated := false
	if len([]rune(text)) > maxSnapshotRunes {
		text = string([]rune(text)[:maxSnapshotRunes])
		truncated = true
	}
	return automationSnapshot{PageID: pageID, Title: title, URL: url, Text: text, Truncated: truncated}, nil
}

func validateSelector(selector string) (string, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" || len(selector) > 512 || !utf8.ValidString(selector) {
		return "", fail("INVALID_SELECTOR", "selector must contain 1 to 512 printable characters")
	}
	for _, char := range selector {
		if unicode.IsControl(char) {
			return "", fail("INVALID_SELECTOR", "selector must contain 1 to 512 printable characters")
		}
	}
	return selector, nil
}

func (r *automationRuntime) click(pageID, selector string) error {
	selector, err := validateSelector(selector)
	if err != nil {
		return err
	}
	ctx, cancel, err := r.pageContext(pageID)
	if err != nil {
		return err
	}
	defer cancel()
	return chromedp.Run(ctx, chromedp.Click(selector, chromedp.ByQuery))
}

func (r *automationRuntime) typeText(pageID, selector, value string) error {
	selector, err := validateSelector(selector)
	if err != nil {
		return err
	}
	if value == "" || len(value) > 8192 || !utf8.ValidString(value) {
		return fail("INVALID_TEXT", "text must contain 1 to 8192 UTF-8 bytes")
	}
	ctx, cancel, err := r.pageContext(pageID)
	if err != nil {
		return err
	}
	defer cancel()
	return chromedp.Run(ctx, chromedp.SendKeys(selector, value, chromedp.ByQuery))
}

var allowedKeys = map[string]string{
	"Enter": kb.Enter, "Tab": kb.Tab, "Escape": kb.Escape, "Backspace": kb.Backspace,
	"Delete": kb.Delete, "ArrowUp": kb.ArrowUp, "ArrowDown": kb.ArrowDown,
	"ArrowLeft": kb.ArrowLeft, "ArrowRight": kb.ArrowRight, "Home": kb.Home,
	"End": kb.End, "PageUp": kb.PageUp, "PageDown": kb.PageDown, "Space": " ",
}

func (r *automationRuntime) pressKey(pageID, key string) error {
	encodedKey, ok := allowedKeys[key]
	if !ok {
		return fail("INVALID_KEY", "key is not supported")
	}
	ctx, cancel, err := r.pageContext(pageID)
	if err != nil {
		return err
	}
	defer cancel()
	return chromedp.Run(ctx, chromedp.KeyEvent(encodedKey))
}

func (r *automationRuntime) screenshot(pageID string) (automationScreenshot, error) {
	ctx, cancel, err := r.pageContext(pageID)
	if err != nil {
		return automationScreenshot{}, err
	}
	defer cancel()
	var png []byte
	if err := chromedp.Run(ctx, chromedp.CaptureScreenshot(&png)); err != nil {
		return automationScreenshot{}, err
	}
	if len(png) > maxScreenshotBytes {
		return automationScreenshot{}, fmt.Errorf("screenshot exceeds output limit")
	}
	return automationScreenshot{PageID: pageID, PNG: "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)}, nil
}
