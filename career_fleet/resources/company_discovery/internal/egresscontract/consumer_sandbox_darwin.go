//go:build darwin

package egresscontract

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Adapted from Outcome Engine/pkg/sandbox/isolation_darwin.go's Seatbelt
// path-form and deny-by-default mechanics. This implementation travels in the
// printed runtime; it neither imports that checkout nor creates a service.
func consumerCommand(ctx context.Context, p WorkspaceProcedure, stage string, writes []string) (*exec.Cmd, error) {
	const executable = "/usr/bin/sandbox-exec"
	if _, err := os.Stat(executable); err != nil {
		return nil, fmt.Errorf("consumer_isolation_unavailable:%w", err)
	}
	var profile strings.Builder
	profile.WriteString("(version 1)\n(deny default)\n(allow process-fork)\n(allow process-exec*)\n(allow sysctl-read)\n(allow file-read-metadata)\n")
	for _, name := range []string{"com.apple.system.notification_center", "com.apple.system.logger", "com.apple.logd", "com.apple.system.opendirectoryd", "com.apple.CoreServices.coreservicesd"} {
		fmt.Fprintf(&profile, "(allow mach-lookup (global-name %q))\n", name)
	}
	// Dynamic-loader/system reads are permitted. User data, package managers
	// and temporary trees require explicit grants below; networking stays denied.
	profile.WriteString("(allow file-read*)\n")
	for _, root := range []string{"/Library", "/opt", "/usr/local", "/private/etc", "/Users", "/Volumes", "/Network", "/private/var/folders", "/private/tmp", "/Home"} {
		for _, path := range consumerPathForms(root) {
			fmt.Fprintf(&profile, "(deny file-read* (subpath %q))\n", path)
		}
	}
	reads := append([]string{stage}, p.ReadOnlyPaths...)
	reads = append(reads, writes...)
	for _, root := range reads {
		for _, path := range consumerPathForms(root) {
			fmt.Fprintf(&profile, "(allow file-read* (subpath %q))\n", path)
		}
	}
	profile.WriteString("(allow file-write* (literal \"/dev/null\"))\n")
	for _, root := range append([]string{stage}, writes...) {
		for _, path := range consumerPathForms(root) {
			fmt.Fprintf(&profile, "(allow file-write* (subpath %q))\n", path)
		}
	}
	for _, root := range p.ReadOnlyPaths {
		for _, path := range consumerPathForms(root) {
			fmt.Fprintf(&profile, "(deny file-write* (subpath %q))\n", path)
		}
	}
	args := append([]string{"-p", profile.String(), p.Executable}, p.Arguments...)
	return exec.CommandContext(ctx, executable, args...), nil
}

func consumerPathForms(value string) []string {
	value = filepath.Clean(value)
	forms := []string{value}
	if real, err := filepath.EvalSymlinks(value); err == nil && real != value {
		forms = append(forms, real)
	}
	if strings.HasPrefix(value, "/var/") {
		forms = append(forms, "/private"+value)
	}
	if strings.HasPrefix(value, "/private/var/") {
		forms = append(forms, strings.TrimPrefix(value, "/private"))
	}
	if strings.HasPrefix(value, "/tmp/") {
		forms = append(forms, "/private"+value)
	}
	if strings.HasPrefix(value, "/etc/") || value == "/etc" {
		forms = append(forms, "/private"+value)
	}
	return forms
}
