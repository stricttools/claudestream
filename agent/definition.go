package agent

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/stricttools/claudestream"
)

// FormatVersion is the only format_version an agent file may declare.
const FormatVersion = 2

// FileSuffix ends every agent file name; the rest of the name is the agent's
// name.
const FileSuffix = ".agent.toml"

// legacySuffix ends the file names of the JSON format, which is refused.
const legacySuffix = ".agent.json"

// ErrInvalidDefinition is wrapped by every problem found in an agent file.
var ErrInvalidDefinition = errors.New("invalid agent definition")

// ErrNotFound is returned by LoadByName when no file exists for the name.
var ErrNotFound = errors.New("agent not found")

// Definition is a loaded agent definition.
type Definition struct {
	// Path is the file the definition was loaded from.
	Path string

	Name        string
	Version     string
	Description string
	// Model is empty when the definition declares none.
	Model          string
	PromptTemplate string
	Tools          []ToolSchema

	// Sandbox, Budget, MCP, and Stream are nil when the definition has no
	// such table; when set, each replaces the base config's value.
	Sandbox *claudestream.Sandbox
	Budget  *claudestream.Budget
	MCP     *claudestream.MCPOptions
	Stream  *claudestream.StreamOptions
}

// ToolSchema is a tool the agent declares; the caller supplies its handler.
type ToolSchema struct {
	Server      string
	Name        string
	Description string
	InputSchema map[string]any
}

// Dir returns the agents directory under cwd.
func Dir(cwd string) string {
	return filepath.Join(cwd, ".claudestream", "agents")
}

// document is the TOML file as decoded. Pointers tell an absent key from a
// zero value where the difference is checked.
type document struct {
	FormatVersion  *int64           `toml:"format_version"`
	Name           string           `toml:"name"`
	Version        string           `toml:"version"`
	Description    string           `toml:"description"`
	Model          *string          `toml:"model"`
	PromptTemplate string           `toml:"prompt_template"`
	Tools          []toolDocument   `toml:"tools"`
	Sandbox        *sandboxDocument `toml:"sandbox"`
	Budget         *budgetDocument  `toml:"budget"`
	MCP            *mcpDocument     `toml:"mcp"`
	Stream         *streamDocument  `toml:"stream"`
}

type toolDocument struct {
	Server      string         `toml:"server"`
	Name        string         `toml:"name"`
	Description string         `toml:"description"`
	InputSchema map[string]any `toml:"input_schema"`
}

type sandboxDocument struct {
	Tools           []string `toml:"tools"`
	WritePaths      []string `toml:"write_paths"`
	SkipPermissions *bool    `toml:"skip_permissions"`
}

type budgetDocument struct {
	CostThresholds  []float64 `toml:"cost_thresholds"`
	TurnThresholds  []int64   `toml:"turn_thresholds"`
	TokenThresholds []int64   `toml:"token_thresholds"`
}

type mcpDocument struct {
	ConfigFiles []string `toml:"config_files"`
	Strict      *bool    `toml:"strict"`
}

type streamDocument struct {
	Verbose                      *bool `toml:"verbose"`
	IncludePartialMessages       *bool `toml:"include_partial_messages"`
	IncludeHookEvents            *bool `toml:"include_hook_events"`
	ReplayUserMessages           *bool `toml:"replay_user_messages"`
	ExcludeDynamicPromptSections *bool `toml:"exclude_dynamic_prompt_sections"`
}

// versionProbe reads only format_version, so a file of another format
// version is refused for its version rather than for keys this version does
// not know.
type versionProbe struct {
	FormatVersion *int64 `toml:"format_version"`
}

// Load reads and checks the agent file at path, which must end in
// FileSuffix.
func Load(path string) (*Definition, error) {
	base := filepath.Base(path)
	if strings.HasSuffix(base, legacySuffix) {
		return nil, legacyError(path)
	}
	stem, ok := strings.CutSuffix(base, FileSuffix)
	if !ok || stem == "" {
		return nil, fmt.Errorf("%w: %s: an agent file is named <name>%s", ErrInvalidDefinition, path, FileSuffix)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading agent file: %w", err)
	}
	return decode(data, path, stem)
}

// LoadByName loads the agent file for name from the agents directory under
// cwd. A missing file is ErrNotFound, naming the agents the directory holds.
func LoadByName(cwd, name string) (*Definition, error) {
	if cwd == "" {
		return nil, errors.New("agent.LoadByName: cwd is required")
	}
	if err := checkName(name); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	path := filepath.Join(Dir(cwd), name+FileSuffix)
	if _, err := os.Stat(path); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("reading agent file: %w", err)
		}
		names, listErr := fileNames(Dir(cwd))
		if listErr != nil {
			return nil, fmt.Errorf("%w: no file %s; listing %s: %w", ErrNotFound, path, Dir(cwd), listErr)
		}
		if len(names) == 0 {
			return nil, fmt.Errorf("%w: no file %s, and %s holds no agents", ErrNotFound, path, Dir(cwd))
		}
		return nil, fmt.Errorf("%w: no file %s; the agents there are: %s", ErrNotFound, path, strings.Join(names, ", "))
	}
	return Load(path)
}

// List loads every agent file in the agents directory under cwd, sorted by
// name. A missing directory holds no agents. Any file that fails to load,
// including a file in the JSON format, makes List fail, reporting every such
// file. Entries not ending in FileSuffix or the JSON suffix are not agent
// files and are skipped.
func List(cwd string) ([]*Definition, error) {
	if cwd == "" {
		return nil, errors.New("agent.List: cwd is required")
	}
	dir := Dir(cwd)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading agents directory: %w", err)
	}
	var defs []*Definition
	var errs []error
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, FileSuffix) && !strings.HasSuffix(n, legacySuffix) {
			continue
		}
		def, err := Load(filepath.Join(dir, n))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		defs = append(defs, def)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	slices.SortFunc(defs, func(a, b *Definition) int { return strings.Compare(a.Name, b.Name) })
	return defs, nil
}

// fileNames returns the agent names of the agent files in dir, from the
// file names alone; a missing directory has none.
func fileNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if stem, ok := strings.CutSuffix(e.Name(), FileSuffix); ok && stem != "" {
			names = append(names, stem)
		}
	}
	return names, nil
}

// checkName refuses names that cannot be a file stem in the agents
// directory.
func checkName(name string) error {
	switch {
	case name == "":
		return errors.New("the agent name is empty")
	case name == "." || name == "..":
		return fmt.Errorf("%q is not an agent name", name)
	case strings.ContainsAny(name, `/\`):
		return fmt.Errorf("agent name %q contains a path separator; pass a file path to Load instead", name)
	}
	return nil
}

func legacyError(path string) error {
	return fmt.Errorf("%w: %s is in the JSON agent format, which is no longer read; convert it to %s by hand as the agent package documentation describes (keys one to one, format_version = %d, delete bare and log_violations, rename to <name>%s)",
		ErrInvalidDefinition, path, FileSuffix, FormatVersion, FileSuffix)
}

// decode parses data, the contents of path, whose file stem is stem.
func decode(data []byte, path, stem string) (*Definition, error) {
	var probe versionProbe
	if err := toml.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("%w: %s: %s", ErrInvalidDefinition, path, describeTOMLError(err))
	}
	switch {
	case probe.FormatVersion == nil:
		return nil, fmt.Errorf("%w: %s: format_version is required; set format_version = %d", ErrInvalidDefinition, path, FormatVersion)
	case *probe.FormatVersion != FormatVersion:
		return nil, fmt.Errorf("%w: %s: format_version %d is not supported; the supported format_version is %d", ErrInvalidDefinition, path, *probe.FormatVersion, FormatVersion)
	}

	var doc document
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%w: %s: %s", ErrInvalidDefinition, path, describeTOMLError(err))
	}
	def, problems := doc.definition(stem)
	if len(problems) > 0 {
		errs := make([]error, len(problems))
		for i, p := range problems {
			errs[i] = fmt.Errorf("%w: %s: %s", ErrInvalidDefinition, path, p)
		}
		return nil, errors.Join(errs...)
	}
	def.Path = path
	return def, nil
}

// describeTOMLError renders a go-toml error with the keys and positions it
// carries.
func describeTOMLError(err error) string {
	var missing *toml.StrictMissingError
	if errors.As(err, &missing) {
		parts := make([]string, len(missing.Errors))
		for i := range missing.Errors {
			e := &missing.Errors[i]
			row, col := e.Position()
			parts[i] = fmt.Sprintf("unknown key %q at line %d, column %d", strings.Join(e.Key(), "."), row, col)
		}
		return strings.Join(parts, "; ")
	}
	var decodeErr *toml.DecodeError
	if errors.As(err, &decodeErr) {
		row, col := decodeErr.Position()
		return fmt.Sprintf("line %d, column %d: %v", row, col, decodeErr)
	}
	return err.Error()
}

// definition checks the decoded document and converts it, returning every
// problem found.
func (d document) definition(stem string) (*Definition, []string) {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	switch {
	case d.Name == "":
		add("name is required")
	case d.Name != stem:
		add("name %q differs from the file name; the file for this agent is %s%s", d.Name, d.Name, FileSuffix)
	}
	if d.Version == "" {
		add("version is required")
	}
	if d.PromptTemplate == "" {
		add("prompt_template is required")
	}
	if d.Model != nil && *d.Model == "" {
		add("model is empty; remove the key to use the base config's model")
	}

	def := &Definition{
		Name:           d.Name,
		Version:        d.Version,
		Description:    d.Description,
		PromptTemplate: d.PromptTemplate,
	}
	if d.Model != nil {
		def.Model = *d.Model
	}

	seenTools := map[string]bool{}
	for i, t := range d.Tools {
		if t.Server == "" {
			add("tools[%d].server is required", i)
		}
		if t.Name == "" {
			add("tools[%d].name is required", i)
		} else if seenTools[t.Name] {
			add("tools[%d].name %q is declared more than once; handlers are matched by tool name", i, t.Name)
		}
		seenTools[t.Name] = true
		if t.InputSchema == nil {
			add("tools[%d].input_schema is required", i)
		}
		def.Tools = append(def.Tools, ToolSchema{
			Server:      t.Server,
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}

	if s := d.Sandbox; s != nil {
		if s.SkipPermissions == nil {
			add("sandbox.skip_permissions is required")
		}
		sandbox := claudestream.Sandbox{
			Tools:           s.Tools,
			WritePaths:      s.WritePaths,
			SkipPermissions: s.SkipPermissions != nil && *s.SkipPermissions,
		}
		if err := sandbox.Validate(); err != nil {
			add("sandbox: %v", err)
		}
		def.Sandbox = &sandbox
	}

	if b := d.Budget; b != nil {
		budget := claudestream.Budget{
			CostThresholds:  b.CostThresholds,
			TurnThresholds:  b.TurnThresholds,
			TokenThresholds: b.TokenThresholds,
		}
		if err := budget.Validate(); err != nil {
			add("budget: %v", err)
		}
		def.Budget = &budget
	}

	if m := d.MCP; m != nil {
		if m.ConfigFiles == nil {
			add("mcp.config_files is required")
		}
		for i, f := range m.ConfigFiles {
			if f == "" {
				add("mcp.config_files[%d] is empty", i)
			}
		}
		if m.Strict == nil {
			add("mcp.strict is required")
		}
		def.MCP = &claudestream.MCPOptions{
			ConfigFiles: m.ConfigFiles,
			Strict:      m.Strict != nil && *m.Strict,
		}
	}

	if s := d.Stream; s != nil {
		value := func(key string, v *bool) bool {
			if v == nil {
				add("stream.%s is required", key)
				return false
			}
			return *v
		}
		def.Stream = &claudestream.StreamOptions{
			Verbose:                      value("verbose", s.Verbose),
			IncludePartialMessages:       value("include_partial_messages", s.IncludePartialMessages),
			IncludeHookEvents:            value("include_hook_events", s.IncludeHookEvents),
			ReplayUserMessages:           value("replay_user_messages", s.ReplayUserMessages),
			ExcludeDynamicPromptSections: value("exclude_dynamic_prompt_sections", s.ExcludeDynamicPromptSections),
		}
	}

	return def, problems
}
