package main

import (
	"errors"
	"io"
	"os"

	"github.com/scopenest/scopenest/native-host/internal/browser"
	"github.com/scopenest/scopenest/native-host/internal/certstore"
	"github.com/scopenest/scopenest/native-host/internal/host"
	"github.com/scopenest/scopenest/native-host/internal/protocol"
	"github.com/scopenest/scopenest/native-host/internal/store"
)

func main() {
	dataDir, err := host.DefaultDataDir()
	if err != nil {
		os.Exit(1)
	}
	st, err := store.New(dataDir)
	if err != nil {
		os.Exit(1)
	}
	if err := st.Migrate(); err != nil {
		os.Exit(1)
	}
	certMgr := certstore.NewManager(st, certstore.NewTrustStore())
	h := host.New(st, browser.ExecLauncher{}, certMgr)
	runMessageLoop(os.Stdin, os.Stdout, h)
}

type messageHost interface {
	Handle(protocol.Request) protocol.Response
	StartStartupCleanup()
}

func runMessageLoop(in io.Reader, out io.Writer, h messageHost) {
	startupCleanupScheduled := false
	for {
		payload, readErr := protocol.ReadMessage(in)
		if errors.Is(readErr, io.EOF) {
			return
		}
		if readErr != nil {
			req := protocol.Request{Version: 1, RequestID: "unknown", Command: "invalid_message"}
			code := "INVALID_MESSAGE"
			if errors.Is(readErr, protocol.ErrMessageTooLarge) {
				code = "MESSAGE_TOO_LARGE"
			}
			_ = protocol.WriteMessage(out, protocol.NewError(req, code, readErr.Error()))
			return
		}
		req, decodeErr := host.DecodeRequest(payload)
		if decodeErr != nil {
			// Keep malformed request internals out of logs; the response remains structured.
			_ = protocol.WriteMessage(out, protocol.NewError(req, host.ErrorCode(decodeErr), decodeErr.Error()))
			continue
		}
		response := h.Handle(req)
		if err := protocol.WriteMessage(out, response); err != nil {
			return
		}
		if !startupCleanupScheduled {
			startupCleanupScheduled = true
			h.StartStartupCleanup()
		}
	}
}
