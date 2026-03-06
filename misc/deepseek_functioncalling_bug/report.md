# DeepSeek Function Calling 缺陷分析与 ds2api 的增强修复策略

> **相关 PR**: #74 (代码核心实现) 与 #75 (Merge to dev)
> **问题背景**: 解决因包括 DeepSeek 在内的部分模型在函数调用（Function Calling/Tool Call）表现不够“规范”，从而导致工具调用失败的问题。

## 一、底层架构对比：为什么会产生 Function Calling 缺陷？

在探讨缺陷前，我们需要理解两种 Function Calling 的底层结构差异：

### 1. OpenAI 的原生结构化返回 (API 级分离)
在 OpenAI 的规范中，**聊天文字与工具调用是在底层的 JSON 结构中被硬性拆分的**：
* 聊天废话存放在 `response.choices[0].message.content` 里。
* 工具请求存放在单独的数组 `response.choices[0].message.tool_calls` 里。

**优势：** 这种设计对客户端极其友好。客户端只需判断 `tool_calls` 是否为空，就能决定是执行代码还是渲染文字。它支持同时并发多个工具请求，且底层的生成殷勤被严格训练和约束，极少抛出语法错误的 JSON。

### 2. DeepSeek 等模型的“单文本流”机制
相比之下，部分未经深度专门微调的模型（或者在特定的通信适配层中），它们依然倾向于把一切内容打包成一个纯文本流吐出。这就是为什么它们的输出往往不仅包含了本该属于 `tool_calls` 结构里的 JSON，还会像个“老实人”一样夹杂了属于 `content` 里的散文。

---

## 二、DeepSeek 在 Function Calling 上的特定缺陷表现

相比于 OpenAI 严格遵循 API 约定的原生结构，DeepSeek 等开源/国产推理模型在工具调用时，经常会暴露出以下三种典型的“不守规矩”的输出行为：

### 1. 混合输出：散文文本与工具 JSON 混杂 (Mixed Prose Streams)
当应用要求模型直接返回工具请求时，DeepSeek 有时候会**“忍不住想和用户搭话”**。
它常常前置一段解释性废话，中间插入工具调用的 JSON 参数，并在末尾再补上一句总结：
```text
好的，我这就帮你读取 README.md 的内容：
{"tool_calls":[{"name":"read_file","input":{"path":"README.md"}}]}
请稍等片刻，我马上把它读出来。
```
**旧版系统痛点：**
原有的代码存在**严格模式（Strict Mode）**校验：
```go
// 如果解析到的 JSON 块前后存在任何非空字符串，就放弃当作工具调用！
if strings.TrimSpace(state.recentTextTail) != "" || strings.TrimSpace(prefixPart) != "" ... {
    return captured, nil, "", true
}
```
这直接导致上述结构被网关认定是一段“普通聊天”，直接原封不动地返回给用户，这直接干挂了后续的工具自动执行流程。

### 2. 工具名格式幻觉：擅自修改或前缀化工具名称
由于 DeepSeek 的预训练数据中有大量的代码和不同的平台结构，它在回复工具名称时，常常无法忠实于 System Prompt 中提供的纯命名（也就是 `name: "read_file"`），而是加上前缀或者拼写变形，例如：
* `{"name": "mcp.search_web"}` （自带命名空间）
* `{"name": "tools.read_file"}`
* `{"name": "search-web"}` （下划线变成了中划线）

**旧版系统痛点：**
旧版系统对于工具名的匹配几乎只有“绝对相等”的字典级比对，只要差了一个字符或加了前缀，就会由于找不到合法工具而直接失败。

### 3. Role 角色的非标准返回
在部分工具通信流的响应中，返回的内容其所属的 `role` 没有被标准化处理，可能携带意料之外的属性，或是与下游严格比对出现冲突。

---

## 二、PR #74 的代码增强修复方案

为了解决大模型这种自身的不规范行为，PR #74 在系统的中间层网关联入了一个**极其包容的容错引擎**。它并不强制要求模型“改过自新”，而是主动做了以下三块增强：

### 1. 从流中分离混合内容（废除 Strict Mode）
修改了 `internal/adapter/openai/tool_sieve_core.go`。
取消了前后包裹文本的拦截逻辑。当系统扫描到流式结构中有完整的 `{"tool_calls":...}` 时，它会将废话和 JSON 分发到不同的事件流中：
```go
if prefix != "" {
    // 将前面的“好的，帮你读文件”剥离出来作为常规文本输出
    state.noteText(prefix)
    events = append(events, toolStreamEvent{Content: prefix})
}
// 捕获并拦截中间的工具请求，进行背后执行
state.pendingToolCalls = calls
```
**效果：** 用户的屏幕上只能看到正常的文字交流，而后端的工具也会立刻挂载。

### 2. 多级宽容匹配引擎 (Resolve Allowed Tool Name)
在 `internal/util/toolcalls_parse.go` 中，新增了一个由严到松降级匹配的强大漏斗策略函数 `resolveAllowedToolName`：

1. **绝对匹配**：和以前一样，`read_file` == `read_file`。
2. **忽略大小写**：`Read_File` 算作合法。
3. **命名空间抹除**：通过寻找最后一个 `.` 来剥离前缀，强制将 `mcp.search_web` 还原出真实的 `search_web`。
4. **终极正则清洗**：
   引入 `var toolNameLoosePattern = regexp.MustCompile(`[^a-z0-9]+`)`。
   这个正则剥离了字符串里所有的符号、空格、格式符。
   将传入的 `read-file` 洗除符号成为 `readfile`，并去和系统中所有合法工具同样清洗后的版本进行比较。只要核心字母一致，即算作匹配成功。

### 3. Role 归一化 (Normalize OpenAIRoleForPrompt)
在 `internal/adapter/openai/responses_input_items.go` 等处，引入了特定的 `normalizeOpenAIRoleForPrompt(role)` 清洗，保证输入和传递给上游的 Role 枚举始终受控，消除了因为意外的身份字段传参崩溃。

---

## 报告总结与 tool_sieve 的本质作用

PR #74 / #75 并没有从模型本身开刀，而是基于**网关应足够健壮**的设计哲学。

**其实整个增强实现，本质上实现了一个名为 `tool_sieve` (工具筛子) 的中间层网关。**
面对 DeepSeek 这种吐出一团混合了聊天文字与 JSON 面团的“不标准”数据流，`tool_sieve` 就像一个勤劳的高精度筛子，不仅人工揉开了面团：
1. 它把散文分拣出来，塞回标准结构的 `content` 字段去展示；
2. 剥离并清洗出有瑕疵的 JSON 块，按照 OpenAI 的标准格式小心翼翼地放进 `tool_calls` 结构里去等待执行。

这意味着，即便 AI 被配置了奇怪的回复设定、加粗了强调语言，甚至是犯了标点符号拼写小失误，**只要它输出了可以拼凑成工具指令的 JSON 核心单元，整个中继层就能将其挽救，并把正确的工具结果呈现给模型和用户**。 这不仅修复了缺陷，更极大地增强了工具网关的通用性和鲁棒性。
