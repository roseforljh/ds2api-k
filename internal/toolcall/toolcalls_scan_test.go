package toolcall

import "testing"

func TestScanToolMarkupTagHandlesOfficialMalformedVariants(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  ToolMarkupTag
	}{
		{
			name:  "duplicate_lt_keeps_full_segment_start",
			input: "<<DSML|tool_calls><<DSML|invoke name=\"Read\"></DSML|invoke></DSML|tool_calls>",
			want:  ToolMarkupTag{Start: 0, Name: "tool_calls", DSMLLike: true},
		},
		{
			name:  "trailing_pipe_after_name",
			input: "<DSML|tool_calls|><DSML|invoke| name=\"Read\"></DSML|invoke|></DSML|tool_calls|>",
			want:  ToolMarkupTag{Start: 0, Name: "tool_calls", DSMLLike: true},
		},
		{
			name:  "fullwidth_trailing_pipe_after_name",
			input: "<DSML｜invoke｜ name=\"Read\">x</DSML｜invoke｜>",
			want:  ToolMarkupTag{Start: 0, Name: "invoke", DSMLLike: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := FindToolMarkupTagOutsideIgnored(tc.input, 0)
			if !ok {
				t.Fatalf("expected tool markup tag in %q", tc.input)
			}
			if got.Start != tc.want.Start || got.Name != tc.want.Name || got.DSMLLike != tc.want.DSMLLike {
				t.Fatalf("got tag start=%d name=%q dsml=%v, want start=%d name=%q dsml=%v", got.Start, got.Name, got.DSMLLike, tc.want.Start, tc.want.Name, tc.want.DSMLLike)
			}
		})
	}
}

func TestIsPartialToolMarkupTagPrefixHandlesGenericDSMLPrefixes(t *testing.T) {
	cases := []string{
		"<D",
		"<DSML",
		"<DSML|tool_ca",
		"<DSML｜invoke｜",
		"<DSML__param",
		"<#DSM",
		"<⌜DS",
		"<｜begin▁of▁inv",
	}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			if !IsPartialToolMarkupTagPrefix(input) {
				t.Fatalf("expected %q to be treated as a partial tool markup prefix", input)
			}
		})
	}
	if IsPartialToolMarkupTagPrefix("<tool_name") {
		t.Fatal("plain non-tool XML-ish prefix should not be held")
	}
}
