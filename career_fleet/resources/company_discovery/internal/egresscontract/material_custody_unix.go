//go:build darwin || linux

package egresscontract

import (
	"os"
	"path/filepath"
	"syscall"
)

// Read-only mounts from the selected file through the root prevent path
// replacement by the deployment principal. Platform administrators and mount
// namespace control remain trusted; this is not hostile-host isolation.
func immutableMaterialPath(path string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	for {
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
		var fs syscall.Statfs_t
		if syscall.Statfs(path, &fs) != nil || fs.Flags&1 == 0 {
			return false
		} // ST_RDONLY / MNT_RDONLY
		parent := filepath.Dir(path)
		if parent == path {
			return true
		}
		path = parent
	}
}
