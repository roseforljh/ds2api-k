# Prompt Compatibility

本文档记录“API 结构化请求 -> DeepSeek 网页纯文本上下文 -> API 结构化响应”的兼容流程。

## 主链路

OpenAI、Claude、Gemini 请求先归一成内部 `StandardRequest`，再由 `promptcompat` 渲染成 DeepSeek 网页聊天可见的纯文本 prompt。

核心顺序：

1. 读取 API 消息、工具、thinking、search、文件引用等字段。
2. 归一化消息角色和内容块。
3. 注入工具调用说明。
4. 渲染最终 `FinalPrompt`。
5. 调 DeepSeek completion。
6. 将 DeepSeek 文本或 SSE 输出还原成 OpenAI `tool_calls`、Claude `tool_use`、Gemini `functionCall`。

## 工具提示格式

工具调用提示使用 DeepSeek V4 官方 DSML 风格：

```xml
<｜DSML｜tool_calls>
  <｜DSML｜invoke name="TOOL_NAME">
    <｜DSML｜parameter name="PARAMETER_NAME" string="true">raw text</｜DSML｜parameter>
    <｜DSML｜parameter name="JSON_PARAMETER" string="false">{"field":"value"}</｜DSML｜parameter>
  </｜DSML｜invoke>
</｜DSML｜tool_calls>
```

规则：

- 字符串参数使用 `string="true"`，正文按原始文本处理。
- 数字、布尔、数组、对象、null 使用 `string="false"`，正文必须是合法 JSON。
- 工具标签优先使用官方全角分隔符 `｜DSML｜`。
- Markdown fence 内的工具调用示例不得触发真实工具调用。
- 模型输出工具调用时，工具块前不得混入解释、角色标记或内部思考。

## 输出解析

输出侧由 `internal/toolcall` 解析工具调用文本。

解析策略：

1. 优先识别官方 `<｜DSML｜tool_calls>` 快路径。
2. `string="true"` 保留原始字符串，不把 JSON 形状字符串误解析成对象。
3. `string="false"` 严格要求合法 JSON，非法值直接拒绝。
4. 工具名必须匹配本次请求可用工具名，匹配大小写不敏感。
5. 未知工具名会进入 policy rejection，不还原为客户端工具调用。
6. legacy DSML/XML 变体仅作为兼容兜底。

## 流式边界兜底

流式层由 `internal/toolstream` 和各协议 runtime 负责防止半截工具标签泄漏。

要求：

- 半截官方 DSML 标签必须暂存，例如 `<｜DS`、`<｜DSML`、`<｜DSML｜par`。
- 完整官方 DSML 工具块优先于 legacy XML 工具块。
- 非法官方 DSML 不得作为普通文本输出，也不得触发工具执行。
- OpenAI Chat 流式输出转成 `tool_calls`。
- OpenAI Responses 流式输出转成 `function_call` item。
- Claude 流式输出转成 `tool_use` block。
- Gemini 原生流式兜底转成 `functionCall` part。

## 异常处理

当模型输出看起来像工具调用但不满足协议时：

- 不把原始 DSML/XML 泄漏给客户端。
- 不执行不完整或非法工具调用。
- 将原始异常工具文本记录到 malformed feedback，供上层重试或错误处理使用。

典型拒绝场景：

- `string="false"` 里是 `{bad json}`。
- 工具名不在当前请求 `tools` 列表。
- 工具调用标签写在 fenced code block 示例中。
- 混用 `<|DSML|`、`<|DSML｜` 等非官方分隔符。
