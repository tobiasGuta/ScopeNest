package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func TestNativeMessageRoundTrip(t *testing.T) {
	var buffer bytes.Buffer
	want := map[string]any{"success": true, "requestId": "one"}
	if err := WriteMessage(&buffer, want); err != nil {
		t.Fatal(err)
	}
	payload, err := ReadMessage(&buffer)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(payload, []byte(`"requestId":"one"`)) {
		t.Fatalf("unexpected payload: %s", payload)
	}
}

func TestRejectsOversizedMessageBeforeAllocation(t *testing.T) {
	var buffer bytes.Buffer
	if err := binary.Write(&buffer, binary.LittleEndian, uint32(MaxMessageSize+1)); err != nil {
		t.Fatal(err)
	}
	_, err := ReadMessage(&buffer)
	if !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("expected ErrMessageTooLarge, got %v", err)
	}
}

func TestReadMessageRejectsTruncatedPayload(t *testing.T) {
	var buffer bytes.Buffer
	_ = binary.Write(&buffer, binary.LittleEndian, uint32(20))
	buffer.WriteString("short")
	if _, err := ReadMessage(&buffer); err == nil {
		t.Fatal("expected truncated payload error")
	}
}
