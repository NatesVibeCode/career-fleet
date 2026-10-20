package egresscontract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The v1 driver uses Node setTimeout directly. Larger delays are clamped to
// 1 ms by Node, so v1 refuses them rather than changing the authored deadline.
// Evidence: https://nodejs.org/api/timers.html#settimeoutcallback-delay-args
// Supporting longer delays requires a newly pinned driver with chunked timers.
const browserTimerMaxMillis = (1 << 31) - 1

// BrowserRuntimePin identifies the exact caller-supplied execution dependency.
// DriverSHA256 and BundleSHA256 bind complete trees; NodeSHA256 binds the binary.
type BrowserRuntimePin struct {
	NodeSHA256        string `json:"node_sha256"`
	DriverSHA256      string `json:"driver_sha256"`
	BundleSHA256      string `json:"bundle_sha256"`
	PlaywrightVersion string `json:"playwright_version"`
	BrowserVersion    string `json:"browser_version"`
}
type BrowserPermission struct {
	// PublicRead grants anonymous public HTTPS only for READ. The broker still
	// refuses private addresses and every method except GET/HEAD. Otherwise
	// source origins remain explicitly closed. Zero quotas impose no local cap;
	// the Process owns cancellation and READ always closes its own context.
	PublicRead    bool              `json:"public_read,omitempty"`
	Runtime       BrowserRuntimePin `json:"runtime"`
	ProgramSHA256 string            `json:"program_sha256,omitempty"`
	ResourceTypes []string          `json:"resource_types"`
	MaxContexts   int               `json:"max_contexts"`
	LeaseMillis   int64             `json:"lease_ms"`
}

// Paths are deployment bindings, not a way to change versions or Node logic.
type BrowserBinding struct {
	NodeExecutable      string `json:"node_executable"`
	PlaywrightDirectory string `json:"playwright_directory"`
	BrowserDirectory    string `json:"browser_directory"`
	BrowserExecutable   string `json:"browser_executable"`
}

func browserHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && strings.ToLower(s) == s
}
func validateBrowserOperation(op Operation) error {
	methods := map[string]bool{"OPEN": true, "NAVIGATE": true, "SUBMIT": true, "ADVANCE": true, "CAPTURE": true, "CLOSE": true, "READ": true}
	if !validToken(op.ID) || !methods[op.Method] || op.Destination.Kind != DestinationBrowser || op.Destination.Path != "" || op.Browser == nil || len(op.Credentials) != 0 || len(op.QueryParameters) != 0 || len(op.Headers) != 0 {
		return fmt.Errorf("invalid local browser operation")
	}
	if op.Browser.PublicRead {
		if op.Method != "READ" || len(op.Destination.Origins) != 0 {
			return fmt.Errorf("public browser read requires READ without fixed origins")
		}
	} else if len(op.Destination.Origins) == 0 {
		return fmt.Errorf("browser requires explicit source origins")
	}
	if err := validateOrigins(op.Destination.Origins); err != nil {
		return err
	}
	if op.TimeoutMillis > browserTimerMaxMillis {
		return fmt.Errorf("browser v1 timeout exceeds Node timer representation; a newly pinned driver is required")
	}
	if op.MaxRequests < 0 || op.MaxRequestBytes < 0 || op.MaxResponseBytes < 0 || op.TimeoutMillis < 0 || op.MaxRedirects < 0 {
		return fmt.Errorf("invalid browser bounds")
	}
	if (op.MaxRedirects == 0 && op.RedirectPolicy != RedirectNone) || (op.MaxRedirects > 0 && op.RedirectPolicy != RedirectDeclaredOrigins) {
		return fmt.Errorf("browser redirects require explicitly declared origins")
	}
	p := op.Browser
	r := p.Runtime
	if p.LeaseMillis > browserTimerMaxMillis {
		return fmt.Errorf("browser v1 lease exceeds Node timer representation; a newly pinned driver is required")
	}
	if p.MaxContexts > 0 && uint64(p.MaxContexts) > (1<<53)-1 {
		return fmt.Errorf("browser context quota is not exactly representable by the v1 driver JSON number")
	}
	if !browserHex64(r.NodeSHA256) || !browserHex64(r.DriverSHA256) || !browserHex64(r.BundleSHA256) || !browserVersion(r.PlaywrightVersion) || !browserVersion(r.BrowserVersion) || p.MaxContexts < 0 || p.LeaseMillis < 0 {
		return fmt.Errorf("invalid browser runtime pins or lease")
	}
	capture := op.Method == "CAPTURE" || op.Method == "READ"
	if (capture && !browserHex64(p.ProgramSHA256)) || (!capture && p.ProgramSHA256 != "") {
		return fmt.Errorf("capture program pin required exactly for CAPTURE")
	}
	if len(p.ResourceTypes) > 6 {
		return fmt.Errorf("invalid browser resource bound")
	}
	seen := map[string]bool{}
	for _, v := range p.ResourceTypes {
		if seen[v] || !(v == "document" || v == "script" || v == "stylesheet" || v == "xhr" || v == "fetch" || v == "other") {
			return fmt.Errorf("invalid browser resource permission")
		}
		seen[v] = true
	}
	if (op.Method == "NAVIGATE" || op.Method == "SUBMIT" || op.Method == "ADVANCE" || op.Method == "READ") && !seen["document"] {
		return fmt.Errorf("browser navigation needs a document permission")
	}
	return nil
}
func browserVersion(s string) bool {
	if len(s) < 3 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r == '.' || r == '-' || r >= 'a' && r <= 'z') {
			return false
		}
	}
	return true
}
func browserAbsolute(s string) bool {
	return s != "" && filepath.IsAbs(s) && filepath.Clean(s) == s && !strings.ContainsAny(s, "\x00\r\n")
}
func validateBrowserBinding(policy Policy, b Binding) (Binding, error) {
	if b.Browser == nil || !browserAbsolute(b.Directory) || len(b.Origins) > 0 || len(b.Routes) > 0 || b.CredentialRef != "" || b.Operation != policy.Operations[0].ID {
		return Binding{}, fmt.Errorf("browser requires an exact runtime and owner-directory binding")
	}
	p := b.Browser
	for _, s := range []string{p.NodeExecutable, p.PlaywrightDirectory, p.BrowserDirectory, p.BrowserExecutable} {
		if !browserAbsolute(s) {
			return Binding{}, fmt.Errorf("browser runtime path is invalid")
		}
	}
	rel, e := filepath.Rel(p.BrowserDirectory, p.BrowserExecutable)
	if e != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return Binding{}, fmt.Errorf("browser executable is outside its pinned bundle")
	}
	return b, nil
}

// BrowserTreeDigest binds every regular file and relative name under one tree.
// It refuses symlinks, special files, and concurrent file changes.
// This exported helper is also used by the build-time dependency inventory.
func BrowserTreeDigest(root string) (string, error) {
	if !browserAbsolute(root) {
		return "", fmt.Errorf("browser tree path invalid")
	}
	info, e := os.Lstat(root)
	if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("browser tree unavailable")
	}
	type entry struct {
		Path   string `json:"path"`
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
	}
	list := []entry{}
	err := filepath.WalkDir(root, func(path string, de os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if de.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("browser tree contains a symlink")
		}
		if de.IsDir() {
			return nil
		}
		i, e := de.Info()
		if e != nil || !i.Mode().IsRegular() {
			return fmt.Errorf("browser tree file invalid")
		}
		sum, e := browserFileDigest(path)
		if e != nil {
			return e
		}
		relative, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		list = append(list, entry{filepath.ToSlash(relative), i.Size(), sum})
		return nil
	})
	if err != nil || len(list) == 0 {
		return "", fmt.Errorf("browser tree could not be pinned")
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Path < list[j].Path })
	raw, _ := json.Marshal(list)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func browserFileDigest(path string) (string, error) {
	i, e := os.Lstat(path)
	if e != nil || !i.Mode().IsRegular() || i.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("browser file unavailable")
	}
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, f)
	after, statErr := f.Stat()
	if e != nil || statErr != nil || n != i.Size() || after.Size() != i.Size() || !after.ModTime().Equal(i.ModTime()) {
		return "", fmt.Errorf("browser file changed during pinning")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
