package toolstream

import (
	"ds2api/internal/toolcall"
	"strings"
	"testing"
)

func TestProcessToolSieveInterceptsXMLToolCallWithoutLeak(t *testing.T) {
	var state State
	// Simulate a model producing XML tool call output chunk by chunk.
	chunks := []string{
		"<｜DSML｜tool_calls>\n",
		`  <｜DSML｜invoke name="read_file">` + "\n",
		`    <｜DSML｜parameter name="path">README.MD</｜DSML｜parameter>` + "\n",
		"  </｜DSML｜invoke>\n",
		"</｜DSML｜tool_calls>",
	}
	var events []Event
	for _, c := range chunks {
		events = append(events, ProcessChunk(&state, c, []string{"read_file"})...)
	}
	events = append(events, Flush(&state, []string{"read_file"})...)

	var textContent string
	var toolCalls int
	for _, evt := range events {
		if evt.Content != "" {
			textContent += evt.Content
		}
		toolCalls += len(evt.ToolCalls)
	}

	if strings.Contains(textContent, "<｜DSML｜invoke ") {
		t.Fatalf("XML tool call content leaked to text: %q", textContent)
	}
	if strings.Contains(textContent, "read_file") {
		t.Fatalf("tool name leaked to text: %q", textContent)
	}
	if toolCalls == 0 {
		t.Fatal("expected tool calls to be extracted, got none")
	}
}

func TestProcessToolSieveInterceptsDSMLToolCallWithoutLeak(t *testing.T) {
	var state State
	chunks := []string{
		"<｜DSML｜tool",
		"_calls>\n",
		`  <｜DSML｜invoke name="read_file">` + "\n",
		`    <｜DSML｜parameter name="path">README.MD</｜DSML｜parameter>` + "\n",
		"  </｜DSML｜invoke>\n",
		"</｜DSML｜tool_calls>",
	}
	var events []Event
	for _, c := range chunks {
		events = append(events, ProcessChunk(&state, c, []string{"read_file"})...)
	}
	events = append(events, Flush(&state, []string{"read_file"})...)

	var textContent string
	var toolCalls int
	for _, evt := range events {
		textContent += evt.Content
		toolCalls += len(evt.ToolCalls)
	}

	if strings.Contains(strings.ToLower(textContent), "dsml") || strings.Contains(textContent, "read_file") {
		t.Fatalf("DSML tool call content leaked to text: %q", textContent)
	}
	if toolCalls != 1 {
		t.Fatalf("expected one DSML tool call, got %d events=%#v", toolCalls, events)
	}
}

func TestProcessToolSieveInterceptsOfficialFullwidthDSMLToolCallWithoutLeak(t *testing.T) {
	var state State
	chunks := []string{
		"<｜DSML｜tool",
		"_calls>\n",
		`  <｜DSML｜invoke name="Read">` + "\n",
		`    <｜DSML｜parameter name="file_path" string="true">README.md</｜DSML｜parameter>` + "\n",
		`    <｜DSML｜parameter name="limit" string="false">55</｜DSML｜parameter>` + "\n",
		"  </｜DSML｜invoke>\n",
		"</｜DSML｜tool_calls>",
	}
	var events []Event
	for _, c := range chunks {
		events = append(events, ProcessChunk(&state, c, []string{"Read"})...)
	}
	events = append(events, Flush(&state, []string{"Read"})...)

	var textContent strings.Builder
	var calls []toolcall.ParsedToolCall
	for _, evt := range events {
		textContent.WriteString(evt.Content)
		calls = append(calls, evt.ToolCalls...)
	}
	if strings.Contains(textContent.String(), "DSML") || strings.Contains(textContent.String(), "README.md") {
		t.Fatalf("official fullwidth DSML tool call leaked to text: %q", textContent.String())
	}
	if len(calls) != 1 || calls[0].Name != "Read" {
		t.Fatalf("expected one official fullwidth DSML Read call, got events=%#v calls=%#v", events, calls)
	}
}

func TestProcessToolSieveRejectsOfficialFullwidthDSMLInvalidJSONWithoutLeak(t *testing.T) {
	var state State
	chunks := []string{
		"<｜DSML｜tool_calls>\n",
		`  <｜DSML｜invoke name="Read">` + "\n",
		`    <｜DSML｜parameter name="file_path" string="true">README.md</｜DSML｜parameter>` + "\n",
		`    <｜DSML｜parameter name="limit" string="false">{bad json}</｜DSML｜parameter>` + "\n",
		"  </｜DSML｜invoke>\n",
		"</｜DSML｜tool_calls>",
	}
	var events []Event
	for _, c := range chunks {
		events = append(events, ProcessChunk(&state, c, []string{"Read"})...)
	}
	events = append(events, Flush(&state, []string{"Read"})...)

	var textContent strings.Builder
	var toolCalls int
	for _, evt := range events {
		textContent.WriteString(evt.Content)
		toolCalls += len(evt.ToolCalls)
	}
	if textContent.Len() != 0 {
		t.Fatalf("expected invalid official DSML not to leak as text, got %q", textContent.String())
	}
	if toolCalls != 0 {
		t.Fatalf("expected no tool calls for invalid official DSML, got %d events=%#v", toolCalls, events)
	}
	if !strings.Contains(state.MalformedToolFeedback, "<｜DSML｜tool_calls>") {
		t.Fatalf("expected malformed feedback to retain rejected DSML, got %q", state.MalformedToolFeedback)
	}
}

func TestProcessToolSieveHoldsSplitOfficialDSMLTagPrefix(t *testing.T) {
	var state State
	events := ProcessChunk(&state, "hello <｜DS", []string{"Read"})
	if len(events) != 1 || events[0].Content != "hello " {
		t.Fatalf("expected only safe prefix before split official DSML tag, got %#v", events)
	}
	events = ProcessChunk(&state, `ML｜tool_calls><｜DSML｜invoke name="Read"><｜DSML｜parameter name="file_path" string="true">README.md</｜DSML｜parameter></｜DSML｜invoke></｜DSML｜tool_calls>`, []string{"Read"})
	events = append(events, Flush(&state, []string{"Read"})...)

	var textContent strings.Builder
	var calls []toolcall.ParsedToolCall
	for _, evt := range events {
		textContent.WriteString(evt.Content)
		calls = append(calls, evt.ToolCalls...)
	}
	if strings.Contains(textContent.String(), "DSML") || strings.Contains(textContent.String(), "README.md") {
		t.Fatalf("split official DSML leaked to text: %q events=%#v", textContent.String(), events)
	}
	if len(calls) != 1 || calls[0].Name != "Read" {
		t.Fatalf("expected one Read call after split official DSML tag, got %#v", calls)
	}
}

func TestProcessToolSievePrefersDSMLBlockOverEarlierCanonicalBlock(t *testing.T) {
	var state State
	chunk := `<tool_calls><invoke name="Read"><parameter name="file_path">legacy.md</parameter></invoke></tool_calls>` +
		`<｜DSML｜tool_calls><｜DSML｜invoke name="Read"><｜DSML｜parameter name="file_path" string="true">official.md</｜DSML｜parameter></｜DSML｜invoke></｜DSML｜tool_calls>`
	events := ProcessChunk(&state, chunk, []string{"Read"})
	events = append(events, Flush(&state, []string{"Read"})...)

	var calls []toolcall.ParsedToolCall
	for _, evt := range events {
		calls = append(calls, evt.ToolCalls...)
	}
	if len(calls) != 0 {
		t.Fatalf("expected non-official block to force retry before official block, got %#v events=%#v", calls, events)
	}
	if !strings.Contains(state.MalformedToolFeedback, "<tool_calls>") {
		t.Fatalf("expected canonical malformed feedback, got %q", state.MalformedToolFeedback)
	}
}

func TestProcessToolSieveInterceptsDSMLDSEPToolCallWithoutLeak(t *testing.T) {
	state := State{}
	chunks := []string{
		"<｜DSML｜tool_calls>\n",
		`<｜DSML｜invoke name="Read">` + "\n",
		`<｜DSML｜parameter name="file_path"><![CDATA[C:\Users\me\repo\README.md]]></｜DSML｜parameter>` + "\n",
		`<｜DSML｜parameter name="limit"><![CDATA[55]]></｜DSML｜parameter>` + "\n",
		"</｜DSML｜invoke>\n",
		"</｜DSML｜tool_calls>",
	}
	var events []Event
	for _, chunk := range chunks {
		events = append(events, ProcessChunk(&state, chunk, []string{"Read"})...)
	}
	events = append(events, Flush(&state, []string{"Read"})...)

	var textContent strings.Builder
	var calls []toolcall.ParsedToolCall
	for _, evt := range events {
		textContent.WriteString(evt.Content)
		calls = append(calls, evt.ToolCalls...)
	}
	if strings.Contains(textContent.String(), "DSML_DSEP") || strings.Contains(textContent.String(), "README.md") {
		t.Fatalf("DSML_DSEP tool call content leaked to text: %q", textContent.String())
	}
	if len(calls) != 1 || calls[0].Name != "Read" || calls[0].Input["file_path"] == "" {
		t.Fatalf("expected one DSML_DSEP Read tool call, got events=%#v calls=%#v", events, calls)
	}
}

func TestProcessToolSieveInterceptsMixedFullwidthDSMLToolCallWithoutLeak(t *testing.T) {
	var state State
	chunks := []string{
		"<｜DSML｜tool",
		"_calls>\n",
		`  <｜DSML｜invoke name="TaskUpdate">` + "\n",
		`    <｜DSML｜parameter name="status">completed</｜DSML｜parameter>` + "\n",
		`    <｜DSML｜parameter name="taskId">1</｜DSML｜parameter>` + "\n",
		"  </｜DSML｜invoke>\n",
		"</｜DSML｜tool_calls>",
	}
	var events []Event
	for _, c := range chunks {
		events = append(events, ProcessChunk(&state, c, []string{"TaskUpdate"})...)
	}
	events = append(events, Flush(&state, []string{"TaskUpdate"})...)

	var textContent strings.Builder
	var toolCalls int
	for _, evt := range events {
		textContent.WriteString(evt.Content)
		toolCalls += len(evt.ToolCalls)
	}

	if strings.Contains(textContent.String(), "DSML") || strings.Contains(textContent.String(), "TaskUpdate") {
		t.Fatalf("mixed-separator DSML tool call content leaked to text: %q", textContent.String())
	}
	if toolCalls != 1 {
		t.Fatalf("expected one mixed-separator DSML tool call, got %d events=%#v", toolCalls, events)
	}
}

func TestProcessToolSieveInterceptsBareMixedFullwidthDSMLToolCallWithoutLeak(t *testing.T) {
	var state State
	chunks := []string{
		"<｜DSML｜tool",
		"_calls>\n",
		`  <｜DSML｜invoke name="TaskUpdate">` + "\n",
		`    <｜DSML｜parameter name="status">completed</｜DSML｜parameter>` + "\n",
		`    <｜DSML｜parameter name="taskId">1</｜DSML｜parameter>` + "\n",
		"  </｜DSML｜invoke>\n",
		"</｜DSML｜tool_calls>",
	}
	var events []Event
	for _, c := range chunks {
		events = append(events, ProcessChunk(&state, c, []string{"TaskUpdate"})...)
	}
	events = append(events, Flush(&state, []string{"TaskUpdate"})...)

	var textContent strings.Builder
	var toolCalls int
	for _, evt := range events {
		textContent.WriteString(evt.Content)
		toolCalls += len(evt.ToolCalls)
	}

	if strings.Contains(textContent.String(), "DSML") || strings.Contains(textContent.String(), "TaskUpdate") {
		t.Fatalf("bare mixed-separator DSML tool call content leaked to text: %q", textContent.String())
	}
	if toolCalls != 1 {
		t.Fatalf("expected one bare mixed-separator DSML tool call, got %d events=%#v", toolCalls, events)
	}
}

func TestProcessToolSieveInterceptsBracketDSMLToolCallWithoutLeak(t *testing.T) {
	var state State
	chunks := []string{
		"<｜DSML｜tool",
		"_calls>\n",
		`  <｜DSML｜invoke name="Bash">` + "\n",
		`    <｜DSML｜parameter name="command"><![CDATA[pwd]]></｜DSML｜parameter>` + "\n",
		"  </｜DSML｜invoke>\n",
		"</｜DSML｜tool_calls>",
	}
	var events []Event
	for _, chunk := range chunks {
		events = append(events, ProcessChunk(&state, chunk, []string{"Bash"})...)
	}
	events = append(events, Flush(&state, []string{"Bash"})...)

	var textContent strings.Builder
	toolCalls := 0
	for _, evt := range events {
		textContent.WriteString(evt.Content)
		toolCalls += len(evt.ToolCalls)
	}

	if strings.Contains(textContent.String(), "DSML") || strings.Contains(textContent.String(), "Bash") {
		t.Fatalf("bracket DSML tool call content leaked to text: %q", textContent.String())
	}
	if toolCalls != 1 {
		t.Fatalf("expected one bracket DSML tool call, got %d events=%#v", toolCalls, events)
	}
}

func TestProcessToolSieveDropsBracketDSMEmptyReadWithoutLeak(t *testing.T) {
	var state State
	chunk := `<｜DSML｜tool_calls><｜DSML｜invoke name="Read"><｜DSML｜parameter name="file_path"></｜DSML｜parameter></｜DSML｜invoke></｜DSML｜tool_calls>`
	events := ProcessChunk(&state, chunk, []string{"Read"})
	events = append(events, Flush(&state, []string{"Read"})...)
	for _, evt := range events {
		if evt.Content != "" || len(evt.ToolCalls) > 0 {
			t.Fatalf("expected invalid empty Read call to be hidden from client and not emitted, got %#v", events)
		}
	}
}

func TestProcessToolSieveDropsSMLSentinelProtocolWithoutLeak(t *testing.T) {
	var state State
	chunk := strings.Join([]string{
		"prefix ",
		"<SML_DOLLAR_EM_OLLAR_\n",
		"<SM_OPEN_ATTR\n",
		"name=\"Read\"\n",
		"file_path=\"C:\\Users\\33039\\Desktop\\KunBoxForWindows\\graphify-out\\GRAPH_REPORT.md\"\n",
		"<SM_CLOSE_ATTR\n",
		"<SM_OPEN_ATTR\n",
		"name=\"Bash\"\n",
		"command=\"ls -la C:/Users/33039/Desktop/KunBoxForWindows/\"\n",
		"description=\"List top-level project structure\"\n",
		"<SMCLOSE_ATTR\n",
		"</SML_DOLLAR_EM_OLLAR_",
		" suffix",
	}, "")
	events := ProcessChunk(&state, chunk, []string{"Read", "Bash"})
	events = append(events, Flush(&state, []string{"Read", "Bash"})...)
	var textContent strings.Builder
	toolCalls := 0
	for _, evt := range events {
		textContent.WriteString(evt.Content)
		toolCalls += len(evt.ToolCalls)
	}
	if toolCalls != 0 {
		t.Fatalf("expected SML sentinel protocol not to emit tool calls, got %d events=%#v", toolCalls, events)
	}
	if textContent.String() != "prefix  suffix" {
		t.Fatalf("expected SML sentinel protocol to be dropped without leaking text, got %q", textContent.String())
	}
}

func TestProcessToolSieveDropsUnparseableReadFilePathWithoutLeak(t *testing.T) {
	var state State
	chunk := `<｜DSML｜tool_calls><｜DSML｜invoke name="Read"><｜DSML｜parameter name="file_path">C:\Users\me\repo\README.md`
	events := ProcessChunk(&state, chunk, []string{"Read"})
	events = append(events, Flush(&state, []string{"Read"})...)
	for _, evt := range events {
		if evt.Content != "" || len(evt.ToolCalls) > 0 {
			t.Fatalf("expected unparseable Read.file_path call to be hidden from client and not emitted, got %#v", events)
		}
	}
}

func TestProcessToolSieveDropsBareReadMissingFilePathWithoutLeak(t *testing.T) {
	var state State
	chunk := `<｜DSML｜invoke name="Read"><｜DSML｜parameter name="limit"><![CDATA[30]]>`
	events := ProcessChunk(&state, chunk, []string{"Read"})
	events = append(events, Flush(&state, []string{"Read"})...)
	for _, evt := range events {
		if evt.Content != "" || len(evt.ToolCalls) > 0 {
			t.Fatalf("expected bare Read call missing file_path to be hidden from client and not emitted, got %#v", events)
		}
	}
	if !strings.Contains(state.MalformedToolFeedback, `<｜DSML｜invoke name="Read">`) {
		t.Fatalf("expected bare Read malformed feedback to be retained for retry, got %q", state.MalformedToolFeedback)
	}
}

func TestProcessToolSieveDropsHashDSMLReadFilePathWithoutLeak(t *testing.T) {
	var state State
	chunk := `<#DSML#tool_calls>
<#DSML#invoke name="Read">
<#DSML#parameter name="file_path">#CDATA#C:\Users\me\repo\settings.rs#CDATA#</#DSML#parameter>
</#DSML#invoke>
</#DSML#tool_calls>`
	events := ProcessChunk(&state, chunk, []string{"Read"})
	events = append(events, Flush(&state, []string{"Read"})...)
	for _, evt := range events {
		if evt.Content != "" || len(evt.ToolCalls) > 0 {
			t.Fatalf("expected hash DSML Read.file_path call to be hidden from client and not emitted, got %#v", events)
		}
	}
	if !strings.Contains(state.MalformedToolFeedback, "<#DSML#tool_calls>") {
		t.Fatalf("expected hash DSML malformed feedback to be retained for retry, got %q", state.MalformedToolFeedback)
	}
}

func TestProcessToolSieveDropsDSMLCallsAliasWithoutLeak(t *testing.T) {
	var state State
	chunk := `<dsml_calls>
<dsml_invoke name="Read">
<dsml_parameter name="file_path"><![CDATA[C:\Users\me\repo\README.md]]></dsml_parameter>
</dsml_invoke>
</dsml_calls>`
	events := ProcessChunk(&state, chunk, []string{"Read"})
	events = append(events, Flush(&state, []string{"Read"})...)
	for _, evt := range events {
		if evt.Content != "" || len(evt.ToolCalls) > 0 {
			t.Fatalf("expected dsml_calls alias to be hidden from client and not emitted, got %#v", events)
		}
	}
	if !strings.Contains(state.MalformedToolFeedback, "<dsml_calls>") {
		t.Fatalf("expected dsml_calls malformed feedback to be retained for retry, got %q", state.MalformedToolFeedback)
	}
}

func TestProcessToolSieveDropsChineseBracketDSMLWithoutLeak(t *testing.T) {
	var state State
	chunks := []string{
		"● ",
		"<【DS",
		"ML】tool_calls>\n",
		`<【DSML】invoke name="Agent">` + "\n",
		`<【DSML】parameter name="description"></【DSML】parameter>` + "\n",
		`<【DSML】parameter name="prompt"><![CDATA[Read files]]></【DSML】parameter>` + "\n",
		"</【DSML】invoke>\n",
		"</【DSML】tool_calls>",
	}
	var events []Event
	for _, chunk := range chunks {
		events = append(events, ProcessChunk(&state, chunk, []string{"Agent"})...)
	}
	events = append(events, Flush(&state, []string{"Agent"})...)
	for _, evt := range events {
		if evt.Content != "" || len(evt.ToolCalls) > 0 {
			t.Fatalf("expected Chinese-bracket DSML to be hidden from client and not emitted, got %#v", events)
		}
	}
	if !strings.Contains(state.MalformedToolFeedback, "<【DSML】tool_calls>") {
		t.Fatalf("expected Chinese-bracket DSML malformed feedback to be retained, got %q", state.MalformedToolFeedback)
	}
}

func TestProcessToolSieveDropsUnknownStructuredToolIntentWithoutLeak(t *testing.T) {
	var state State
	chunk := `<action name="Read"><arg name="file_path"><![CDATA[C:\Users\me\repo\README.md]]></arg></action>`
	events := ProcessChunk(&state, chunk, []string{"Read"})
	events = append(events, Flush(&state, []string{"Read"})...)
	for _, evt := range events {
		if evt.Content != "" || len(evt.ToolCalls) > 0 {
			t.Fatalf("expected unknown structured Read call to be hidden and retried, got %#v", events)
		}
	}
	if !strings.Contains(state.MalformedToolFeedback, `<action name="Read">`) {
		t.Fatalf("expected unknown structured tool feedback to be retained, got %q", state.MalformedToolFeedback)
	}
}

func TestFindToolSegmentStartDetectsDSMLCallsAlias(t *testing.T) {
	got := findToolSegmentStart(nil, "prefix <dsml_calls>", []string{"Read"})
	if got != len("prefix ") {
		t.Fatalf("expected dsml_calls alias start at %d, got %d", len("prefix "), got)
	}
}

func TestProcessToolSieveDropsLocalizedPunctuationReadCallWithoutLeak(t *testing.T) {
	var state State
	chunk := `● <｜tool_calls＞
<！invoke name=“Read”>
<！parameter name=“file_path”><！[CDATA[C:\Users\me\repo\README.md]]><！/parameter>
<！/invoke>
</！tool_calls＞`
	events := ProcessChunk(&state, chunk, []string{"Read"})
	events = append(events, Flush(&state, []string{"Read"})...)
	for _, evt := range events {
		if evt.Content != "" || len(evt.ToolCalls) > 0 {
			t.Fatalf("expected localized-punctuation Read call to be hidden from client and not emitted, got %#v", events)
		}
	}
	if !strings.Contains(state.MalformedToolFeedback, "<｜tool_calls＞") {
		t.Fatalf("expected localized-punctuation malformed feedback to be retained for retry, got %q", state.MalformedToolFeedback)
	}
}

func TestProcessToolSieveDropsSentenceInvokeReadCallWithoutLeak(t *testing.T) {
	var state State
	chunk := `● <｜begin▁of▁sentence｜>
<｜begin▁of▁invoke name="Read">
<｜begin▁of▁parameter name="file_path"></｜begin▁of▁parameter>
<｜begin▁of▁parameter name="limit"></｜begin▁of▁parameter>
<｜begin▁of▁parameter name="offset"></｜begin▁of▁parameter>
</｜begin▁of▁invoke>
<｜end▁of▁sentence｜>`
	events := ProcessChunk(&state, chunk, []string{"Read"})
	events = append(events, Flush(&state, []string{"Read"})...)
	for _, evt := range events {
		if evt.Content != "" || len(evt.ToolCalls) > 0 {
			t.Fatalf("expected sentence/invoke Read call to be hidden from client and not emitted, got %#v", events)
		}
	}
	if !strings.Contains(state.MalformedToolFeedback, "begin▁of▁invoke") {
		t.Fatalf("expected sentence/invoke malformed feedback to be retained for retry, got %q", state.MalformedToolFeedback)
	}
}

func TestProcessToolSieveDropsSentenceBareReadCallWithoutLeak(t *testing.T) {
	var state State
	chunk := `● <｜begin▁of▁sentence｜>Read
reasoningReading the file at the insertion point to get precise content for the Edit tool.
I need to read the file around the insertion point to get exact content for matching.
</｜DSML｜parameter>
</｜DSML｜parameter>
</｜DSML｜parameter>
</｜DSML｜parameter>
</｜DSML｜invoke>`
	events := ProcessChunk(&state, chunk, []string{"Read"})
	events = append(events, Flush(&state, []string{"Read"})...)
	for _, evt := range events {
		if evt.Content != "" || len(evt.ToolCalls) > 0 {
			t.Fatalf("expected sentence/bare Read call to be hidden from client and not emitted, got %#v", events)
		}
	}
	if !strings.Contains(state.MalformedToolFeedback, "begin▁of▁sentence") {
		t.Fatalf("expected sentence/bare malformed feedback to be retained for retry, got %q", state.MalformedToolFeedback)
	}
}

func TestProcessToolSieveDropsMalformedSkillCallWithoutLeak(t *testing.T) {
	var state State
	chunk := `Skill
<skill>pua</skill>
</|DSML|skill_calls>`
	events := ProcessChunk(&state, chunk, []string{"Skill"})
	events = append(events, Flush(&state, []string{"Skill"})...)
	for _, evt := range events {
		if evt.Content != "" || len(evt.ToolCalls) > 0 {
			t.Fatalf("expected malformed skill call to be hidden from client and not emitted, got %#v", events)
		}
	}
	if !strings.Contains(state.MalformedToolFeedback, "<skill>pua</skill>") {
		t.Fatalf("expected malformed skill feedback to be retained for retry, got %q", state.MalformedToolFeedback)
	}
}

func TestProcessToolSieveBuffersLocalizedPunctuationReadCallAcrossChunks(t *testing.T) {
	var state State
	chunks := []string{
		`● <｜tool_calls＞` + "\n",
		`<！invoke name=“Read”>` + "\n",
		`<！parameter name=“file_path”><！[CDATA[C:\Users\me\repo\README.md]]><！/parameter>` + "\n",
		`<！/invoke>` + "\n",
		`</！tool_calls＞`,
	}
	var events []Event
	for _, chunk := range chunks {
		events = append(events, ProcessChunk(&state, chunk, []string{"Read"})...)
	}
	events = append(events, Flush(&state, []string{"Read"})...)
	for _, evt := range events {
		if evt.Content != "" || len(evt.ToolCalls) > 0 {
			t.Fatalf("expected split localized-punctuation Read call to be buffered and hidden, got %#v", events)
		}
	}
	if !strings.Contains(state.MalformedToolFeedback, "<｜tool_calls＞") ||
		!strings.Contains(state.MalformedToolFeedback, "file_path") {
		t.Fatalf("expected complete localized malformed feedback to be retained, got %q", state.MalformedToolFeedback)
	}
}

func TestProcessToolSievePreservesMixedFullwidthDSMLMentionBeforeToolCall(t *testing.T) {
	var state State
	chunks := []string{
		"Summary: support mixed <｜DSML｜tool_calls> wrappers.\n\n",
		"<｜DSML｜tool_calls>\n",
		"<｜DSML｜invoke name=\"TaskUpdate\">\n",
		"<｜DSML｜parameter name=\"status\">completed</｜DSML｜parameter>\n",
		"</｜DSML｜invoke>\n",
		"</｜DSML｜tool_calls>",
	}
	var events []Event
	for _, c := range chunks {
		events = append(events, ProcessChunk(&state, c, []string{"TaskUpdate"})...)
	}
	events = append(events, Flush(&state, []string{"TaskUpdate"})...)

	var textContent strings.Builder
	var toolCalls int
	for _, evt := range events {
		textContent.WriteString(evt.Content)
		toolCalls += len(evt.ToolCalls)
	}

	if !strings.Contains(textContent.String(), "Summary: support mixed <｜DSML｜tool_calls> wrappers.") {
		t.Fatalf("expected mixed-separator DSML prose mention to be preserved, got %q", textContent.String())
	}
	if strings.Contains(textContent.String(), "TaskUpdate") {
		t.Fatalf("real mixed-separator DSML tool call leaked to text: %q", textContent.String())
	}
	if toolCalls != 1 {
		t.Fatalf("expected one mixed-separator DSML tool call, got %d events=%#v", toolCalls, events)
	}
}

func TestProcessToolSieveHandlesLongXMLToolCall(t *testing.T) {
	var state State
	const toolName = "write_to_file"
	payload := strings.Repeat("x", 4096)
	splitAt := len(payload) / 2
	chunks := []string{
		"<｜DSML｜tool_calls>\n  <｜DSML｜invoke name=\"" + toolName + "\">\n    <｜DSML｜parameter name=\"content\"><![CDATA[",
		payload[:splitAt],
		payload[splitAt:],
		"]]></｜DSML｜parameter>\n  </｜DSML｜invoke>\n</｜DSML｜tool_calls>",
	}

	var events []Event
	for _, c := range chunks {
		events = append(events, ProcessChunk(&state, c, []string{toolName})...)
	}
	events = append(events, Flush(&state, []string{toolName})...)

	var textContent strings.Builder
	toolCalls := 0
	var gotPayload any
	for _, evt := range events {
		if evt.Content != "" {
			textContent.WriteString(evt.Content)
		}
		if len(evt.ToolCalls) > 0 && gotPayload == nil {
			gotPayload = evt.ToolCalls[0].Input["content"]
		}
		toolCalls += len(evt.ToolCalls)
	}

	if toolCalls != 1 {
		t.Fatalf("expected one long XML tool call, got %d events=%#v", toolCalls, events)
	}
	if textContent.Len() != 0 {
		t.Fatalf("expected no leaked text for long XML tool call, got %q", textContent.String())
	}
	got, _ := gotPayload.(string)
	if got != payload {
		t.Fatalf("expected long XML payload to survive intact, got len=%d want=%d", len(got), len(payload))
	}
}

func TestProcessToolSieveKeepsCDATAEmbeddedToolClosingBuffered(t *testing.T) {
	var state State
	payload := strings.Join([]string{
		"# DS2API 4.0 更新内容",
		"",
		strings.Repeat("x", 4096),
		"```xml",
		"<｜DSML｜tool_calls>",
		"  <｜DSML｜invoke name=\"demo\">",
		"    <｜DSML｜parameter name=\"value\">x</｜DSML｜parameter>",
		"  </｜DSML｜invoke>",
		"</｜DSML｜tool_calls>",
		"```",
		"tail",
	}, "\n")
	innerClose := strings.Index(payload, "</｜DSML｜tool_calls>") + len("</｜DSML｜tool_calls>")
	chunks := []string{
		"<｜DSML｜tool_calls>\n  <｜DSML｜invoke name=\"Write\">\n    <｜DSML｜parameter name=\"content\"><![CDATA[",
		payload[:innerClose],
		payload[innerClose:],
		"]]></｜DSML｜parameter>\n    <｜DSML｜parameter name=\"file_path\">DS2API-4.0-Release-Notes.md</｜DSML｜parameter>\n  </｜DSML｜invoke>\n</｜DSML｜tool_calls>",
	}

	var events []Event
	for i, c := range chunks {
		next := ProcessChunk(&state, c, []string{"Write"})
		if i <= 1 {
			for _, evt := range next {
				if evt.Content != "" || len(evt.ToolCalls) > 0 {
					t.Fatalf("expected no events before outer closing tag, chunk=%d events=%#v", i, next)
				}
			}
		}
		events = append(events, next...)
	}
	events = append(events, Flush(&state, []string{"Write"})...)

	var textContent strings.Builder
	var gotPayload string
	toolCalls := 0
	for _, evt := range events {
		textContent.WriteString(evt.Content)
		if len(evt.ToolCalls) > 0 {
			toolCalls += len(evt.ToolCalls)
			gotPayload, _ = evt.ToolCalls[0].Input["content"].(string)
		}
	}

	if toolCalls != 1 {
		t.Fatalf("expected one parsed tool call, got %d events=%#v", toolCalls, events)
	}
	if textContent.Len() != 0 {
		t.Fatalf("expected no leaked text, got %q", textContent.String())
	}
	if gotPayload != payload {
		t.Fatalf("expected full CDATA payload to survive intact, got len=%d want=%d", len(gotPayload), len(payload))
	}
}

func TestProcessToolSieveFallsBackWhenCDATANeverCloses(t *testing.T) {
	var state State
	chunks := []string{
		"<｜DSML｜tool_calls>\n  <｜DSML｜invoke name=\"Write\">\n    <｜DSML｜parameter name=\"content\"><![CDATA[",
		"hello world",
		"</｜DSML｜parameter>\n  </｜DSML｜invoke>\n</｜DSML｜tool_calls>",
	}
	var events []Event
	for _, c := range chunks {
		events = append(events, ProcessChunk(&state, c, []string{"Write"})...)
	}
	events = append(events, Flush(&state, []string{"Write"})...)

	var textContent strings.Builder
	toolCalls := 0
	for _, evt := range events {
		if evt.Content != "" {
			textContent.WriteString(evt.Content)
		}
		toolCalls += len(evt.ToolCalls)
		if len(evt.ToolCalls) > 0 {
			if got, _ := evt.ToolCalls[0].Input["content"].(string); got != "hello world" {
				t.Fatalf("expected recovered CDATA payload, got %q", got)
			}
		}
	}

	if toolCalls != 0 {
		t.Fatalf("expected unclosed CDATA payload to be rejected, got %d tool calls events=%#v", toolCalls, events)
	}
	if textContent.Len() != 0 || !strings.Contains(state.MalformedToolFeedback, "<｜DSML｜tool_calls>") {
		t.Fatalf("expected no leaked text and malformed feedback, text=%q feedback=%q", textContent.String(), state.MalformedToolFeedback)
	}
}

func TestProcessToolSieveXMLWithLeadingText(t *testing.T) {
	var state State
	// Model outputs some prose then an XML tool call.
	chunks := []string{
		"Let me check the file.\n",
		"<｜DSML｜tool_calls>\n  <｜DSML｜invoke name=\"read_file\">\n",
		`    <｜DSML｜parameter name="path">go.mod</｜DSML｜parameter>` + "\n  </｜DSML｜invoke>\n</｜DSML｜tool_calls>",
	}
	var events []Event
	for _, c := range chunks {
		events = append(events, ProcessChunk(&state, c, []string{"read_file"})...)
	}
	events = append(events, Flush(&state, []string{"read_file"})...)

	var textContent string
	var toolCalls int
	for _, evt := range events {
		if evt.Content != "" {
			textContent += evt.Content
		}
		toolCalls += len(evt.ToolCalls)
	}

	// Leading text should be emitted.
	if !strings.Contains(textContent, "Let me check the file.") {
		t.Fatalf("expected leading text to be emitted, got %q", textContent)
	}
	// The XML itself should NOT leak.
	if strings.Contains(textContent, "<｜DSML｜invoke ") {
		t.Fatalf("XML tool call content leaked to text: %q", textContent)
	}
	if toolCalls == 0 {
		t.Fatal("expected tool calls to be extracted, got none")
	}
}

func TestProcessToolSievePassesThroughNonToolXMLBlock(t *testing.T) {
	var state State
	chunk := `<tool><title>示例 XML</title><body>plain text xml payload</body></tool>`
	events := ProcessChunk(&state, chunk, []string{"read_file"})
	events = append(events, Flush(&state, []string{"read_file"})...)

	var textContent strings.Builder
	toolCalls := 0
	for _, evt := range events {
		textContent.WriteString(evt.Content)
		toolCalls += len(evt.ToolCalls)
	}
	if toolCalls != 0 {
		t.Fatalf("expected no tool calls for plain XML payload, got %d events=%#v", toolCalls, events)
	}
	if textContent.String() != chunk {
		t.Fatalf("expected XML payload to pass through unchanged, got %q", textContent.String())
	}
}

func TestProcessToolSieveNonToolXMLKeepsSuffixForToolParsing(t *testing.T) {
	var state State
	chunk := `<tool><title>plain xml</title></tool><｜DSML｜tool_calls><｜DSML｜invoke name="read_file"><｜DSML｜parameter name="path">README.MD</｜DSML｜parameter></｜DSML｜invoke></｜DSML｜tool_calls>`
	events := ProcessChunk(&state, chunk, []string{"read_file"})
	events = append(events, Flush(&state, []string{"read_file"})...)

	var textContent strings.Builder
	toolCalls := 0
	for _, evt := range events {
		textContent.WriteString(evt.Content)
		toolCalls += len(evt.ToolCalls)
	}
	if !strings.Contains(textContent.String(), `<tool><title>plain xml</title></tool>`) {
		t.Fatalf("expected leading non-tool XML to be preserved, got %q", textContent.String())
	}
	if strings.Contains(textContent.String(), `<｜DSML｜tool_calls><invoke`) {
		t.Fatalf("expected invoke tool XML to be intercepted, got %q", textContent.String())
	}
	if toolCalls != 1 {
		t.Fatalf("expected exactly one parsed tool call from suffix, got %d events=%#v", toolCalls, events)
	}
}

func TestProcessToolSieveDropsMalformedExecutableXMLBlock(t *testing.T) {
	var state State
	chunk := `<｜DSML｜tool_calls><｜DSML｜invoke name="read_file"><param>{"path":"README.md"}</param></｜DSML｜invoke></｜DSML｜tool_calls>`
	events := ProcessChunk(&state, chunk, []string{"read_file"})
	events = append(events, Flush(&state, []string{"read_file"})...)

	for _, evt := range events {
		if evt.Content != "" || len(evt.ToolCalls) > 0 {
			t.Fatalf("expected malformed executable-looking XML to be hidden for retry, got %#v", events)
		}
	}
	if !strings.Contains(state.MalformedToolFeedback, `<｜DSML｜tool_calls>`) {
		t.Fatalf("expected malformed executable-looking XML feedback to be retained, got %q", state.MalformedToolFeedback)
	}
}

func TestProcessToolSievePassesThroughFencedXMLToolCallExamples(t *testing.T) {
	var state State
	input := strings.Join([]string{
		"Before first example.\n```",
		"xml\n<｜DSML｜tool_calls><｜DSML｜invoke name=\"read_file\"><｜DSML｜parameter name=\"path\">README.md</｜DSML｜parameter></｜DSML｜invoke></｜DSML｜tool_calls>\n```\n",
		"Between examples.\n```xml\n",
		"<｜DSML｜tool_calls><｜DSML｜invoke name=\"search\"><｜DSML｜parameter name=\"q\">golang</｜DSML｜parameter></｜DSML｜invoke></｜DSML｜tool_calls>\n",
		"```\nAfter examples.",
	}, "")

	chunks := []string{
		"Before first example.\n```",
		"xml\n<｜DSML｜tool_calls><｜DSML｜invoke name=\"read_file\"><｜DSML｜parameter name=\"path\">README.md</｜DSML｜parameter></｜DSML｜invoke></｜DSML｜tool_calls>\n```\n",
		"Between examples.\n```xml\n",
		"<｜DSML｜tool_calls><｜DSML｜invoke name=\"search\"><｜DSML｜parameter name=\"q\">golang</｜DSML｜parameter></｜DSML｜invoke></｜DSML｜tool_calls>\n",
		"```\nAfter examples.",
	}

	var events []Event
	for _, c := range chunks {
		events = append(events, ProcessChunk(&state, c, []string{"read_file", "search"})...)
	}
	events = append(events, Flush(&state, []string{"read_file", "search"})...)

	var textContent strings.Builder
	toolCalls := 0
	for _, evt := range events {
		if evt.Content != "" {
			textContent.WriteString(evt.Content)
		}
		toolCalls += len(evt.ToolCalls)
	}

	if toolCalls != 0 {
		t.Fatalf("expected fenced XML examples to stay text, got %d tool calls events=%#v", toolCalls, events)
	}
	if textContent.String() != input {
		t.Fatalf("expected fenced XML examples to pass through unchanged, got %q", textContent.String())
	}
}

func TestProcessToolSieveKeepsPartialXMLTagInsideFencedExample(t *testing.T) {
	var state State
	input := strings.Join([]string{
		"Example:\n```xml\n<tool_ca",
		"lls><｜DSML｜invoke name=\"read_file\"><｜DSML｜parameter name=\"path\">README.md</｜DSML｜parameter></｜DSML｜invoke></｜DSML｜tool_calls>\n```\n",
		"Done.",
	}, "")

	chunks := []string{
		"Example:\n```xml\n<tool_ca",
		"lls><｜DSML｜invoke name=\"read_file\"><｜DSML｜parameter name=\"path\">README.md</｜DSML｜parameter></｜DSML｜invoke></｜DSML｜tool_calls>\n```\n",
		"Done.",
	}

	var events []Event
	for _, c := range chunks {
		events = append(events, ProcessChunk(&state, c, []string{"read_file"})...)
	}
	events = append(events, Flush(&state, []string{"read_file"})...)

	var textContent strings.Builder
	toolCalls := 0
	for _, evt := range events {
		if evt.Content != "" {
			textContent.WriteString(evt.Content)
		}
		toolCalls += len(evt.ToolCalls)
	}

	if toolCalls != 0 {
		t.Fatalf("expected partial fenced XML to stay text, got %d tool calls events=%#v", toolCalls, events)
	}
	if textContent.String() != input {
		t.Fatalf("expected partial fenced XML to pass through unchanged, got %q", textContent.String())
	}
}

func TestProcessToolSievePartialXMLTagHeldBack(t *testing.T) {
	var state State
	// Chunk ends with a partial XML tool tag.
	events := ProcessChunk(&state, "Hello <too", []string{"read_file"})

	var textContent string
	for _, evt := range events {
		textContent += evt.Content
	}

	// "Hello " should be emitted, but "<too" should be held back.
	if strings.Contains(textContent, "<too") {
		t.Fatalf("partial XML tag should not be emitted, got %q", textContent)
	}
	if !strings.Contains(textContent, "Hello") {
		t.Fatalf("expected 'Hello' text to be emitted, got %q", textContent)
	}
}

func TestFindToolSegmentStartDetectsXMLToolCalls(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  int
	}{
		{"tool_calls_tag", "some text <｜DSML｜tool_calls>\n", 10},
		{"invoke_tag_missing_wrapper", "some text <｜DSML｜invoke name=\"read_file\">\n", 10},
		{"bare_tool_call_text", "prefix <tool_call>\n", -1},
		{"xml_inside_code_fence", "```xml\n<｜DSML｜tool_calls><｜DSML｜invoke name=\"read_file\"></｜DSML｜invoke></｜DSML｜tool_calls>\n```", -1},
		{"no_xml", "just plain text", -1},
		{"gemini_json_no_detect", `some text {"functionCall":{"name":"search"}}`, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := findToolSegmentStart(nil, tc.input, []string{"read_file"})
			if got != tc.want {
				t.Fatalf("findToolSegmentStart(%q) = %d, want %d", tc.input, got, tc.want)
			}
		})
	}
}

func TestFindPartialXMLToolTagStart(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  int
	}{
		{"partial_tool_calls", "Hello <tool_ca", 6},
		{"partial_invoke", "Hello <inv", 6},
		{"bare_tool_call_not_held", "Hello <tool_name", -1},
		{"partial_lt_only", "Text <", 5},
		{"partial_generic_dsml_prefix", "Hello <DSML|too", 6},
		{"partial_hash_dsml_prefix", "Hello <#DS", 6},
		{"partial_begin_invoke_prefix", "Hello <｜begin▁of▁inv", 6},
		{"complete_tag", "Text <｜DSML｜tool_calls>done", -1},
		{"no_lt", "plain text", -1},
		{"closed_lt", "a < b > c", -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := findPartialXMLToolTagStart(tc.input)
			if got != tc.want {
				t.Fatalf("findPartialXMLToolTagStart(%q) = %d, want %d", tc.input, got, tc.want)
			}
		})
	}
}

func TestHasOpenXMLToolTag(t *testing.T) {
	if !hasOpenXMLToolTag("<｜DSML｜tool_calls>\n<｜DSML｜invoke name=\"foo\">") {
		t.Fatal("should detect open XML tool tag without closing tag")
	}
	if hasOpenXMLToolTag("<｜DSML｜tool_calls>\n<｜DSML｜invoke name=\"foo\"></｜DSML｜invoke>\n</｜DSML｜tool_calls>") {
		t.Fatal("should return false when closing tag is present")
	}
	if hasOpenXMLToolTag("plain text without any XML") {
		t.Fatal("should return false for plain text")
	}
}

// Test the EXACT scenario the user reports: token-by-token streaming where
// <｜DSML｜tool_calls> tag arrives in small pieces.
func TestProcessToolSieveTokenByTokenXMLNoLeak(t *testing.T) {
	var state State
	// Simulate DeepSeek model generating tokens one at a time.
	chunks := []string{
		"<",
		"｜DSML｜tool",
		"_ca",
		"lls",
		">\n",
		"  <｜DSML｜in",
		"voke",
		` name="`,
		"read",
		"_file",
		`">` + "\n",
		"    <｜DSML｜para",
		`meter name="path">`,
		"README.MD",
		"</｜DSML｜parameter>\n",
		"  </｜DSML｜invoke>\n",
		"</",
		"｜DSML｜tool_calls",
		">",
	}
	var events []Event
	for _, c := range chunks {
		events = append(events, ProcessChunk(&state, c, []string{"read_file"})...)
	}
	events = append(events, Flush(&state, []string{"read_file"})...)

	var textContent string
	var toolCalls int
	for _, evt := range events {
		if evt.Content != "" {
			textContent += evt.Content
		}
		toolCalls += len(evt.ToolCalls)
	}

	if strings.Contains(textContent, "<｜DSML｜invoke ") {
		t.Fatalf("XML tool call content leaked to text in token-by-token mode: %q", textContent)
	}
	if strings.Contains(textContent, "tool_calls>") {
		t.Fatalf("closing tag fragment leaked to text: %q", textContent)
	}
	if strings.Contains(textContent, "read_file") {
		t.Fatalf("tool name leaked to text: %q", textContent)
	}
	if toolCalls == 0 {
		t.Fatal("expected tool calls to be extracted, got none")
	}
}

func TestFlushToolSieveIncompleteXMLHiddenForRetry(t *testing.T) {
	var state State
	// XML block starts but stream ends before completion.
	chunks := []string{
		"<｜DSML｜tool_calls>\n",
		"  <｜DSML｜invoke name=\"read_file\">\n",
	}
	var events []Event
	for _, c := range chunks {
		events = append(events, ProcessChunk(&state, c, []string{"read_file"})...)
	}
	events = append(events, Flush(&state, []string{"read_file"})...)

	var textContent string
	for _, evt := range events {
		if evt.Content != "" {
			textContent += evt.Content
		}
	}

	if textContent != "" || !strings.Contains(state.MalformedToolFeedback, "<｜DSML｜tool_calls>") {
		t.Fatalf("expected incomplete XML to be hidden for retry, text=%q feedback=%q", textContent, state.MalformedToolFeedback)
	}
}

// Test that the opening tag "<｜DSML｜tool_calls>\n  " is NOT emitted as text content.
func TestOpeningXMLTagNotLeakedAsContent(t *testing.T) {
	var state State
	// First chunk is the opening tag - should be held, not emitted.
	evts1 := ProcessChunk(&state, "<｜DSML｜tool_calls>\n  ", []string{"read_file"})
	for _, evt := range evts1 {
		if strings.Contains(evt.Content, "<｜DSML｜tool_calls>") {
			t.Fatalf("opening tag leaked on first chunk: %q", evt.Content)
		}
	}

	// Remaining content arrives.
	evts2 := ProcessChunk(&state, "<｜DSML｜invoke name=\"read_file\">\n    <｜DSML｜parameter name=\"path\">README.MD</｜DSML｜parameter>\n  </｜DSML｜invoke>\n</｜DSML｜tool_calls>", []string{"read_file"})
	evts2 = append(evts2, Flush(&state, []string{"read_file"})...)

	var textContent string
	var toolCalls int
	allEvents := append(evts1, evts2...)
	for _, evt := range allEvents {
		if evt.Content != "" {
			textContent += evt.Content
		}
		toolCalls += len(evt.ToolCalls)
	}

	if strings.Contains(textContent, "<｜DSML｜invoke ") {
		t.Fatalf("XML content leaked: %q", textContent)
	}
	if toolCalls == 0 {
		t.Fatal("expected tool calls to be extracted")
	}
}

func TestProcessToolSieveFallsBackToRawAttemptCompletion(t *testing.T) {
	var state State
	// Simulate an agent outputting attempt_completion XML tag.
	// If it does not parse as a tool call, it should fall back to raw text.
	chunks := []string{
		"Done with task.\n",
		"<attempt_completion>\n",
		"  <result>Here is the answer</result>\n",
		"</attempt_completion>",
	}
	var events []Event
	for _, c := range chunks {
		events = append(events, ProcessChunk(&state, c, []string{"attempt_completion"})...)
	}
	events = append(events, Flush(&state, []string{"attempt_completion"})...)

	var textContent string
	for _, evt := range events {
		if evt.Content != "" {
			textContent += evt.Content
		}
	}

	if !strings.Contains(textContent, "Done with task.\n") {
		t.Fatalf("expected leading text to be emitted, got %q", textContent)
	}

	if textContent != strings.Join(chunks, "") {
		t.Fatalf("expected agent XML to fall back to raw text, got %q", textContent)
	}
}

func TestProcessToolSievePassesThroughBareToolCallAsText(t *testing.T) {
	var state State
	chunk := `<｜DSML｜invoke name="read_file"><｜DSML｜parameter name="path">README.md</｜DSML｜parameter></｜DSML｜invoke>`
	events := ProcessChunk(&state, chunk, []string{"read_file"})
	events = append(events, Flush(&state, []string{"read_file"})...)

	var textContent strings.Builder
	toolCalls := 0
	for _, evt := range events {
		textContent.WriteString(evt.Content)
		toolCalls += len(evt.ToolCalls)
	}

	if toolCalls != 0 {
		t.Fatalf("expected bare invoke to remain text, got %d events=%#v", toolCalls, events)
	}
	if textContent.String() != chunk {
		t.Fatalf("expected bare invoke to pass through unchanged, got %q", textContent.String())
	}
}

func TestProcessToolSieveBareInvokeInlineProseDoesNotStall(t *testing.T) {
	var state State
	chunk := "Use `<｜DSML｜invoke name=\"read_file\">` as plain documentation text."
	events := ProcessChunk(&state, chunk, []string{"read_file"})

	var textContent strings.Builder
	toolCalls := 0
	for _, evt := range events {
		textContent.WriteString(evt.Content)
		toolCalls += len(evt.ToolCalls)
	}

	if toolCalls != 0 {
		t.Fatalf("expected inline invoke prose to remain text, got %d events=%#v", toolCalls, events)
	}
	if textContent.String() != "" {
		t.Fatalf("expected inline invoke prose to be hidden for retry, got %q", textContent.String())
	}
	if state.MalformedToolFeedback == "" {
		t.Fatal("expected inline invoke prose to retain malformed feedback")
	}
}

func TestProcessToolSieveBareInvokeExampleHiddenWhenNotRepairable(t *testing.T) {
	var state State
	chunks := []string{
		`Example: <｜DSML｜invoke name="read_file"><｜DSML｜parameter name="path">README.md</｜DSML｜parameter>`,
		"</｜DSML｜invoke> then continue.",
	}
	var events []Event
	for _, c := range chunks {
		events = append(events, ProcessChunk(&state, c, []string{"read_file"})...)
	}

	for _, evt := range events {
		if evt.Content != "" || len(evt.ToolCalls) > 0 {
			t.Fatalf("expected non-repairable bare invoke to be hidden for retry, got %#v", events)
		}
	}
	if state.capturing {
		t.Fatal("expected non-repairable bare invoke not to leave stream capture open")
	}
	if !strings.Contains(state.MalformedToolFeedback, `<｜DSML｜invoke name="read_file">`) || !strings.Contains(state.MalformedToolFeedback, `</｜DSML｜invoke>`) {
		t.Fatalf("expected non-repairable bare invoke feedback to be retained, got %q", state.MalformedToolFeedback)
	}
}

func TestProcessToolSieveRepairsMissingOpeningWrapperWithoutLeakingInvokeText(t *testing.T) {
	var state State
	chunks := []string{
		"<｜DSML｜invoke name=\"read_file\">\n",
		"  <｜DSML｜parameter name=\"path\">README.md</｜DSML｜parameter>\n",
		"</｜DSML｜invoke>\n",
		"</｜DSML｜tool_calls>",
	}
	var events []Event
	for _, c := range chunks {
		events = append(events, ProcessChunk(&state, c, []string{"read_file"})...)
	}
	events = append(events, Flush(&state, []string{"read_file"})...)

	var textContent strings.Builder
	toolCalls := 0
	for _, evt := range events {
		textContent.WriteString(evt.Content)
		toolCalls += len(evt.ToolCalls)
	}

	if toolCalls != 0 {
		t.Fatalf("expected missing-wrapper stream to be rejected, got %d events=%#v", toolCalls, events)
	}
	if textContent.Len() != 0 || state.MalformedToolFeedback == "" {
		t.Fatalf("expected missing-wrapper stream to be hidden for retry, text=%q feedback=%q", textContent.String(), state.MalformedToolFeedback)
	}
}

// Test fullwidth pipe variant: <｜DSML｜tool_calls> (U+FF5C) should be buffered and parsed.
func TestProcessToolSieveFullwidthPipeVariantDoesNotLeak(t *testing.T) {
	var state State
	chunks := []string{
		"<｜DSML｜tool_calls>\n",
		"<｜DSML｜invoke name=\"execute_command\">\n",
		"<｜DSML｜parameter name=\"command\">git status</｜DSML｜parameter>\n",
		"</｜DSML｜invoke>\n",
		"</｜DSML｜tool_calls>",
	}
	var events []Event
	for _, c := range chunks {
		events = append(events, ProcessChunk(&state, c, []string{"execute_command"})...)
	}
	events = append(events, Flush(&state, []string{"execute_command"})...)

	var textContent string
	var toolCalls int
	for _, evt := range events {
		textContent += evt.Content
		toolCalls += len(evt.ToolCalls)
	}

	if strings.Contains(textContent, "invoke") || strings.Contains(textContent, "execute_command") {
		t.Fatalf("fullwidth pipe variant leaked to text: %q", textContent)
	}
	if toolCalls != 1 {
		t.Fatalf("expected one tool call from fullwidth pipe variant, got %d events=%#v", toolCalls, events)
	}
}

// Test <｜DSML｜tool_calls> with DSML invoke/parameter tags should buffer the
// wrapper instead of leaking it before the block is complete.
func TestProcessToolSieveFullwidthDSMLPrefixVariantDoesNotLeak(t *testing.T) {
	var state State
	chunks := []string{
		"<｜DSML｜tool",
		"_calls>\n",
		"<｜DSML｜invoke name=\"Bash\">\n",
		"<｜DSML｜parameter name=\"command\"><![CDATA[ls -la /Users/aq/Desktop/myproject/ds2api/]]></｜DSML｜parameter>\n",
		"<｜DSML｜parameter name=\"description\"><![CDATA[List project root contents]]></｜DSML｜parameter>\n",
		"</｜DSML｜invoke>\n",
		"<｜DSML｜invoke name=\"Bash\">\n",
		"<｜DSML｜parameter name=\"command\"><![CDATA[cat /Users/aq/Desktop/myproject/ds2api/package.json 2>/dev/null || echo \"No package.json found\"]]></｜DSML｜parameter>\n",
		"<｜DSML｜parameter name=\"description\"><![CDATA[Check for existing package.json]]></｜DSML｜parameter>\n",
		"</｜DSML｜invoke>\n",
		"</｜DSML｜tool_calls>",
	}
	var events []Event
	for _, c := range chunks {
		events = append(events, ProcessChunk(&state, c, []string{"Bash"})...)
	}
	events = append(events, Flush(&state, []string{"Bash"})...)

	var textContent strings.Builder
	var toolCalls int
	var names []string
	for _, evt := range events {
		textContent.WriteString(evt.Content)
		for _, call := range evt.ToolCalls {
			toolCalls++
			names = append(names, call.Name)
		}
	}

	if toolCalls != 2 {
		t.Fatalf("expected two tool calls from fullwidth DSML prefix variant, got %d events=%#v", toolCalls, events)
	}
	if len(names) != 2 || names[0] != "Bash" || names[1] != "Bash" {
		t.Fatalf("expected two Bash tool calls, got %v", names)
	}
	if textContent.Len() != 0 {
		t.Fatalf("expected fullwidth DSML prefix variant not to leak text, got %q", textContent.String())
	}
}

// Test <｜DSML｜tool_calls> with <|DSML|invoke> (DSML prefix without leading pipe on wrapper).
func TestProcessToolSieveDSMLPrefixVariantDoesNotLeak(t *testing.T) {
	var state State
	chunks := []string{
		"<｜DSML｜tool_calls>\n",
		"  <｜DSML｜invoke name=\"execute_command\">\n",
		"    <｜DSML｜parameter name=\"command\"><![CDATA[git status]]></｜DSML｜parameter>\n",
		"  </｜DSML｜invoke>\n",
		"</｜DSML｜tool_calls>",
	}
	var events []Event
	for _, c := range chunks {
		events = append(events, ProcessChunk(&state, c, []string{"execute_command"})...)
	}
	events = append(events, Flush(&state, []string{"execute_command"})...)

	var textContent string
	var toolCalls int
	for _, evt := range events {
		textContent += evt.Content
		toolCalls += len(evt.ToolCalls)
	}

	if strings.Contains(strings.ToLower(textContent), "dsml") || strings.Contains(textContent, "execute_command") {
		t.Fatalf("DSML prefix variant leaked to text: %q", textContent)
	}
	if toolCalls != 1 {
		t.Fatalf("expected one tool call from DSML prefix variant, got %d events=%#v", toolCalls, events)
	}
}

// Test <｜DSML｜tool_calls> with <DSML|invoke> (no pipe anywhere) should be buffered and parsed.
func TestProcessToolSieveDSMLBarePrefixVariantDoesNotLeak(t *testing.T) {
	var state State
	chunks := []string{
		"<｜DSML｜tool_calls>\n",
		"<｜DSML｜invoke name=\"execute_command\">\n",
		"<｜DSML｜parameter name=\"command\"><![CDATA[git status]]></｜DSML｜parameter>\n",
		"</｜DSML｜invoke>\n",
		"</｜DSML｜tool_calls>",
	}
	var events []Event
	for _, c := range chunks {
		events = append(events, ProcessChunk(&state, c, []string{"execute_command"})...)
	}
	events = append(events, Flush(&state, []string{"execute_command"})...)

	var textContent string
	var toolCalls int
	for _, evt := range events {
		textContent += evt.Content
		toolCalls += len(evt.ToolCalls)
	}

	if strings.Contains(strings.ToLower(textContent), "dsml") || strings.Contains(textContent, "execute_command") {
		t.Fatalf("DSML bare prefix variant leaked to text: %q", textContent)
	}
	if toolCalls != 1 {
		t.Fatalf("expected one tool call from DSML bare prefix variant, got %d events=%#v", toolCalls, events)
	}
}
