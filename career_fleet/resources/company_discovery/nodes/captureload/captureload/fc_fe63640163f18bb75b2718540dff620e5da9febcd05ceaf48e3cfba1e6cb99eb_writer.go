package captureload

import "io"

type fc_fe63640163f18bb75b2718540dff620e5da9febcd05ceaf48e3cfba1e6cb99eb_writer interface {
	io.Writer
	WriteByte(byte) error
	WriteString(string) (int, error)
}
