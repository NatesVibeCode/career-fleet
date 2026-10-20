package egresscontract

import (
	"bytes"
	"io"
	"sort"
)

// SDKDiagnostics streams attempt evidence to the Process owner's diagnostic
// channel. A trailing window keeps credential values private even when writes
// split them. Its size follows the selected credential material, not a quota.
type SDKDiagnostics struct {
	output  io.Writer
	secrets [][]byte
	pending []byte
}

func NewSDKDiagnostics(output io.Writer, secrets []string) *SDKDiagnostics {
	w := &SDKDiagnostics{output: output}
	for _, secret := range secrets {
		if secret != "" {
			w.secrets = append(w.secrets, []byte(secret))
		}
	}
	sort.Slice(w.secrets, func(i, j int) bool { return len(w.secrets[i]) > len(w.secrets[j]) })
	return w
}

func (w *SDKDiagnostics) Write(p []byte) (int, error) {
	w.pending = append(w.pending, p...)
	if err := w.emit(false); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *SDKDiagnostics) Close() error { return w.emit(true) }

func (w *SDKDiagnostics) emit(final bool) error {
	var output []byte
	pos := 0
scan:
	for pos < len(w.pending) {
		for _, secret := range w.secrets {
			if bytes.HasPrefix(w.pending[pos:], secret) {
				output = append(output, []byte("[redacted-credential]")...)
				pos += len(secret)
				continue scan
			}
			if bytes.HasPrefix(secret, w.pending[pos:]) {
				if !final {
					break scan
				}
				output = append(output, []byte("[redacted-credential]")...)
				pos = len(w.pending)
				break scan
			}
		}
		output = append(output, w.pending[pos])
		pos++
	}
	if len(output) > 0 {
		if n, err := w.output.Write(output); err != nil {
			return err
		} else if n != len(output) {
			return io.ErrShortWrite
		}
	}
	w.pending = append(w.pending[:0], w.pending[pos:]...)
	return nil
}
