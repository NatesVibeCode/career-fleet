package egresscontract

import (
	"fmt"
	"os"
	"path/filepath"
)

// Snapshot the selected multi-file runtime before launching it. The manager
// retains the original deployment binding for cross-Node identity; only the
// private, reverified paths are passed to Node and Chromium.
func snapshotBrowserRuntime(binding Binding, pin BrowserRuntimePin, root string) (Binding, error) {
	selected := binding
	browser := *binding.Browser
	selected.Browser = &browser
	if !immutableMaterialPath(browser.NodeExecutable) {
		nodeRoot := filepath.Join(root, "node-runtime")
		if err := os.Mkdir(nodeRoot, 0700); err != nil {
			return Binding{}, err
		}
		browser.NodeExecutable = filepath.Join(nodeRoot, filepath.Base(binding.Browser.NodeExecutable))
		if err := copySelectedMaterial(binding.Browser.NodeExecutable, browser.NodeExecutable, pin.NodeSHA256, true); err != nil {
			return Binding{}, err
		}
	}
	for _, tree := range []struct {
		source string
		target *string
		name   string
		digest string
	}{
		{binding.Browser.PlaywrightDirectory, &browser.PlaywrightDirectory, "playwright", pin.DriverSHA256},
		{binding.Browser.BrowserDirectory, &browser.BrowserDirectory, "browser", pin.BundleSHA256},
	} {
		if immutableMaterialPath(tree.source) {
			continue
		}
		target := filepath.Join(root, tree.name)
		if err := copyBrowserTree(tree.source, target); err != nil {
			return Binding{}, err
		}
		digest, err := BrowserTreeDigest(target)
		if err != nil || digest != tree.digest {
			return Binding{}, fmt.Errorf("browser runtime changed during selection")
		}
		*tree.target = target
	}
	rel, err := filepath.Rel(binding.Browser.BrowserDirectory, binding.Browser.BrowserExecutable)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return Binding{}, fmt.Errorf("browser executable outside selected tree")
	}
	browser.BrowserExecutable = filepath.Join(browser.BrowserDirectory, rel)
	if err = verifyBrowserRuntime(selected, pin); err != nil {
		return Binding{}, err
	}
	return selected, nil
}

func copyBrowserTree(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("browser runtime contains indirect material")
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, rel)
		if entry.IsDir() {
			return os.Mkdir(destination, 0700)
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("browser runtime material is not regular")
		}
		digest, err := browserFileDigest(path)
		if err != nil {
			return err
		}
		return copySelectedMaterial(path, destination, digest, info.Mode().Perm()&0111 != 0)
	})
}
