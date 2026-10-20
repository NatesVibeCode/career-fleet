package egresscontract

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// PrepareSDKRuntime authenticates one source descriptor, then expands only regular
// files into private invocation custody. It never downloads or resolves packages.
// Archives contain dereferenced files: links, devices and escaping paths refuse.
func PrepareSDKRuntime(bundle, digest, binary, target string) (root, executable string, cleanup func(), err error) {
	cleanup = func() {}
	if target != runtime.GOOS+"-"+runtime.GOARCH || !sdkBundlePath(binary) {
		return "", "", cleanup, fmt.Errorf("SDK runtime target or executable selection differs")
	}
	input, err := os.Open(bundle)
	if err != nil {
		return "", "", cleanup, err
	}
	defer input.Close()
	h := sha256.New()
	if _, err = io.Copy(h, input); err != nil {
		return "", "", cleanup, err
	}
	if hex.EncodeToString(h.Sum(nil)) != digest {
		return "", "", cleanup, fmt.Errorf("SDK runtime bundle changed")
	}
	if _, err = input.Seek(0, 0); err != nil {
		return "", "", cleanup, err
	}
	observed := sha256.New()
	compressed, err := gzip.NewReader(io.TeeReader(input, observed))
	if err != nil {
		return "", "", cleanup, err
	}
	defer compressed.Close()
	root, err = os.MkdirTemp("", "praxis-sdk-runtime-")
	if err != nil {
		return "", "", cleanup, err
	}
	privateRoot := root
	cleanup = func() { _ = os.RemoveAll(privateRoot) }
	defer func() {
		if err != nil {
			cleanup()
		}
	}()
	archive := tar.NewReader(compressed)
	seen := map[string]bool{}
	for {
		var header *tar.Header
		header, err = archive.Next()
		if err == io.EOF {
			err = nil
			break
		}
		if err != nil {
			return "", "", cleanup, err
		}
		name := header.Name
		if header.Typeflag == tar.TypeDir {
			name = strings.TrimSuffix(name, "/")
		}
		if !sdkBundlePath(name) || seen[name] {
			return "", "", cleanup, fmt.Errorf("invalid or duplicate SDK runtime path")
		}
		seen[name] = true
		destination := filepath.Join(root, filepath.FromSlash(name))
		if header.Typeflag == tar.TypeDir {
			err = os.MkdirAll(destination, 0700)
		} else if header.Typeflag == tar.TypeReg {
			if err = os.MkdirAll(filepath.Dir(destination), 0700); err == nil {
				var output *os.File
				output, err = os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
				if err == nil {
					_, copyErr := io.Copy(output, archive)
					closeErr := output.Close()
					err = copyErr
					if err == nil {
						err = closeErr
					}
					mode := os.FileMode(0400)
					if header.Mode&0111 != 0 {
						mode = 0500
					}
					if err == nil {
						err = os.Chmod(destination, mode)
					}
				}
			}
		} else {
			err = fmt.Errorf("SDK runtime archive requires regular files; links and devices refuse")
		}
		if err != nil {
			return "", "", cleanup, err
		}
	}
	// Consume the gzip trailer as well; truncated/corrupt streams cannot execute.
	if _, err = io.Copy(io.Discard, compressed); err != nil {
		return "", "", cleanup, err
	}
	if _, err = io.Copy(observed, input); err != nil {
		return "", "", cleanup, err
	}
	if hex.EncodeToString(observed.Sum(nil)) != digest {
		return "", "", cleanup, fmt.Errorf("SDK runtime bundle changed while being extracted")
	}
	executable = filepath.Join(root, filepath.FromSlash(binary))
	info, statErr := os.Stat(executable)
	if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return "", "", cleanup, fmt.Errorf("SDK runtime executable absent or not executable")
	}
	return root, executable, cleanup, nil
}

func sdkBundlePath(name string) bool {
	return name != "" && name != "." && !path.IsAbs(name) && path.Clean(name) == name && !strings.HasPrefix(name, "../") && !strings.ContainsAny(name, "\\\x00")
}
