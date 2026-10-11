package manifest

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Setting is a field of an agent's form (manifest [[settings]]). The owner
// or an admin fills the form in the Agent Center; the agent reads the
// values. It is how an agent learns what to do (an offer, targets…) and
// how its mode is switched with one button (a toggle).
type Setting struct {
	Key  string `toml:"key" json:"key"`
	Name string `toml:"label" json:"label"`
	// Type: text (one line), textarea, number, toggle, choice, list (of
	// short texts, one per line in the form).
	Type      string   `toml:"type" json:"type"`
	Help      string   `toml:"help" json:"help,omitempty"`
	Required  bool     `toml:"required" json:"required,omitempty"`
	Default   any      `toml:"default" json:"default,omitempty"`
	Choices   []string `toml:"choices" json:"choices,omitempty"`
	Min       *float64 `toml:"min" json:"min,omitempty"`
	Max       *float64 `toml:"max" json:"max,omitempty"`
	MaxLength int      `toml:"max_length" json:"max_length,omitempty"`
}

// Setting types.
const (
	SettingText     = "text"
	SettingTextarea = "textarea"
	SettingNumber   = "number"
	SettingToggle   = "toggle"
	SettingChoice   = "choice"
	SettingList     = "list"
)

const (
	maxSettings  = 30
	maxListItems = 200
)

// ErrSettings wraps every invalid value.
var ErrSettings = errors.New("invalid settings")

var settingKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// maxLength is the length limit of a text, textarea or list item.
func (f Setting) maxLength() int {
	if f.MaxLength > 0 {
		return f.MaxLength
	}
	switch f.Type {
	case SettingTextarea:
		return 4000
	case SettingList:
		return 100
	default:
		return 200
	}
}

func checkSettings(fields []Setting) error {
	if len(fields) > maxSettings {
		return fmt.Errorf("at most %d settings", maxSettings)
	}
	seen := map[string]bool{}
	for i := range fields {
		f := &fields[i]
		switch {
		case !settingKeyRe.MatchString(f.Key) || seen[f.Key]:
			return fmt.Errorf("settings: key %q must be unique, a-z, 0-9 and _", f.Key)
		case strings.TrimSpace(f.Name) == "" || utf8.RuneCountInString(f.Name) > 80:
			return fmt.Errorf("settings.%s: label must be 1 to 80 characters", f.Key)
		case utf8.RuneCountInString(f.Help) > 300:
			return fmt.Errorf("settings.%s: help is too long", f.Key)
		case f.MaxLength < 0 || f.MaxLength > 20000:
			return fmt.Errorf("settings.%s: max_length must be 1 to 20000", f.Key)
		}
		seen[f.Key] = true
		switch f.Type {
		case SettingText, SettingTextarea, SettingNumber, SettingToggle, SettingList:
			if len(f.Choices) > 0 {
				return fmt.Errorf("settings.%s: choices are for type choice", f.Key)
			}
		case SettingChoice:
			if len(f.Choices) == 0 || len(f.Choices) > 30 {
				return fmt.Errorf("settings.%s: 1 to 30 choices", f.Key)
			}
			for _, c := range f.Choices {
				if c == "" || utf8.RuneCountInString(c) > 80 {
					return fmt.Errorf("settings.%s: choices are 1 to 80 characters", f.Key)
				}
			}
		default:
			return fmt.Errorf("settings.%s: type must be text, textarea, number, toggle, choice or list", f.Key)
		}
		if f.Default != nil {
			v, err := f.Value(f.Default)
			if err != nil {
				return fmt.Errorf("settings.%s: default: %v", f.Key, err)
			}
			f.Default = v
		}
	}
	return nil
}

// Value checks and normalizes a value for the field.
func (f Setting) Value(v any) (any, error) {
	switch f.Type {
	case SettingText, SettingTextarea, SettingChoice:
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("a text is expected")
		}
		s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
		if f.Type != SettingTextarea && strings.ContainsAny(s, "\n\t") {
			return nil, errors.New("one line is expected")
		}
		if utf8.RuneCountInString(s) > f.maxLength() || hasControl(s) {
			return nil, fmt.Errorf("up to %d characters", f.maxLength())
		}
		if f.Type == SettingChoice && !slices.Contains(f.Choices, s) {
			return nil, fmt.Errorf("one of %v is expected", f.Choices)
		}
		return s, nil
	case SettingNumber:
		var n float64
		switch x := v.(type) {
		case float64:
			n = x
		case int64:
			n = float64(x)
		case int:
			n = float64(x)
		default:
			return nil, errors.New("a number is expected")
		}
		if math.IsNaN(n) || math.IsInf(n, 0) || (f.Min != nil && n < *f.Min) || (f.Max != nil && n > *f.Max) {
			return nil, errors.New("number out of range")
		}
		return n, nil
	case SettingToggle:
		b, ok := v.(bool)
		if !ok {
			return nil, errors.New("true or false is expected")
		}
		return b, nil
	case SettingList:
		raw, ok := v.([]any)
		if !ok {
			if ss, ok2 := v.([]string); ok2 {
				for _, s := range ss {
					raw = append(raw, s)
				}
			} else {
				return nil, errors.New("a list of texts is expected")
			}
		}
		if len(raw) > maxListItems {
			return nil, fmt.Errorf("up to %d items", maxListItems)
		}
		items := []string{}
		for _, it := range raw {
			s, ok := it.(string)
			if !ok {
				return nil, errors.New("a list of texts is expected")
			}
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			if utf8.RuneCountInString(s) > f.maxLength() || strings.ContainsAny(s, "\n\t") || hasControl(s) {
				return nil, fmt.Errorf("items: one line, up to %d characters", f.maxLength())
			}
			items = append(items, s)
		}
		return items, nil
	}
	return nil, errors.New("unknown type")
}

func hasControl(s string) bool {
	for _, r := range s {
		if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f {
			return true
		}
	}
	return false
}

// CheckValues validates values against the fields (unknown keys are
// refused) and returns them normalized.
func CheckValues(fields []Setting, values map[string]any) (map[string]any, error) {
	out := map[string]any{}
	byKey := map[string]Setting{}
	for _, f := range fields {
		byKey[f.Key] = f
	}
	for k, v := range values {
		f, ok := byKey[k]
		if !ok {
			return nil, fmt.Errorf("%w: unknown setting %q", ErrSettings, k)
		}
		if v == nil {
			continue
		}
		nv, err := f.Value(v)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrSettings, f.Name, err)
		}
		if s, ok := nv.(string); ok && s == "" {
			continue
		}
		out[k] = nv
	}
	return out, nil
}

// WithDefaults returns the values an agent sees: the saved ones, else the
// defaults.
func WithDefaults(fields []Setting, values map[string]any) map[string]any {
	out := map[string]any{}
	for _, f := range fields {
		if v, ok := values[f.Key]; ok {
			out[f.Key] = v
		} else if f.Default != nil {
			// Normalized again: a manifest read back from JSON has []any
			// lists and float64 numbers.
			if v, err := f.Value(f.Default); err == nil {
				out[f.Key] = v
			}
		}
	}
	return out
}

// MissingRequired lists the required fields without a value (nor a
// default).
func MissingRequired(fields []Setting, values map[string]any) []string {
	var out []string
	all := WithDefaults(fields, values)
	for _, f := range fields {
		if !f.Required {
			continue
		}
		v, ok := all[f.Key]
		if l, isList := v.([]string); !ok || (isList && len(l) == 0) {
			out = append(out, f.Key)
		}
	}
	return out
}
