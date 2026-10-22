// Package electron owns the Electron applications an agent drives: it
// launches them in isolation (or attaches to running ones), follows the
// process tree, keeps their output, reconnects when a development loop
// restarts them, and guarantees that everything it started is stopped.
package electron

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"howett.net/plist"
)

// Target is a resolved launch target.
type Target struct {
	// Executable is the program started (the Electron binary).
	Executable string `json:"executable"`
	// Args precede the caller's arguments ("." for an Electron project).
	Args []string `json:"args,omitempty"`
	// Dir is the working directory (the project for an Electron project).
	Dir string `json:"dir,omitempty"`
	// Bundle is the .app bundle, when there is one.
	Bundle   string `json:"bundle,omitempty"`
	BundleID string `json:"bundle_id,omitempty"`
	Name     string `json:"name"`
	// Evidence explains how the target was recognised as Electron.
	Evidence string `json:"evidence"`
	// Packaged is true for a built application (a .app or a bare binary),
	// false for a project directory run through the electron binary.
	Packaged bool `json:"packaged"`
}

// UsageError is a caller mistake (bad path, wrong kind of target).
type UsageError struct{ Msg string }

func (e *UsageError) Error() string { return e.Msg }

// Resolve identifies what to start for a path: an Electron project directory
// (package.json + node_modules/electron), a .app bundle built with Electron,
// or an Electron executable (inside a bundle or bare).
func Resolve(path string) (*Target, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, &UsageError{fmt.Sprintf("target %s: %v", path, err)}
	}
	if st.IsDir() && !strings.HasSuffix(abs, ".app") {
		return resolveProject(abs)
	}
	if st.IsDir() {
		return resolveBundle(abs)
	}
	if b := enclosingBundle(abs); b != "" {
		t, err := resolveBundle(b)
		if err == nil {
			t.Executable = abs
		}
		return t, err
	}
	if st.Mode()&0o111 == 0 {
		return nil, &UsageError{abs + " is not executable"}
	}
	return &Target{Executable: abs, Name: filepath.Base(abs), Evidence: "bare executable (assumed Electron: it must accept --remote-debugging-port)", Packaged: true}, nil
}

func resolveProject(dir string) (*Target, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil, &UsageError{"a directory target must be an Electron project (package.json with node_modules/electron) or a .app bundle"}
	}
	var pkg struct {
		Name        string `json:"name"`
		ProductName string `json:"productName"`
		Main        string `json:"main"`
	}
	_ = json.Unmarshal(raw, &pkg)
	exe := electronBinary(dir)
	if exe == "" {
		return nil, &UsageError{"no Electron binary under node_modules/electron/dist: install the project's dependencies first (bun install)"}
	}
	name := pkg.ProductName
	if name == "" {
		name = pkg.Name
	}
	if name == "" {
		name = filepath.Base(dir)
	}
	if pkg.Main != "" {
		if _, err := os.Stat(filepath.Join(dir, pkg.Main)); err != nil {
			return nil, &UsageError{fmt.Sprintf("package.json main %q does not exist: build the project first (its dev or build script)", pkg.Main)}
		}
	}
	return &Target{Executable: exe, Args: []string{"."}, Dir: dir, Name: name, Evidence: "package.json with node_modules/electron"}, nil
}

func electronBinary(dir string) string {
	var p string
	switch runtime.GOOS {
	case "darwin":
		p = filepath.Join(dir, "node_modules", "electron", "dist", "Electron.app", "Contents", "MacOS", "Electron")
	case "windows":
		p = filepath.Join(dir, "node_modules", "electron", "dist", "electron.exe")
	default:
		p = filepath.Join(dir, "node_modules", "electron", "dist", "electron")
	}
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

func enclosingBundle(exe string) string {
	for d := filepath.Dir(exe); d != "/" && d != "."; d = filepath.Dir(d) {
		if strings.HasSuffix(d, ".app") {
			return d
		}
	}
	return ""
}

func resolveBundle(app string) (*Target, error) {
	f, err := os.Open(filepath.Join(app, "Contents", "Info.plist"))
	if err != nil {
		return nil, &UsageError{app + " has no Contents/Info.plist"}
	}
	defer f.Close()
	var info struct {
		Exe  string `plist:"CFBundleExecutable"`
		ID   string `plist:"CFBundleIdentifier"`
		Name string `plist:"CFBundleName"`
	}
	if err := plist.NewDecoder(f).Decode(&info); err != nil {
		return nil, fmt.Errorf("%s: Info.plist: %w", app, err)
	}
	if info.Exe == "" {
		return nil, &UsageError{app + " declares no CFBundleExecutable"}
	}
	t := &Target{Executable: filepath.Join(app, "Contents", "MacOS", info.Exe), Bundle: app, BundleID: info.ID, Name: info.Name, Packaged: true}
	if t.Name == "" {
		t.Name = strings.TrimSuffix(filepath.Base(app), ".app")
	}
	if _, err := os.Stat(filepath.Join(app, "Contents", "Frameworks", "Electron Framework.framework")); err != nil {
		return nil, &UsageError{app + " is not an Electron application (no Contents/Frameworks/Electron Framework.framework)"}
	}
	t.Evidence = "Contents/Frameworks/Electron Framework.framework"
	return t, nil
}
