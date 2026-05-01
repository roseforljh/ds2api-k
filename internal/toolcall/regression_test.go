package toolcall

import (
	"reflect"
	"testing"
)

func TestRegression_RobustXMLAndCDATA(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		expected []ParsedToolCall
	}{
		{
			name:     "Standard JSON scalar parameters (Regression)",
			text:     `<｜DSML｜tool_calls><｜DSML｜invoke name="foo"><｜DSML｜parameter name="a">1</｜DSML｜parameter></｜DSML｜invoke></｜DSML｜tool_calls>`,
			expected: []ParsedToolCall{{Name: "foo", Input: map[string]any{"a": float64(1)}}},
		},
		{
			name:     "XML tags parameters (Regression)",
			text:     `<｜DSML｜tool_calls><｜DSML｜invoke name="foo"><｜DSML｜parameter name="arg1">hello</｜DSML｜parameter></｜DSML｜invoke></｜DSML｜tool_calls>`,
			expected: []ParsedToolCall{{Name: "foo", Input: map[string]any{"arg1": "hello"}}},
		},
		{
			name: "CDATA parameters (New Feature)",
			text: `<｜DSML｜tool_calls><｜DSML｜invoke name="write_file"><｜DSML｜parameter name="content"><![CDATA[line 1
line 2 with <tags> and & symbols]]></｜DSML｜parameter></｜DSML｜invoke></｜DSML｜tool_calls>`,
			expected: []ParsedToolCall{{
				Name:  "write_file",
				Input: map[string]any{"content": "line 1\nline 2 with <tags> and & symbols"},
			}},
		},
		{
			name: "Nested XML with repeated parameters (New Feature)",
			text: `<｜DSML｜tool_calls><｜DSML｜invoke name="write_file"><｜DSML｜parameter name="path">script.sh</｜DSML｜parameter><｜DSML｜parameter name="content"><![CDATA[#!/bin/bash
echo "hello"
]]></｜DSML｜parameter><｜DSML｜parameter name="item">first</｜DSML｜parameter><｜DSML｜parameter name="item">second</｜DSML｜parameter></｜DSML｜invoke></｜DSML｜tool_calls>`,
			expected: []ParsedToolCall{{
				Name: "write_file",
				Input: map[string]any{
					"path":    "script.sh",
					"content": "#!/bin/bash\necho \"hello\"\n",
					"item":    []any{"first", "second"},
				},
			}},
		},
		{
			name: "Dirty XML with unescaped symbols (Robustness Improvement)",
			text: `<｜DSML｜tool_calls><｜DSML｜invoke name="bash"><｜DSML｜parameter name="command">echo "hello" > out.txt && cat out.txt</｜DSML｜parameter></｜DSML｜invoke></｜DSML｜tool_calls>`,
			expected: []ParsedToolCall{{
				Name:  "bash",
				Input: map[string]any{"command": "echo \"hello\" > out.txt && cat out.txt"},
			}},
		},
		{
			name: "Mixed JSON inside CDATA (New Hybrid Case)",
			text: `<｜DSML｜tool_calls><｜DSML｜invoke name="foo"><｜DSML｜parameter name="json_param"><![CDATA[works]]></｜DSML｜parameter></｜DSML｜invoke></｜DSML｜tool_calls>`,
			expected: []ParsedToolCall{{
				Name:  "foo",
				Input: map[string]any{"json_param": "works"},
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseToolCalls(tt.text, []string{"foo", "write_file", "bash"})
			if len(got) != len(tt.expected) {
				t.Fatalf("expected %d calls, got %d", len(tt.expected), len(got))
			}
			for i := range got {
				if got[i].Name != tt.expected[i].Name {
					t.Errorf("expected name %q, got %q", tt.expected[i].Name, got[i].Name)
				}
				if !reflect.DeepEqual(got[i].Input, tt.expected[i].Input) {
					t.Errorf("expected input %#v, got %#v", tt.expected[i].Input, got[i].Input)
				}
			}
		})
	}
}
