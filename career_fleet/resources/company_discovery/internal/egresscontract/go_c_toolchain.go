package egresscontract

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// The caller selects resources, while the printed Process retains command
// profiles and build behavior. The manifest pins the complete selected native
// toolchain closure; only its copied image can execute.
type GoCCompilerPin struct {
	Root              string   `json:"root"`
	Manifest          string   `json:"manifest"`
	ManifestDigest    string   `json:"manifest_digest"`
	CC                string   `json:"cc"`
	CXX               string   `json:"cxx"`
	Linker            string   `json:"linker"`
	SDK               string   `json:"sdk"`
	ResourceDirectory string   `json:"resource_directory"`
	QualifiedTargets  []string `json:"qualified_targets"`
}
type GoCCompilerFile struct {
	Path   string `json:"path"`
	Digest string `json:"digest,omitempty"`
	Mode   uint32 `json:"mode,omitempty"`
	Target string `json:"target,omitempty"`
}
type GoCCompilerManifest struct {
	Schema string            `json:"schema"`
	Files  []GoCCompilerFile `json:"files"`
}

func cloneGoCCompilerPin(p *GoCCompilerPin) *GoCCompilerPin {
	if p == nil {
		return nil
	}
	copy := *p
	copy.QualifiedTargets = append([]string{}, p.QualifiedTargets...)
	return &copy
}

func readGoCCompilerManifest(p *GoCCompilerPin) (GoCCompilerManifest, error) {
	var m GoCCompilerManifest
	if p == nil || !absoluteGoRoot(p.Root) || !absoluteGoRoot(p.Manifest) || !validHex(p.ManifestDigest) || len(p.QualifiedTargets) == 0 {
		return m, fmt.Errorf("C compiler resource pin absent")
	}
	for _, name := range []string{p.CC, p.CXX, p.Linker, p.SDK, p.ResourceDirectory} {
		if !cleanGoPath(name) || name == "." {
			return m, fmt.Errorf("C compiler path is invalid")
		}
	}
	root, e := filepath.EvalSymlinks(p.Root)
	if e != nil || root != p.Root {
		return m, fmt.Errorf("C compiler root is not canonical")
	}
	raw, e := os.ReadFile(p.Manifest)
	if e != nil {
		return m, e
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != p.ManifestDigest {
		return m, fmt.Errorf("C compiler manifest pin differs")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	e = d.Decode(&m)
	if e != nil || d.Decode(new(any)) != io.EOF || m.Schema != "praxis.c_toolchain_files.v1" || len(m.Files) == 0 {
		return m, fmt.Errorf("C compiler manifest is invalid")
	}
	seen := map[string]bool{}
	for _, f := range m.Files {
		if !cleanGoPath(f.Path) || f.Path == "." || seen[f.Path] {
			return m, fmt.Errorf("C compiler manifest path is invalid")
		}
		seen[f.Path] = true
		if f.Target != "" {
			if filepath.IsAbs(f.Target) || f.Digest != "" || f.Mode != 0 {
				return m, fmt.Errorf("C compiler link is invalid")
			}
			resolved := filepath.Clean(filepath.Join(filepath.Dir(f.Path), f.Target))
			if !cleanGoPath(resolved) {
				return m, fmt.Errorf("C compiler link escapes its image")
			}
		} else if !validHex(f.Digest) || f.Mode&^uint32(0555) != 0 || f.Mode&0400 == 0 {
			return m, fmt.Errorf("C compiler file pin is invalid")
		}
	}
	for _, name := range []string{p.CC, p.CXX, p.Linker} {
		if !seen[name] {
			return m, fmt.Errorf("C compiler executable is not pinned")
		}
	}
	return m, nil
}

func prepareGoCCompilerImage(ctx context.Context, p *GoCCompilerPin, scratch string) (*GoCCompilerPin, error) {
	m, e := readGoCCompilerManifest(p)
	if e != nil {
		return nil, e
	}
	dir := filepath.Join(scratch, ".c-runtime-"+p.ManifestDigest)
	verify := func(root string) error {
		r, e := os.OpenRoot(root)
		if e != nil {
			return e
		}
		defer r.Close()
		expected := map[string]GoCCompilerFile{}
		for _, f := range m.Files {
			expected[f.Path] = f
		}
		seen := map[string]bool{}
		e = fs.WalkDir(r.FS(), ".", func(name string, entry fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if entry.IsDir() {
				return nil
			}
			f, ok := expected[name]
			if !ok {
				return fmt.Errorf("C compiler image contains an unpinned file: %s", name)
			}
			info, e := r.Lstat(name)
			if e != nil {
				return e
			}
			if f.Target != "" {
				target, e := r.Readlink(name)
				if e != nil || target != f.Target {
					return fmt.Errorf("C compiler image link differs: %s", name)
				}
			} else {
				if !info.Mode().IsRegular() || uint32(info.Mode().Perm()) != f.Mode {
					return fmt.Errorf("C compiler image file mode differs: %s", name)
				}
				stream, e := r.Open(name)
				if e != nil {
					return e
				}
				h := sha256.New()
				_, e = copyGoCCompilerBytes(ctx, h, stream)
				stream.Close()
				if e != nil {
					return e
				}
				if hex.EncodeToString(h.Sum(nil)) != f.Digest {
					return fmt.Errorf("C compiler image bytes differ: %s", name)
				}
			}
			seen[name] = true
			return nil
		})
		if e != nil {
			return e
		}
		if len(seen) != len(expected) {
			return fmt.Errorf("C compiler image is incomplete")
		}
		for _, name := range []string{p.CC, p.CXX, p.Linker} {
			actual, e := filepath.EvalSymlinks(filepath.Join(root, name))
			if e != nil || !goWithin(root, actual) {
				return fmt.Errorf("C compiler executable escapes image")
			}
			st, e := os.Stat(actual)
			if e != nil || st.Mode()&0111 == 0 {
				return fmt.Errorf("C compiler executable is not executable")
			}
		}
		for _, name := range []string{p.SDK, p.ResourceDirectory} {
			actual, e := filepath.EvalSymlinks(filepath.Join(root, name))
			if e != nil || !goWithin(root, actual) {
				return fmt.Errorf("C compiler data escapes image")
			}
			st, e := os.Stat(actual)
			if e != nil || !st.IsDir() {
				return fmt.Errorf("C compiler data absent")
			}
		}
		return nil
	}
	if _, e = os.Lstat(dir); os.IsNotExist(e) {
		pending, e := os.MkdirTemp(scratch, ".c-runtime-pending-")
		if e != nil {
			return nil, e
		}
		defer os.RemoveAll(pending)
		source, e := os.OpenRoot(p.Root)
		if e != nil {
			return nil, e
		}
		defer source.Close()
		for _, f := range m.Files {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			to := filepath.Join(pending, f.Path)
			if e = os.MkdirAll(filepath.Dir(to), 0700); e != nil {
				return nil, e
			}
			info, e := source.Lstat(f.Path)
			if e != nil {
				return nil, e
			}
			if f.Target != "" {
				target, e := source.Readlink(f.Path)
				if e != nil || target != f.Target {
					return nil, fmt.Errorf("selected C compiler link changed")
				}
				if e = os.Symlink(target, to); e != nil {
					return nil, e
				}
				continue
			}
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("selected C compiler file is not regular")
			}
			in, e := source.Open(f.Path)
			if e != nil {
				return nil, e
			}
			out, e := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if e != nil {
				in.Close()
				return nil, e
			}
			h := sha256.New()
			_, e = copyGoCCompilerBytes(ctx, io.MultiWriter(out, h), in)
			in.Close()
			closeErr := out.Close()
			if e != nil {
				return nil, e
			}
			if closeErr != nil {
				return nil, closeErr
			}
			if hex.EncodeToString(h.Sum(nil)) != f.Digest {
				return nil, fmt.Errorf("selected C compiler bytes changed: %s", f.Path)
			}
			if e = os.Chmod(to, os.FileMode(f.Mode)); e != nil {
				return nil, e
			}
		}
		if e = verify(pending); e != nil {
			return nil, e
		}
		if e = os.Rename(pending, dir); e != nil {
			if v := verify(dir); v != nil {
				return nil, e
			}
		}
	} else if e != nil {
		return nil, e
	}
	canonical, e := filepath.EvalSymlinks(dir)
	if e != nil || canonical != dir {
		return nil, fmt.Errorf("C compiler image root is not canonical")
	}
	if e = verify(dir); e != nil {
		return nil, e
	}
	out := *p
	out.Root = dir
	return &out, nil
}

// Go's CC/CXX parser quotes whole arguments and performs no backslash
// unescaping. Shell concatenation and strconv.Quote are not its grammar.
func goCCompilerCommand(p *GoCCompilerPin, name string) (string, error) {
	args := []string{filepath.Join(p.Root, name), "-B" + filepath.Join(p.Root, filepath.Dir(p.Linker)), "-isysroot", filepath.Join(p.Root, p.SDK), "-resource-dir", filepath.Join(p.Root, p.ResourceDirectory)}
	for i, arg := range args {
		// Quotes inside an unquoted word are literal in Go's parser. Keep
		// representable paths containing both quote kinds when no whitespace
		// requires a quoted word.
		if !strings.ContainsAny(arg, " \t\r\n") && arg != "" && arg[0] != '\'' && arg[0] != '"' {
			continue
		}
		if !strings.ContainsRune(arg, '\'') {
			args[i] = "'" + arg + "'"
		} else if !strings.ContainsRune(arg, '"') {
			args[i] = "\"" + arg + "\""
		} else {
			return "", fmt.Errorf("C compiler argument cannot be represented by Go's CC/CXX quote grammar: %q", arg)
		}
	}
	return strings.Join(args, " "), nil
}

type goCCompilerContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r goCCompilerContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func copyGoCCompilerBytes(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	n, err := io.Copy(dst, goCCompilerContextReader{ctx: ctx, reader: src})
	if err != nil {
		return n, err
	}
	return n, ctx.Err()
}
