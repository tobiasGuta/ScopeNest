package host

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/scopenest/scopenest/native-host/internal/browser"
	"github.com/scopenest/scopenest/native-host/internal/model"
	"github.com/scopenest/scopenest/native-host/internal/store"
)

func TestRealBrowserAutomationKeepsTwoTemporaryIdentitiesSeparate(t *testing.T) {
	executable := os.Getenv("SCOPENEST_AUTOMATION_ACCEPTANCE_BROWSER")
	if executable == "" {
		t.Skip("set SCOPENEST_AUTOMATION_ACCEPTANCE_BROWSER to run the real-browser acceptance test")
	}

	page := `<!doctype html><meta charset="utf-8"><title>ScopeNest automation acceptance</title>
<input id="identity"><button id="save" onclick="localStorage.setItem('identity', identity.value); document.cookie='identity='+identity.value+'; SameSite=Lax'; render()">Save identity</button><output id="result"></output>
<script>function render(){result.textContent='storage='+localStorage.getItem('identity')+' cookie='+document.cookie} render()</script>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	defer server.Close()

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := New(st, browser.ExecLauncher{}, nil)
	create := func(name string) model.Container {
		response := h.Handle(request(t, "create_temporary_container", containerInput{Name: name, Color: "#725cff", BrowserType: "chrome", BrowserExecutable: executable, NetworkMode: "direct", AutomationEnabled: true}))
		if !response.Success {
			t.Fatalf("create %s: %#v", name, response)
		}
		return response.Data.(model.Container)
	}
	a := create("Acceptance A")
	b := create("Acceptance B")
	for _, container := range []model.Container{a, b} {
		response := h.LaunchForMCP(container.ID, container.Name, server.URL, nil)
		if !response.Success {
			t.Fatalf("launch %s: %#v", container.Name, response)
		}
	}
	defer func() {
		_, _ = h.close(a.ID)
		_, _ = h.close(b.ID)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			db, loadErr := st.Load()
			if loadErr == nil && len(db.Containers) == 0 {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
	}()

	h.mu.Lock()
	portA, portB := h.automations[a.ID].port, h.automations[b.ID].port
	endpointA, endpointB := h.automations[a.ID].endpoint, h.automations[b.ID].endpoint
	h.mu.Unlock()
	if portA == portB || portA < 1024 || portB < 1024 {
		t.Fatalf("ephemeral endpoints are not distinct: A=%d B=%d", portA, portB)
	}
	if !strings.HasPrefix(endpointA, "ws://127.0.0.1:") || !strings.HasPrefix(endpointB, "ws://127.0.0.1:") {
		t.Fatalf("automation endpoints are not fixed to IPv4 loopback")
	}
	assertNotListeningOnNonLoopback(t, portA)
	assertNotListeningOnNonLoopback(t, portB)

	open := func(container model.Container) automationPage {
		response := h.BrowserOpenPageForMCP(container.ID, container.Name, server.URL)
		if !response.Success {
			t.Fatalf("open %s: %#v", container.Name, response)
		}
		var result automationPage
		raw, _ := json.Marshal(response.Data)
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	pageA, pageB := open(a), open(b)
	setIdentity := func(container model.Container, pageID, identity string) automationSnapshot {
		if response := h.BrowserTypeForMCP(container.ID, container.Name, pageID, "#identity", identity); !response.Success {
			t.Fatalf("type %s: %#v", container.Name, response)
		}
		if response := h.BrowserClickForMCP(container.ID, container.Name, pageID, "#save"); !response.Success {
			t.Fatalf("click %s: %#v", container.Name, response)
		}
		if response := h.BrowserNavigateForMCP(container.ID, container.Name, pageID, server.URL+"/navigated"); !response.Success {
			t.Fatalf("navigate %s: %#v", container.Name, response)
		}
		response := h.BrowserSnapshotForMCP(container.ID, container.Name, pageID)
		if !response.Success {
			t.Fatalf("snapshot %s: %#v", container.Name, response)
		}
		var result automationSnapshot
		raw, _ := json.Marshal(response.Data)
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	snapshotA := setIdentity(a, pageA.ID, "A")
	snapshotB := setIdentity(b, pageB.ID, "B")
	if !strings.Contains(snapshotA.Text, "storage=A cookie=identity=A") || strings.Contains(snapshotA.Text, "storage=B") {
		t.Fatalf("container A state = %q", snapshotA.Text)
	}
	if !strings.Contains(snapshotB.Text, "storage=B cookie=identity=B") || strings.Contains(snapshotB.Text, "storage=A") {
		t.Fatalf("container B state = %q", snapshotB.Text)
	}
	if response := h.BrowserSnapshotForMCP(b.ID, b.Name, pageA.ID); response.Success || response.ErrorCode != "PAGE_NOT_FOUND" {
		t.Fatalf("container B accepted A page reference: %#v", response)
	}

	if _, err := h.close(a.ID); err != nil {
		t.Fatalf("close %s: %v", a.Name, err)
	}
	if _, err := h.close(b.ID); err != nil {
		t.Fatalf("close %s: %v", b.Name, err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		db, err := st.Load()
		if err == nil && len(db.Containers) == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	db, _ := st.Load()
	t.Fatalf("temporary containers were not cleaned after owned browser exit: %#v", db.Containers)
}

func assertNotListeningOnNonLoopback(t *testing.T, port int) {
	t.Helper()
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	checked := false
	for _, address := range addresses {
		ip, _, err := net.ParseCIDR(address.String())
		if err != nil || ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		checked = true
		connection, dialErr := net.DialTimeout("tcp", net.JoinHostPort(ip.String(), fmt.Sprint(port)), 250*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			t.Fatalf("DevTools port %d accepted a connection on non-loopback address %s", port, ip)
		}
	}
	if !checked {
		t.Log("no non-loopback interface was available for the listener acceptance check")
	}
}
