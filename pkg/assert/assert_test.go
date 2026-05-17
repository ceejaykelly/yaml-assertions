package assert

import (
	"fmt"
	"runtime"
	"testing"
)

type AssertTestCase struct {
	name            string
	values          []byte
	object_path     string
	expected_input  any
	operator        Operator
	expected_output bool
}

func executeAssertTestCases(t *testing.T, tests []AssertTestCase) {
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Evaluate(tt.values, tt.object_path, string(tt.operator), tt.expected_input)
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			if result != tt.expected_output {
				t.Errorf("Assertion failed for test: %s", tt.name)
			}
		})
	}
}

func TestEqualAssert(t *testing.T) {
	tests := []AssertTestCase{
		{
			name:            "Test happy path with Equal operator",
			values:          []byte("foo: bar\nbaz: qux\n"),
			object_path:     "foo",
			expected_input:  "bar",
			operator:        Equal,
			expected_output: true,
		},
		{
			name:            "Test mismatching value with Equal operator",
			values:          []byte("foo: bar\nbaz: qux\n"),
			object_path:     "foo",
			expected_input:  "different value",
			operator:        Equal,
			expected_output: false,
		},
		{
			name:            "Test nested value with Equal operator",
			values:          []byte("foo:\n  bar: 123\nbaz: qux\n"),
			object_path:     "foo.bar",
			expected_input:  123,
			operator:        Equal,
			expected_output: true,
		},
		{
			name:            "Test nested array value with Equal operator",
			values:          []byte("foo:\n  bar: [123, 456]\nbaz: qux\n"),
			object_path:     "foo.bar[0]",
			expected_input:  123,
			operator:        Equal,
			expected_output: true,
		},
	}

	executeAssertTestCases(t, tests)
}

func TestExistsAssert(t *testing.T) {
	tests := []AssertTestCase{
		{
			name:            "Test existing path with Exists operator",
			values:          []byte("foo:\n  bar: 123\nbaz: qux\n"),
			object_path:     "foo.bar",
			expected_input:  nil,
			operator:        Exists,
			expected_output: true,
		},
		{
			name:            "Test non-existent path with Exists operator",
			values:          []byte("foo:\n  bar: 123\nbaz: qux\n"),
			object_path:     "foo.nonexistent",
			expected_input:  nil,
			operator:        Exists,
			expected_output: false,
		},
	}

	executeAssertTestCases(t, tests)
}

func TestContainsAssert(t *testing.T) {
	tests := []AssertTestCase{
		{
			name:            "Test string contains with Contains operator",
			values:          []byte("foo: barbaz\n"),
			object_path:     "foo",
			expected_input:  "bar",
			operator:        Contains,
			expected_output: true,
		},
		{
			name:            "Test string does not contain with Contains operator",
			values:          []byte("foo: barbaz\n"),
			object_path:     "foo",
			expected_input:  "qux",
			operator:        Contains,
			expected_output: false,
		},
	}

	executeAssertTestCases(t, tests)
}

func TestArrayContainsAssert(t *testing.T) {
	tests := []AssertTestCase{
		{
			name:            "Test array contains with Contains operator",
			values:          []byte("foo: [\"bar\", \"baz\"]\n"),
			object_path:     "foo",
			expected_input:  "bar",
			operator:        Contains,
			expected_output: true,
		},
		{
			name:            "Test array contains map with Contains operator",
			values:          []byte("foo: [{\"key\": \"value\", \"anotherKey\": \"anotherValue\"}, {\"key\": \"another value\"}]\n"),
			object_path:     "foo",
			expected_input:  map[string]string{"key": "value"},
			operator:        Contains,
			expected_output: true,
		},
	}

	executeAssertTestCases(t, tests)
}

func TestAllAssert(t *testing.T) {
	tests := []AssertTestCase{
		{
			name:            "Test all elements match with All operator",
			values:          []byte("foo: [{\"key\": \"value\", \"anotherKey\": \"anotherValue\"}, {\"key\": \"value\", \"anotherKey\": \"anotherValue\"}]\n"),
			object_path:     "foo",
			expected_input:  map[string]string{"key": "value"},
			operator:        All,
			expected_output: true,
		},
		{
			name:            "Test not all elements match with All operator",
			values:          []byte("foo: [{\"key\": \"value\"}, {\"key\": \"different value\"}]\n"),
			object_path:     "foo",
			expected_input:  map[string]string{"key": "value"},
			operator:        All,
			expected_output: false,
		},
	}

	executeAssertTestCases(t, tests)
}

func TestFilterPathAssert(t *testing.T) {
	multiContainerYAML := []byte(`
containers:
  - name: app
    image: my-repo/app:latest
    env:
      - name: APP_VAR
        value: "hello"
    securityContext:
      runAsNonRoot: true
  - name: sidecar
    image: my-repo/sidecar:latest
    env:
      - name: SIDECAR_VAR
        value: "world"
      - name: SHARED_VAR
        value: "shared"
    securityContext:
      runAsNonRoot: true
  - name: monitoring
    image: prom/exporter:latest
    port: 9090
`)

	tests := []AssertTestCase{
		{
			name:            "Filter by name and check env var exists in that container",
			values:          multiContainerYAML,
			object_path:     "containers[name=sidecar].env",
			expected_input:  map[string]string{"name": "SIDECAR_VAR"},
			operator:        Contains,
			expected_output: true,
		},
		{
			name:            "Filter confirms app container does NOT have sidecar-only env var",
			values:          multiContainerYAML,
			object_path:     "containers[name=app].env",
			expected_input:  map[string]string{"name": "SIDECAR_VAR"},
			operator:        Contains,
			expected_output: false,
		},
		{
			name:            "Filter by name then navigate to a scalar field",
			values:          multiContainerYAML,
			object_path:     "containers[name=monitoring].port",
			expected_input:  9090,
			operator:        Equal,
			expected_output: true,
		},
		{
			name:            "Filter with exists: present field",
			values:          multiContainerYAML,
			object_path:     "containers[name=sidecar].image",
			expected_input:  nil,
			operator:        Exists,
			expected_output: true,
		},
		{
			name:            "Filter with exists: missing filter key returns false",
			values:          multiContainerYAML,
			object_path:     "containers[name=nonexistent].image",
			expected_input:  nil,
			operator:        Exists,
			expected_output: false,
		},
		{
			name: "Filter then check all env vars of a specific container",
			values: []byte(`
containers:
  - name: app
    env:
      - name: VAR_A
        value: "a"
      - name: VAR_B
        value: "b"
`),
			object_path:     "containers[name=app].env",
			expected_input:  map[string]string{"value": "a"},
			operator:        Contains,
			expected_output: true,
		},
		{
			name: "Filter by numeric string field",
			values: []byte(`
items:
  - id: "1"
    label: first
  - id: "2"
    label: second
`),
			object_path:     "items[id=2].label",
			expected_input:  "second",
			operator:        Equal,
			expected_output: true,
		},
	}

	executeAssertTestCases(t, tests)
}

func TestEvaluateFunc(t *testing.T) {
	values := []byte("replicas: 3\nimage:\n  tag: v1.2.3\n")

	t.Run("custom function passes when condition holds", func(t *testing.T) {
		pass, err := EvaluateFunc(values, "replicas", func(v any) (bool, error) {
			n, ok := v.(float64)
			if !ok {
				return false, fmt.Errorf("expected float64, got %T", v)
			}
			return n > 0, nil
		})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if !pass {
			t.Error("expected pass")
		}
	})

	t.Run("custom function fails when condition does not hold", func(t *testing.T) {
		pass, err := EvaluateFunc(values, "replicas", func(v any) (bool, error) {
			n, _ := v.(float64)
			return n > 100, nil
		})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if pass {
			t.Error("expected fail")
		}
	})

	t.Run("custom function works on nested path", func(t *testing.T) {
		pass, err := EvaluateFunc(values, "image.tag", func(v any) (bool, error) {
			s, ok := v.(string)
			return ok && len(s) > 0, nil
		})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if !pass {
			t.Error("expected pass")
		}
	})

	t.Run("custom function receives nil for missing path", func(t *testing.T) {
		pass, err := EvaluateFunc(values, "nonexistent.path", func(v any) (bool, error) {
			return v == nil, nil
		})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if !pass {
			t.Error("expected pass: missing path should yield nil value")
		}
	})

	t.Run("custom function works after filter-based path", func(t *testing.T) {
		containerYAML := []byte(`
containers:
  - name: app
    replicas: 2
  - name: worker
    replicas: 5
`)
		pass, err := EvaluateFunc(containerYAML, "containers[name=worker].replicas", func(v any) (bool, error) {
			n, ok := v.(float64)
			return ok && n >= 3, nil
		})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if !pass {
			t.Error("expected pass: worker replicas should be >= 3")
		}
	})
}

func TestScriptOperator(t *testing.T) {
	values := []byte("foo: bar\ncount: 42\n")

	t.Run("script that exits 0 passes", func(t *testing.T) {
		pass, err := Evaluate(values, "foo", string(Script), "exit 0")
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if !pass {
			t.Error("expected pass for exit 0 script")
		}
	})

	t.Run("script that exits 1 fails without error", func(t *testing.T) {
		pass, err := Evaluate(values, "foo", string(Script), "exit 1")
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if pass {
			t.Error("expected fail for exit 1 script")
		}
	})

	t.Run("script receives VALUE env var", func(t *testing.T) {
		var script string
		if runtime.GOOS == "windows" {
			script = `if "%VALUE%"=="bar" (exit 0) else (exit 1)`
		} else {
			script = `[ "$VALUE" = "bar" ]`
		}
		pass, err := Evaluate(values, "foo", string(Script), script)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if !pass {
			t.Error("expected pass: VALUE env var should equal 'bar'")
		}
	})

	t.Run("script non-string expected returns error", func(t *testing.T) {
		_, err := Evaluate(values, "foo", string(Script), 42)
		if err == nil {
			t.Error("expected error when expected is not a string for script operator")
		}
	})
}
