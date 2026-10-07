package agent

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// ErrInvalidInvocation is wrapped by every problem with the variables,
// handlers, or base config an agent is invoked with.
var ErrInvalidInvocation = errors.New("invalid agent invocation")

// placeholder matches a {variable} placeholder: a name of letters, digits,
// and underscores in braces. There is no escape: a literal {word} in a
// template is a placeholder. {{word}} holds the placeholder {word}, so its
// value ends up in single braces.
var placeholder = regexp.MustCompile(`\{([\p{L}\p{N}_]+)\}`)

// Placeholders returns the distinct placeholder names in template, sorted.
func Placeholders(template string) []string {
	seen := map[string]bool{}
	for _, m := range placeholder.FindAllStringSubmatch(template, -1) {
		seen[m[1]] = true
	}
	return slices.Sorted(maps.Keys(seen))
}

// ResolvePrompt replaces every placeholder in template with its value from
// vars. A placeholder without a value, or a variable the template has no
// placeholder for, is refused. Values are inserted as they are: braces in a
// value are never treated as placeholders.
func ResolvePrompt(template string, vars map[string]string) (string, error) {
	names := Placeholders(template)
	var unresolved, unused []string
	for _, name := range names {
		if _, ok := vars[name]; !ok {
			unresolved = append(unresolved, name)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(vars)) {
		if !slices.Contains(names, key) {
			unused = append(unused, key)
		}
	}
	var errs []error
	if len(unresolved) > 0 {
		errs = append(errs, fmt.Errorf("%w: the template's placeholders %s have no value", ErrInvalidInvocation, strings.Join(unresolved, ", ")))
	}
	if len(unused) > 0 {
		errs = append(errs, fmt.Errorf("%w: the variables %s have no placeholder in the template; its placeholders are: %s", ErrInvalidInvocation, strings.Join(unused, ", "), describeNames(names)))
	}
	if len(errs) > 0 {
		return "", errors.Join(errs...)
	}
	return placeholder.ReplaceAllStringFunc(template, func(match string) string {
		return vars[match[1:len(match)-1]]
	}), nil
}

func describeNames(names []string) string {
	if len(names) == 0 {
		return "(none)"
	}
	return strings.Join(names, ", ")
}
