package host

import "fmt"

type commandError struct {
	code    string
	message string
}

func (e *commandError) Error() string { return e.message }

func fail(code, format string, args ...any) error {
	return &commandError{code: code, message: fmt.Sprintf(format, args...)}
}

func ErrorCode(err error) string {
	if typed, ok := err.(*commandError); ok {
		return typed.code
	}
	return "INVALID_REQUEST"
}
