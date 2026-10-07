package claudestream

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Sandbox answers permission requests on the consumer's behalf: a tool must
// be on the allow-list, and a file-writing tool must write inside one of the
// writable paths. A session with a Sandbox never yields PermissionRequest;
// it yields PermissionDecided instead.
type Sandbox struct {
	// Tools is the tool allow-list, passed as --allowedTools; nil allows
	// every tool.
	Tools []string
	// WritePaths are the files and directories Write, Edit, and MultiEdit
	// may write inside, relative paths resolved against the session's
	// working directory; nil leaves writes unrestricted.
	WritePaths []string
	// SkipPermissions passes --dangerously-skip-permissions.
	SkipPermissions bool
}

// SandboxDecision is a Sandbox's answer to one permission request.
type SandboxDecision struct {
	Allowed bool
	// Message is the reason sent to the model on a denial; empty when
	// allowed.
	Message string
}

// writeTools are the tools whose file_path is checked against WritePaths.
var writeTools = []string{"Write", "Edit", "MultiEdit"}

// Validate refuses empty tool names and empty write paths.
func (s Sandbox) Validate() error {
	var errs []error
	for i, t := range s.Tools {
		if t == "" {
			errs = append(errs, fmt.Errorf("Tools[%d] is empty", i))
		}
	}
	for i, p := range s.WritePaths {
		if p == "" {
			errs = append(errs, fmt.Errorf("WritePaths[%d] is empty", i))
		}
	}
	return errors.Join(errs...)
}

// restricts reports whether the sandbox limits tools or write paths, which
// is what makes the session intercept permission prompts.
func (s Sandbox) restricts() bool {
	return s.Tools != nil || s.WritePaths != nil
}

// Decide answers a permission request for toolName with input. Relative
// paths are resolved against cwd (empty means this process's working
// directory). The tool allow-list is checked first; then, for Write, Edit,
// and MultiEdit, input's file_path must resolve (symlinks followed, as far as
// the path exists) to one of WritePaths or a path inside one of them.
func (s Sandbox) Decide(toolName string, input map[string]any, cwd string) SandboxDecision {
	if s.Tools != nil && !slices.Contains(s.Tools, toolName) {
		return deny("Tool '%s' not in sandbox allow-list", toolName)
	}
	if s.WritePaths == nil || !slices.Contains(writeTools, toolName) {
		return SandboxDecision{Allowed: true}
	}

	rawPath, present := input["file_path"]
	filePath, isString := rawPath.(string)
	switch {
	case present && !isString:
		return deny("file_path of write tool %s is not a string while write_paths is set", toolName)
	case filePath == "":
		return deny("Empty file_path in write tool when write_paths is set")
	}

	target, err := realPath(filePath, cwd)
	if err != nil {
		return deny("Path '%s' cannot be resolved: %v", filePath, err)
	}
	for _, allowedPath := range s.WritePaths {
		allowed, err := realPath(allowedPath, cwd)
		if err != nil {
			return deny("Sandbox write path '%s' cannot be resolved: %v", allowedPath, err)
		}
		if isWithin(target, allowed) {
			return SandboxDecision{Allowed: true}
		}
	}
	return deny("Path '%s' outside allowed write scope", filePath)
}

func deny(format string, args ...any) SandboxDecision {
	return SandboxDecision{Message: fmt.Sprintf(format, args...)}
}

// realPath makes path absolute against cwd and resolves symlinks in the
// longest prefix of it that exists; the part that does not exist yet is
// appended unchanged (after cleaning).
func realPath(path, cwd string) (string, error) {
	if !filepath.IsAbs(path) {
		base := cwd
		if base == "" {
			var err error
			if base, err = os.Getwd(); err != nil {
				return "", err
			}
		}
		path = filepath.Join(base, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}

	existing := abs
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(existing)
		if err == nil {
			return filepath.Join(append([]string{resolved}, missing...)...), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", err
		}
		missing = append([]string{filepath.Base(existing)}, missing...)
		existing = parent
	}
}

// isWithin reports whether target is allowed itself or a path inside it.
// Both must be resolved; the separator check keeps /src/foo from matching
// /src/foobar.
func isWithin(target, allowed string) bool {
	if target == allowed {
		return true
	}
	if !strings.HasSuffix(allowed, string(filepath.Separator)) {
		allowed += string(filepath.Separator)
	}
	return strings.HasPrefix(target, allowed)
}
