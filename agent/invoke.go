package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/stricttools/claudestream"
)

// Handler runs one of the agent's tools; it has the type of
// claudestream.Tool's Handler field.
type Handler = func(ctx context.Context, arguments json.RawMessage) (string, error)

// BuildConfig returns the session config for def, starting from base:
//
//   - SystemPrompt is def's prompt template resolved with vars (see
//     ResolvePrompt);
//   - SessionName is def's name;
//   - Tools are def's tool schemas, each with its handler from handlers,
//     keyed by tool name; a declared tool without a handler, or a handler
//     for a tool def does not declare, is refused;
//   - Model is def's model when def declares one;
//   - Sandbox, Budget, MCP, and Stream are def's when def has the table;
//   - every other field is base's.
//
// base must leave SystemPrompt, SessionName, and Tools empty: the definition
// sets them, and a value there would be lost. The result is not validated;
// claudestream.Start validates it.
func BuildConfig(def *Definition, base claudestream.Config, vars map[string]string, handlers map[string]Handler) (claudestream.Config, error) {
	if def == nil {
		return claudestream.Config{}, fmt.Errorf("%w: the definition is nil", ErrInvalidInvocation)
	}
	var errs []error
	if base.SystemPrompt != "" {
		errs = append(errs, fmt.Errorf("%w: the base config sets SystemPrompt; agent %s sets it from its prompt_template", ErrInvalidInvocation, def.Name))
	}
	if base.SessionName != "" {
		errs = append(errs, fmt.Errorf("%w: the base config sets SessionName; agent %s sets it to the agent name", ErrInvalidInvocation, def.Name))
	}
	if len(base.Tools) > 0 {
		errs = append(errs, fmt.Errorf("%w: the base config sets Tools; agent %s declares its tools and takes their handlers separately", ErrInvalidInvocation, def.Name))
	}

	prompt, err := ResolvePrompt(def.PromptTemplate, vars)
	if err != nil {
		errs = append(errs, fmt.Errorf("agent %s: %w", def.Name, err))
	}
	tools, err := buildTools(def, handlers)
	if err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return claudestream.Config{}, errors.Join(errs...)
	}

	cfg := base
	cfg.SystemPrompt = prompt
	cfg.SessionName = def.Name
	cfg.Tools = tools
	if def.Model != "" {
		cfg.Model = def.Model
	}
	if def.Sandbox != nil {
		cfg.Sandbox = def.Sandbox
	}
	if def.Budget != nil {
		cfg.Budget = def.Budget
	}
	if def.MCP != nil {
		cfg.MCP = def.MCP
	}
	if def.Stream != nil {
		cfg.Stream = def.Stream
	}
	return cfg, nil
}

// Invoke builds the session config for def (see BuildConfig) and starts the
// session.
func Invoke(ctx context.Context, def *Definition, base claudestream.Config, vars map[string]string, handlers map[string]Handler) (*claudestream.Session, error) {
	cfg, err := BuildConfig(def, base, vars, handlers)
	if err != nil {
		return nil, err
	}
	session, err := claudestream.Start(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("starting agent %s: %w", def.Name, err)
	}
	return session, nil
}

// buildTools pairs def's tool schemas with handlers, refusing declared tools
// without a handler and handlers without a declared tool.
func buildTools(def *Definition, handlers map[string]Handler) ([]claudestream.Tool, error) {
	var tools []claudestream.Tool
	var missing []string
	declared := map[string]bool{}
	for _, t := range def.Tools {
		declared[t.Name] = true
		h := handlers[t.Name]
		if h == nil {
			missing = append(missing, t.Name)
			continue
		}
		tools = append(tools, claudestream.Tool{
			Server:      t.Server,
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
			Handler:     h,
		})
	}
	var extra []string
	for _, name := range slices.Sorted(maps.Keys(handlers)) {
		if !declared[name] {
			extra = append(extra, name)
		}
	}
	var errs []error
	if len(missing) > 0 {
		errs = append(errs, fmt.Errorf("%w: agent %s declares tools without a handler: %s", ErrInvalidInvocation, def.Name, strings.Join(missing, ", ")))
	}
	if len(extra) > 0 {
		errs = append(errs, fmt.Errorf("%w: handlers given for tools agent %s does not declare: %s; its tools are: %s", ErrInvalidInvocation, def.Name, strings.Join(extra, ", "), describeNames(toolNames(def))))
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return tools, nil
}

func toolNames(def *Definition) []string {
	names := make([]string, len(def.Tools))
	for i, t := range def.Tools {
		names[i] = t.Name
	}
	return names
}
