package egresscontract

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// snapshotRuntimeMaterial copies selected bytes through one open file and hashes
// the copy, then supplies only private read-only paths to the adapter. A rename,
// symlink swap or in-place write at the original deployment path cannot change
// what the adapter subsequently opens. This is custody inside a trusted Process
// OS principal; it is not isolation from a hostile same-principal process or an
// administrator. Such deployments require platform-enforced immutable custody.
func snapshotRuntimeMaterial(policy Policy, binding Binding) (Binding, func(), error) {
	cleanup := func() {}
	if len(binding.RuntimeMaterial) == 0 {
		return binding, cleanup, nil
	}
	permissions := map[string]RuntimeMaterialPermission{}
	for _, op := range policy.Operations {
		if op.ID == binding.Operation {
			for _, p := range op.RuntimeMaterial {
				permissions[p.ID] = p
			}
		}
	}
	dir, err := os.MkdirTemp("", "praxis-selected-material-")
	if err != nil {
		return Binding{}, cleanup, err
	}
	var once sync.Once
	cleanup = func() { once.Do(func() { _ = os.RemoveAll(dir) }) }
	selected := append([]RuntimeMaterialBinding(nil), binding.RuntimeMaterial...)
	for i, material := range selected {
		permission, ok := permissions[material.ID]
		if !ok {
			cleanup()
			return Binding{}, func() {}, fmt.Errorf("undeclared runtime material")
		}
		// Keep platform binaries in OS-enforced immutable custody: moving a
		// platform-signed executable may invalidate its OS execution authority.
		if immutableMaterialPath(material.Path) {
			continue
		}
		// A separate private directory preserves each selected basename without
		// interpreting IDs as paths or depending on ambient sibling files.
		root := filepath.Join(dir, fmt.Sprint(i))
		if err = os.Mkdir(root, 0700); err != nil {
			cleanup()
			return Binding{}, func() {}, err
		}
		path := filepath.Join(root, filepath.Base(material.Path))
		if err = copySelectedMaterial(material.Path, path, permission.SHA256, permission.Kind == "executable"); err != nil {
			cleanup()
			return Binding{}, func() {}, err
		}
		selected[i].Path = path
	}
	binding.RuntimeMaterial = selected
	return binding, cleanup, nil
}

func copySelectedMaterial(source, target, digest string, executable bool) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil || !info.Mode().IsRegular() || (executable && info.Mode().Perm()&0111 == 0) {
		return fmt.Errorf("regular selected material required")
	}
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(output, hash), input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if hex.EncodeToString(hash.Sum(nil)) != digest {
		return fmt.Errorf("runtime material changed during selection")
	}
	mode := os.FileMode(0400)
	if executable {
		mode = 0500
	}
	return os.Chmod(target, mode)
}
