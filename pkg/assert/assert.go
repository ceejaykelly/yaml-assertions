// Package assert provides the core assertion evaluation logic for ya.
// It supports path-based lookups into YAML documents and comparisons using
// a set of operators (==, !=, contains, exists).
package assert

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"strconv"
	"strings"

	"sigs.k8s.io/yaml"
)

// Operator is the type for assertion comparison operators.
type Operator string

const (
	Equal    Operator = "=="
	NotEqual Operator = "!="
	Contains Operator = "contains"
	All      Operator = "all"
	Exists   Operator = "exists"
	// Script runs a shell command and treats a zero exit code as pass.
	// The queried value is provided to the command as the VALUE environment variable.
	Script Operator = "script"

	// Future operators to implement:
	// GreaterThan        Operator = ">"
	// LessThan           Operator = "<"
	// GreaterThanOrEqual Operator = ">="
	// LessThanOrEqual    Operator = "<="
)

// AssertSpec describes a single assertion to run against a rendered YAML document.
// Kind and Name are used to select the target Kubernetes resource from a multi-doc
// YAML stream. Path, Op, and Value define what is asserted.
type AssertSpec struct {
	// Kind filters documents by their "kind" field (e.g. "Deployment").
	// Leave empty to match any kind.
	Kind string `yaml:"kind,omitempty" json:"kind,omitempty"`

	// Name filters documents by metadata.name. Leave empty to match any name.
	Name string `yaml:"name,omitempty" json:"name,omitempty"`

	// Path is a dot-and-bracket-notation path into the YAML document.
	// Supports numeric indexing and field-value filter expressions:
	//
	//   "spec.template.spec.containers[0].image"         — numeric index
	//   "spec.template.spec.containers[name=app].image"  — filter by field value
	Path string `yaml:"path" json:"path"`

	// Op is the comparison operator (==, !=, contains, exists).
	// NOTE: sigs.k8s.io/yaml unmarshals via JSON, so the json tag must match
	// the YAML key "operator" — yaml tags alone are not sufficient.
	Op Operator `yaml:"operator" json:"operator"`

	// Value is the expected value for the assertion. Ignored when Op is "exists".
	// For Op "script", Value must be a shell command string.
	Value any `yaml:"expected,omitempty" json:"expected,omitempty"`
}

// segmentType describes the kind of a single parsed path component.
type segmentType uint8

const (
	segmentKey    segmentType = iota // "foo" — map key
	segmentIndex                     // "[0]" — numeric array index
	segmentFilter                    // "[name=app]" — filter array element by field value
)

// pathSegment is a single component of a parsed YAML path expression.
type pathSegment struct {
	typ         segmentType
	key         string // segmentKey: the map key name
	index       int    // segmentIndex: the array index
	filterKey   string // segmentFilter: field name to match
	filterValue string // segmentFilter: expected field value
}

// normalizeNumber converts common numeric types to float64 to allow
// consistent comparison regardless of whether the value came from YAML
// unmarshalling (always float64) or user-supplied code (may be int/uint/…).
func normalizeNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	case uint:
		return float64(n), true
	case uint64:
		return float64(n), true
	default:
		return 0, false
	}
}

// parsePath splits a path expression into typed segments. Supports:
//
//	"foo.bar"             → [key:foo, key:bar]
//	"foo[0].bar"          → [key:foo, index:0, key:bar]
//	"foo[name=bar].baz"   → [key:foo, filter:{name=bar}, key:baz]
func parsePath(path string) []pathSegment {
	var result []pathSegment
	i := 0
	for i < len(path) {
		if path[i] == '.' {
			i++
			continue
		}
		if path[i] == '[' {
			// Bracket segment: extract content between [ and ]
			j := i + 1
			for j < len(path) && path[j] != ']' {
				j++
			}
			if j < len(path) && path[j] == ']' {
				content := path[i+1 : j]
				if eqIdx := strings.Index(content, "="); eqIdx >= 0 {
					// Filter segment: [key=value]
					result = append(result, pathSegment{
						typ:         segmentFilter,
						filterKey:   content[:eqIdx],
						filterValue: content[eqIdx+1:],
					})
				} else if idx, err := strconv.Atoi(content); err == nil {
					// Numeric index segment: [0]
					result = append(result, pathSegment{typ: segmentIndex, index: idx})
				} else {
					// Unrecognised bracket content — treat as a map key
					result = append(result, pathSegment{typ: segmentKey, key: content})
				}
				i = j + 1
				continue
			}
		}
		// Normal map key: read until the next '.' or '['
		j := i
		for j < len(path) && path[j] != '.' && path[j] != '[' {
			j++
		}
		if j > i {
			result = append(result, pathSegment{typ: segmentKey, key: path[i:j]})
		}
		i = j
	}
	return result
}

// resolvePath walks segments over data and returns (value, found, error).
// found=false with nil error means the path simply does not exist.
// A non-nil error indicates an irrecoverable problem (e.g. out-of-range index).
func resolvePath(data map[string]interface{}, segments []pathSegment) (any, bool, error) {
	var current any = data
	for _, seg := range segments {
		switch seg.typ {
		case segmentKey:
			m, ok := current.(map[string]interface{})
			if !ok {
				// Type mismatch during traversal — treat as not found
				return nil, false, nil
			}
			val, exists := m[seg.key]
			if !exists {
				return nil, false, nil
			}
			current = val

		case segmentIndex:
			arr, ok := current.([]interface{})
			if !ok {
				return nil, false, nil
			}
			if seg.index < 0 || seg.index >= len(arr) {
				return nil, false, fmt.Errorf("array index %d out of range (length %d)", seg.index, len(arr))
			}
			current = arr[seg.index]

		case segmentFilter:
			arr, ok := current.([]interface{})
			if !ok {
				return nil, false, nil
			}
			matched := false
			for _, item := range arr {
				itemMap, ok := item.(map[string]interface{})
				if !ok {
					continue
				}
				val, exists := itemMap[seg.filterKey]
				if !exists {
					continue
				}
				// Compare as string to handle YAML numeric values transparently
				if fmt.Sprintf("%v", val) == seg.filterValue {
					current = item
					matched = true
					break
				}
			}
			if !matched {
				return nil, false, nil
			}
		}
	}
	return current, true, nil
}

// Evaluate walks the given YAML byte slice along objectPath and compares the
// found value using the specified operator. It returns (true, nil) when the
// assertion passes, (false, nil) when it fails cleanly, and (false, err) when
// the path or values cannot be resolved.
func Evaluate(values []byte, objectPath string, operator string, expected any) (bool, error) {
	var data map[string]interface{}
	if err := yaml.Unmarshal(values, &data); err != nil {
		return false, fmt.Errorf("failed to unmarshal values: %w", err)
	}

	segments := parsePath(objectPath)
	current, found, err := resolvePath(data, segments)
	if err != nil {
		return false, err
	}
	if !found {
		if operator == string(Exists) {
			return false, nil
		}
		return false, fmt.Errorf("path not found: %s", objectPath)
	}
	if operator == string(Exists) {
		return true, nil
	}

	// Normalize numbers for comparison
	if a, aok := normalizeNumber(current); aok {
		if b, bok := normalizeNumber(expected); bok {
			current = a
			expected = b
		}
	}

	// Compare using the operator
	switch Operator(operator) {
	case Equal:
		return reflect.DeepEqual(current, expected), nil
	case NotEqual:
		return !reflect.DeepEqual(current, expected), nil
	case Exists:
		return true, nil
	case Contains:
		// Support string contains and array contains
		switch v := current.(type) {
		case string:
			substr, ok := expected.(string)
			if !ok {
				return false, fmt.Errorf("expected value for contains must be a string when target is string")
			}
			return strings.Contains(v, substr), nil
		case []interface{}:
			normalizedExpected, err := normalizeViaYAML(expected)
			if err != nil {
				return false, fmt.Errorf("failed to normalize expected value: %w", err)
			}
			if expectedMap, ok := normalizedExpected.(map[string]interface{}); ok {
				for _, item := range v {
					if itemMap, ok := item.(map[string]interface{}); ok {
						if isSubset(expectedMap, itemMap) {
							return true, nil
						}
					}
				}
				return false, nil
			}
			for _, item := range v {
				if reflect.DeepEqual(item, normalizedExpected) {
					return true, nil
				}
			}
			return false, nil
		default:
			return false, fmt.Errorf("contains operator not supported for type %T", current)
		}
	case All:
		// For arrays: require every element to match the expected value/subset
		switch v := current.(type) {
		case []interface{}:
			normalizedExpected, err := normalizeViaYAML(expected)
			if err != nil {
				return false, fmt.Errorf("failed to normalize expected value: %w", err)
			}
			if expectedMap, ok := normalizedExpected.(map[string]interface{}); ok {
				for _, item := range v {
					itemMap, ok := item.(map[string]interface{})
					if !ok || !isSubset(expectedMap, itemMap) {
						return false, nil
					}
				}
				return true, nil
			}
			// For non-map expected values, require exact equality for all
			for _, item := range v {
				if !reflect.DeepEqual(item, normalizedExpected) {
					return false, nil
				}
			}
			return true, nil
		default:
			return false, fmt.Errorf("all operator not supported for type %T", current)
		}
	case Script:
		scriptStr, ok := expected.(string)
		if !ok {
			return false, fmt.Errorf("script operator requires expected to be a shell command string")
		}
		return runScript(scriptStr, current)
	default:
		return false, fmt.Errorf("unsupported operator: %s", operator)
	}
}

// EvaluateFunc resolves objectPath within values and passes the found value to
// fn for custom validation. fn receives nil when the path does not exist.
// This is the idiomatic way to run arbitrary Go logic against a queried value
// in programmatic tests.
//
// Example:
//
//	pass, err := assert.EvaluateFunc(docBytes, "spec.replicas", func(v any) (bool, error) {
//	    n, ok := v.(float64)
//	    return ok && n >= 2, nil
//	})
func EvaluateFunc(values []byte, objectPath string, fn func(value any) (bool, error)) (bool, error) {
	var data map[string]interface{}
	if err := yaml.Unmarshal(values, &data); err != nil {
		return false, fmt.Errorf("failed to unmarshal values: %w", err)
	}

	segments := parsePath(objectPath)
	value, _, err := resolvePath(data, segments)
	if err != nil {
		return false, err
	}
	return fn(value)
}

// runScript runs script as a shell command with value exposed as the VALUE
// environment variable. Exit code 0 → pass (true, nil); non-zero → fail
// (false, nil); execution failure → (false, err).
//
// On Windows cmd /C is used; on all other platforms sh -c is used.
func runScript(script string, value any) (bool, error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", script)
	} else {
		cmd = exec.Command("sh", "-c", script)
	}
	cmd.Env = append(os.Environ(), "VALUE="+fmt.Sprintf("%v", value))
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// Non-zero exit code means assertion failure, not an execution error
			return false, nil
		}
		return false, fmt.Errorf("script execution error: %w", err)
	}
	return true, nil
}

// normalizeViaYAML converts v to the same type representation that YAML
// unmarshalling produces (e.g. map[string]interface{} instead of map[string]string).
// This allows typed Go values to be compared with unmarshalled YAML values.
func normalizeViaYAML(v any) (any, error) {
	b, err := yaml.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := yaml.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// isSubset reports whether every key in expected exists in actual with a deeply
// equal value. Keys present in actual but absent from expected are ignored.
// This enables partial map matching, e.g. asserting {name: FOO} matches
// {name: FOO, value: "secret"} without knowing the secret.
func isSubset(expected, actual map[string]interface{}) bool {
	for k, expectedVal := range expected {
		actualVal, ok := actual[k]
		if !ok {
			return false
		}
		if !reflect.DeepEqual(actualVal, expectedVal) {
			return false
		}
	}
	return true
}
