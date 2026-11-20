package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Template is a stored message body with {{name}} placeholders, filled in when a notification
// is created from it (D22). Templates never change once created.
type Template struct {
	Name      string
	Body      string
	CreatedAt time.Time
}

// Template limits.
const (
	MaxTemplateNameLength = 64
	MaxTemplateBodyLength = MaxEmailLength // the longest content any channel accepts
)

// ErrTemplateExists means a template with that name already exists.
var ErrTemplateExists = errors.New("template already exists")

var (
	// templateName allows names like order_shipped, otp-code or welcome.v2.
	templateName = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)
	// placeholder matches {{first_name}}, with optional spaces inside the braces.
	placeholder = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)
)

// Validate checks a new template's name and body and returns a *ValidationError if they are invalid.
func (t Template) Validate() error {
	var errs []FieldError
	add := func(field, msg string) { errs = append(errs, FieldError{Field: field, Message: msg}) }

	if !templateName.MatchString(t.Name) {
		add("name", fmt.Sprintf("must be 1 to %d characters: lowercase letters, digits, '_', '-' or '.', "+
			"starting with a letter or digit", MaxTemplateNameLength))
	}

	switch rest := placeholder.ReplaceAllString(t.Body, ""); {
	case strings.TrimSpace(t.Body) == "":
		add("body", "is required")
	case utf8.RuneCountInString(t.Body) > MaxTemplateBodyLength:
		add("body", fmt.Sprintf("must be at most %d characters", MaxTemplateBodyLength))
	case strings.Contains(rest, "{{") || strings.Contains(rest, "}}"): // braces left that aren't a placeholder
		add("body", "has a malformed placeholder; use {{name}} with letters, digits and '_'")
	}

	if len(errs) > 0 {
		return &ValidationError{Fields: errs}
	}
	return nil
}

// Variables returns the placeholder names in the body, each once, in order of first use.
func (t Template) Variables() []string {
	var names []string
	seen := map[string]bool{}
	for _, m := range placeholder.FindAllStringSubmatch(t.Body, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			names = append(names, m[1])
		}
	}
	return names
}

// Render fills in every placeholder from vars; extra variables are ignored. A missing one is a
// *ValidationError naming "variables.<name>". Values are inserted as they are: a value that
// looks like a placeholder is not filled in again.
func (t Template) Render(vars map[string]string) (string, error) {
	var errs []FieldError
	for _, name := range t.Variables() {
		if _, ok := vars[name]; !ok {
			errs = append(errs, FieldError{Field: "variables." + name, Message: "is required by template " + t.Name})
		}
	}
	if len(errs) > 0 {
		return "", &ValidationError{Fields: errs}
	}
	return placeholder.ReplaceAllStringFunc(t.Body, func(m string) string {
		return vars[placeholder.FindStringSubmatch(m)[1]]
	}), nil
}
