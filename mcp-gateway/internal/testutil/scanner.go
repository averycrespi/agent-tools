package testutil

import (
	"bytes"
	"fmt"
	"io"
)

const scannerBufferBytes = 32 * 1024

type CanaryScanner struct {
	canary []byte
}

type LeakError struct {
	Sink string
}

func (leak *LeakError) Error() string {
	return fmt.Sprintf("secret canary detected in %s", leak.Sink)
}

func NewCanaryScanner(canary []byte) (*CanaryScanner, error) {
	if len(canary) == 0 {
		return nil, fmt.Errorf("canary must not be empty")
	}
	return &CanaryScanner{canary: append([]byte(nil), canary...)}, nil
}

func (scanner *CanaryScanner) Scan(sink string, reader io.Reader) error {
	buffer := make([]byte, scannerBufferBytes)
	carry := make([]byte, 0, len(scanner.canary)-1)

	for {
		count, readErr := reader.Read(buffer)
		if count > 0 {
			window := make([]byte, 0, len(carry)+count)
			window = append(window, carry...)
			window = append(window, buffer[:count]...)
			if bytes.Contains(window, scanner.canary) {
				return &LeakError{Sink: sink}
			}
			carry = trailingBytes(window, len(scanner.canary)-1)
		}
		if readErr != nil {
			if readErr == io.EOF {
				return nil
			}
			return fmt.Errorf("scan %s: %w", sink, readErr)
		}
	}
}

func trailingBytes(value []byte, count int) []byte {
	if count <= 0 {
		return nil
	}
	if len(value) < count {
		count = len(value)
	}
	return append([]byte(nil), value[len(value)-count:]...)
}
