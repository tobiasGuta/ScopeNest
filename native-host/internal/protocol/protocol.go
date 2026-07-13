package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

const (
	Version        = 1
	MaxMessageSize = 1024 * 1024
)

var ErrMessageTooLarge = errors.New("native message exceeds maximum size")

type Request struct {
	Version   int             `json:"version"`
	RequestID string          `json:"requestId"`
	Command   string          `json:"command"`
	Data      json.RawMessage `json:"data,omitempty"`
}

type ErrorDetail struct {
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

type Response struct {
	Version   int          `json:"version"`
	Success   bool         `json:"success"`
	RequestID string       `json:"requestId"`
	Command   string       `json:"command"`
	Data      any          `json:"data,omitempty"`
	Error     *ErrorDetail `json:"error,omitempty"`
	ErrorCode string       `json:"errorCode,omitempty"`
	Timestamp string       `json:"timestamp"`
}

func NewSuccess(req Request, data any) Response {
	return Response{Version: Version, Success: true, RequestID: req.RequestID, Command: req.Command, Data: data, Timestamp: time.Now().UTC().Format(time.RFC3339Nano)}
}

func NewError(req Request, code, message string) Response {
	return Response{Version: Version, Success: false, RequestID: req.RequestID, Command: req.Command, Error: &ErrorDetail{Message: message}, ErrorCode: code, Timestamp: time.Now().UTC().Format(time.RFC3339Nano)}
}

func ReadMessage(r io.Reader) ([]byte, error) {
	var size uint32
	if err := binary.Read(r, binary.LittleEndian, &size); err != nil {
		return nil, err
	}
	if size > MaxMessageSize {
		return nil, ErrMessageTooLarge
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, fmt.Errorf("read native message: %w", err)
	}
	return payload, nil
}

func WriteMessage(w io.Writer, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode native message: %w", err)
	}
	if len(payload) > MaxMessageSize {
		return ErrMessageTooLarge
	}
	if err := binary.Write(w, binary.LittleEndian, uint32(len(payload))); err != nil {
		return fmt.Errorf("write native message length: %w", err)
	}
	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("write native message: %w", err)
	}
	return nil
}
