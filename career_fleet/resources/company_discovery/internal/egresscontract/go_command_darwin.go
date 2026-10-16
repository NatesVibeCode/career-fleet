//go:build darwin

package egresscontract

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// This lowers the existing native Seatbelt mechanism to exact Go executable
// grants. No network permission is present and no shell is involved.
func goSandboxCommand(ctx context.Context, pin GoRuntimePin, args []string, roots map[string]string, writes []string, image string) (*exec.Cmd, error) {
	var p strings.Builder
	p.WriteString("(version 1)\n(deny default)\n(allow process-fork)\n(allow sysctl-read)\n(allow process-info*)\n")
	for _, name := range []string{"com.apple.system.notification_center", "com.apple.system.logger", "com.apple.logd", "com.apple.system.opendirectoryd", "com.apple.CoreServices.coreservicesd"} {
		fmt.Fprintf(&p, "(allow mach-lookup (global-name %q))\n", name)
	}
	// Loader and system runtime dependencies are mechanical platform resources.
	read := []string{"/usr/lib", "/System/Library", "/Library/Apple/System/Library", "/private/var/db/dyld"}
	for _, r := range roots {
		read = append(read, r)
	}
	read = append(read, writes...)
	sort.Strings(read)
	for _, r := range read {
		for parent := filepath.Dir(r); parent != "."; parent = filepath.Dir(parent) {
			for _, path := range consumerPathForms(parent) {
				fmt.Fprintf(&p, "(allow file-read* (literal %q))\n", path)
			}
			if filepath.Dir(parent) == parent {
				break
			}
		}
		for _, path := range consumerPathForms(r) {
			fmt.Fprintf(&p, "(allow file-read* (subpath %q))\n(allow file-map-executable (subpath %q))\n", path, path)
		}
	}
	executables := []string{pin.Executable}
	for _, c := range pin.Components {
		executables = append(executables, filepath.Join(pin.GOROOT, c.Path))
	}
	if pin.CCompiler != nil {
		for _, name := range []string{pin.CCompiler.CC, pin.CCompiler.CXX, pin.CCompiler.Linker} {
			resolved, e := filepath.EvalSymlinks(filepath.Join(pin.CCompiler.Root, name))
			if e != nil {
				return nil, e
			}
			executables = append(executables, resolved)
		}
	}
	for _, r := range executables {
		for _, path := range consumerPathForms(r) {
			fmt.Fprintf(&p, "(allow file-read* (literal %q))\n(allow process-exec* (literal %q))\n", path, path)
		}
	}
	p.WriteString("(allow file-read* (literal \"/dev/null\") (literal \"/dev/urandom\"))\n(allow file-write* (literal \"/dev/null\"))\n")
	for _, r := range writes {
		for _, path := range consumerPathForms(r) {
			fmt.Fprintf(&p, "(allow file-write* (subpath %q))\n", path)
		}
	}
	for id, r := range roots {
		if id == "stage" {
			continue
		}
		for _, path := range consumerPathForms(r) {
			fmt.Fprintf(&p, "(deny file-write* (subpath %q))\n", path)
		}
	}
	if image != "" {
		for _, path := range consumerPathForms(image) {
			fmt.Fprintf(&p, "(deny file-write* (subpath %q))\n", path)
		}
	}
	argv := append([]string{"-p", p.String(), pin.Executable}, args...)
	return exec.CommandContext(ctx, "/usr/bin/sandbox-exec", argv...), nil
}
