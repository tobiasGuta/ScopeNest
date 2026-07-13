package main

import (
	"errors"
	"io"
	"os"

	"github.com/scopenest/scopenest/native-host/internal/browser"
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
	h := host.New(st, browser.ExecLauncher{})
	_ = h.Handle(protocol.Request{Version: 1, RequestID: "startup", Command: "cleanup_temporary_containers"})

	for {
		payload, readErr := protocol.ReadMessage(os.Stdin)
		if errors.Is(readErr, io.EOF) {
			return
		}
		if readErr != nil {
			req := protocol.Request{Version: 1, RequestID: "unknown", Command: "invalid_message"}
			code := "INVALID_MESSAGE"
			if errors.Is(readErr, protocol.ErrMessageTooLarge) {
				code = "MESSAGE_TOO_LARGE"
			}
			_ = protocol.WriteMessage(os.Stdout, protocol.NewError(req, code, readErr.Error()))
			return
		}
		req, decodeErr := host.DecodeRequest(payload)
		if decodeErr != nil {
			// Keep malformed request internals out of logs; the response remains structured.
			_ = protocol.WriteMessage(os.Stdout, protocol.NewError(req, host.ErrorCode(decodeErr), decodeErr.Error()))
			continue
		}
		response := h.Handle(req)
		if err := protocol.WriteMessage(os.Stdout, response); err != nil {
			return
		}
	}
}
