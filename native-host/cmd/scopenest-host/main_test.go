package main

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/scopenest/scopenest/native-host/internal/protocol"
)

type blockingStartupCleanupHost struct {
	started chan struct{}
	release chan struct{}
}

func (h *blockingStartupCleanupHost) Handle(req protocol.Request) protocol.Response {
	return protocol.NewSuccess(req, map[string]any{"hostVersion": "test"})
}

func (h *blockingStartupCleanupHost) StartStartupCleanup() {
	close(h.started)
	<-h.release
}

func TestMessageLoopWritesFirstPingBeforeSchedulingStartupCleanup(t *testing.T) {
	var in bytes.Buffer
	var out bytes.Buffer
	req := protocol.Request{Version: protocol.Version, RequestID: "first-ping", Command: "ping"}
	if err := protocol.WriteMessage(&in, req); err != nil {
		t.Fatal(err)
	}

	h := &blockingStartupCleanupHost{started: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		runMessageLoop(&in, &out, h)
		close(done)
	}()

	select {
	case <-h.started:
	case <-time.After(time.Second):
		t.Fatal("startup cleanup was not scheduled")
	}

	payload, err := protocol.ReadMessage(&out)
	if err != nil {
		t.Fatalf("ping was not written before startup cleanup: %v", err)
	}
	var response protocol.Response
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatal(err)
	}
	if !response.Success || response.RequestID != req.RequestID || response.Command != req.Command {
		t.Fatalf("unexpected first response: %#v", response)
	}

	close(h.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("message loop did not stop at EOF")
	}
}
