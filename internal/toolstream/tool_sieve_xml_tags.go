package toolstream

import "regexp"

// --- XML tool call support for the streaming sieve ---

//nolint:unused // kept as explicit tag inventory for future XML sieve refinements.
var xmlToolCallClosingTags = []string{"</tool_calls>", "</|dsml|tool_calls>", "</|dsmltool_calls>", "</|dsml tool_calls>", "</dsml|tool_calls>", "</dsmltool_calls>", "</dsml tool_calls>", "</｜tool_calls>", "</|tool_calls>"}

// xmlToolCallBlockPattern matches a complete canonical XML tool call block.
//
//nolint:unused // reserved for future fast-path XML block detection.
var xmlToolCallBlockPattern = regexp.MustCompile(`(?is)((?:<tool_calls\b|<\|dsml\|tool_calls\b)[^>]*>\s*(?:.*?)\s*(?:</tool_calls>|</\|dsml\|tool_calls>))`)

// xmlToolTagsToDetect is the set of XML tag prefixes used by findToolSegmentStart.
var xmlToolTagsToDetect = []string{
	"<#dsml#tool_calls>", "<#dsml#tool_calls\n", "<#dsml#tool_calls ",
	"<#dsml#invoke ", "<#dsml#invoke\n", "<#dsml#invoke\t", "<#dsml#invoke\r",
	"<#dsm#tool_calls>", "<#dsm#tool_calls\n", "<#dsm#tool_calls ",
	"<#dsm#invoke ", "<#dsm#invoke\n", "<#dsm#invoke\t", "<#dsm#invoke\r",
	"<⌜dsml⌝tool_calls>", "<⌜dsml⌝tool_calls\n", "<⌜dsml⌝tool_calls ",
	"<⌜dsml⌝invoke ", "<⌜dsml⌝invoke\n", "<⌜dsml⌝invoke\t", "<⌜dsml⌝invoke\r",
	"<⌜dsm⌝tool_calls>", "<⌜dsm⌝tool_calls\n", "<⌜dsm⌝tool_calls ",
	"<⌜dsm⌝invoke ", "<⌜dsm⌝invoke\n", "<⌜dsm⌝invoke\t", "<⌜dsm⌝invoke\r",
	"<|dsml|tool_calls>", "<|dsml|tool_calls\n", "<|dsml|tool_calls ",
	"<｜dsml|tool_calls>", "<｜dsml|tool_calls\n", "<｜dsml|tool_calls ",
	"<|dsml｜tool_calls>", "<|dsml｜tool_calls\n", "<|dsml｜tool_calls ",
	"<｜dsml｜tool_calls>", "<｜dsml｜tool_calls\n", "<｜dsml｜tool_calls ",
	"<|dsml|invoke ", "<|dsml|invoke\n", "<|dsml|invoke\t", "<|dsml|invoke\r",
	"<|dsml｜invoke ", "<|dsml｜invoke\n", "<|dsml｜invoke\t", "<|dsml｜invoke\r",
	"<｜dsml|invoke ", "<｜dsml|invoke\n", "<｜dsml|invoke\t", "<｜dsml|invoke\r",
	"<｜dsml｜invoke ", "<｜dsml｜invoke\n", "<｜dsml｜invoke\t", "<｜dsml｜invoke\r",
	"<|dsmltool_calls>", "<|dsmltool_calls\n", "<|dsmltool_calls ",
	"<|dsmlinvoke ", "<|dsmlinvoke\n", "<|dsmlinvoke\t", "<|dsmlinvoke\r",
	"<|dsml tool_calls>", "<|dsml tool_calls\n", "<|dsml tool_calls ",
	"<|dsml invoke ", "<|dsml invoke\n", "<|dsml invoke\t", "<|dsml invoke\r",
	"<dsml|tool_calls>", "<dsml|tool_calls\n", "<dsml|tool_calls ",
	"<dsml｜tool_calls>", "<dsml｜tool_calls\n", "<dsml｜tool_calls ",
	"<dsml|invoke ", "<dsml|invoke\n", "<dsml|invoke\t", "<dsml|invoke\r",
	"<dsml｜invoke ", "<dsml｜invoke\n", "<dsml｜invoke\t", "<dsml｜invoke\r",
	"<dsmltool_calls>", "<dsmltool_calls\n", "<dsmltool_calls ",
	"<dsmlinvoke ", "<dsmlinvoke\n", "<dsmlinvoke\t", "<dsmlinvoke\r",
	"<dsml tool_calls>", "<dsml tool_calls\n", "<dsml tool_calls ",
	"<dsml invoke ", "<dsml invoke\n", "<dsml invoke\t", "<dsml invoke\r",
	"<｜tool_calls>", "<｜tool_calls\n", "<｜tool_calls ",
	"<｜invoke ", "<｜invoke\n", "<｜invoke\t", "<｜invoke\r",
	"<|tool_calls>", "<|tool_calls\n", "<|tool_calls ",
	"<|invoke ", "<|invoke\n", "<|invoke\t", "<|invoke\r",
	"<tool_calls>", "<tool_calls\n", "<tool_calls ", "<invoke ", "<invoke\n", "<invoke\t", "<invoke\r",
}
