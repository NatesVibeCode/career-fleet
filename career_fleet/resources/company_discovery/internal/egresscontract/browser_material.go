package egresscontract

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The printed Process installs this profile after the explicitly selected
// system-launcher transition. It grants no network access and no ambient user
// data. Chromium's rendezvous and power-monitor resources are mechanical OS
// dependencies, not page-selected authorities. Clipboard services stay denied.
func browserSandboxProfile(selected Binding, directory, script string) (string, error) {
	if selected.Browser == nil || !browserAbsolute(directory) || !browserAbsolute(script) {
		return "", fmt.Errorf("browser sandbox material is absent")
	}
	// Seatbelt matches the resolved filesystem object. In particular the
	// platform's /var and /tmp aliases must not turn exact grants into denials.
	browser := *selected.Browser
	selected.Browser = &browser
	for _, path := range []*string{&directory, &script, &browser.NodeExecutable, &browser.PlaywrightDirectory, &browser.BrowserDirectory} {
		if !browserAbsolute(*path) {
			return "", fmt.Errorf("browser sandbox path is invalid")
		}
		real, err := filepath.EvalSymlinks(*path)
		if err != nil {
			return "", fmt.Errorf("browser sandbox material cannot be resolved")
		}
		*path = real
	}
	var p strings.Builder
	p.WriteString("(version 1)\n(deny default)\n(allow process-fork)\n(allow signal (target same-sandbox))\n(allow sysctl-read)\n(allow process-info*)\n(allow file-read-metadata)\n")
	for _, name := range []string{"com.apple.system.notification_center", "com.apple.system.logger", "com.apple.logd", "com.apple.system.opendirectoryd", "com.apple.system.opendirectoryd.libinfo", "com.apple.CoreServices.coreservicesd"} {
		fmt.Fprintf(&p, "(allow mach-lookup (global-name %q))\n", name)
	}
	p.WriteString("(allow ipc-posix-shm-read-data (ipc-posix-name \"apple.shm.notification_center\"))\n(allow iokit-open (iokit-user-client-class \"RootDomainUserClient\"))\n")
	p.WriteString("(allow mach-register (global-name-prefix \"org.chromium.Chromium.MachPortRendezvousServer.\"))\n(allow mach-lookup (global-name-prefix \"org.chromium.Chromium.MachPortRendezvousServer.\"))\n")
	p.WriteString("(allow file-read*)\n")
	for _, root := range []string{"/Library", "/opt", "/usr/local", "/private/etc", "/Users", "/Volumes", "/Network", "/private/var/folders", "/private/tmp", "/Home"} {
		fmt.Fprintf(&p, "(deny file-read* (subpath %q))\n", root)
	}
	for _, root := range []string{directory, selected.Browser.NodeExecutable, selected.Browser.PlaywrightDirectory, selected.Browser.BrowserDirectory} {
		if !browserAbsolute(root) {
			return "", fmt.Errorf("browser sandbox path is invalid")
		}
		for parent := filepath.Dir(root); parent != "."; parent = filepath.Dir(parent) {
			fmt.Fprintf(&p, "(allow file-read* (literal %q))\n", parent)
			if filepath.Dir(parent) == parent {
				break
			}
		}
		fmt.Fprintf(&p, "(allow file-read* (subpath %q))\n", root)
	}
	fmt.Fprintf(&p, "(allow file-write* (literal \"/dev/null\") (subpath %q))\n", directory)
	for _, root := range []string{selected.Browser.NodeExecutable, selected.Browser.PlaywrightDirectory, selected.Browser.BrowserDirectory, script} {
		fmt.Fprintf(&p, "(deny file-write* (subpath %q))\n", root)
	}
	fmt.Fprintf(&p, "(allow process-exec* (literal %q) (subpath %q))\n", selected.Browser.NodeExecutable, selected.Browser.BrowserDirectory)
	return p.String(), nil
}

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
