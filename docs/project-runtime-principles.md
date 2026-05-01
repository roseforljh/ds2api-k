# ds2api 项目基本运行原理

本文先讲清楚这个项目怎么跑起来、请求怎么流动、兼容层到底在做什么。重点不是继续补正则，而是把链路拆开，看清楚哪些是主流程，哪些只是兜底。

## 1. 一句话理解

`ds2api` 是一个把 OpenAI、Claude、Gemini 风格 API 请求接进来，再转换成 DeepSeek 网页聊天接口请求的代理服务。

它不是简单转发 JSON。核心动作是：

1. 接收不同协议的 API 请求。
2. 做鉴权，选择 DeepSeek token 或配置里的账号。
3. 把不同协议的消息、工具、thinking、search 等字段归一成内部标准请求。
4. 把标准请求渲染成 DeepSeek 网页聊天能理解的纯文本 prompt。
5. 调 DeepSeek 的 session、PoW、completion 接口。
6. 把 DeepSeek SSE 输出再包装回 OpenAI、Claude 或 Gemini 的响应格式。

## 2. 启动链路

本地入口是 `cmd/ds2api/main.go`。

启动时大致做这些事：

1. `config.LoadDotEnv()` 读取 `.env`。
2. `config.RefreshLogger()` 根据配置刷新日志。
3. `webui.EnsureBuiltOnStartup()` 确保 WebUI 静态产物可用。
4. `server.NewApp()` 创建完整服务实例。
5. 监听 `0.0.0.0:${PORT}`，默认端口是 `5001`。
6. 收到中断信号后最多等待 10 秒优雅退出。


## 3. 服务装配链路

核心装配在 `internal/server/router.go`。

`server.NewApp()` 做了几件关键事情：

1. `config.LoadStoreWithError()` 加载 `config.json` 或环境变量配置。
2. `account.NewPool(store)` 创建账号池。
3. `auth.NewResolver(...)` 创建鉴权和账号选择器。
4. `dsclient.NewClient(store, resolver)` 创建 DeepSeek 客户端。
5. `dsClient.PreloadPow(...)` 预热 PoW 求解器。
6. 创建 OpenAI、Claude、Gemini、Admin、WebUI handler。
7. 用 `chi` 注册路由。

主要路由包括：

- `GET /v1/models`
- `POST /v1/chat/completions`
- `POST /v1/responses`
- `GET /v1/responses/{response_id}`
- `POST /v1/files`
- `POST /v1/embeddings`
- Claude 兼容路由，由 `internal/httpapi/claude` 注册
- Gemini 兼容路由，由 `internal/httpapi/gemini` 注册
- `/admin/*`
- WebUI 路由

## 4. 配置加载原理

配置入口是 `internal/config/store.go`。

加载优先级是：

1. 先看 `DS2API_CONFIG_JSON`。
2. 再看配置文件路径，默认是 `config.json`。

`Store` 不是单纯保存配置，它还维护了索引：

- `keyMap` 用来快速判断 API key 是否存在。
- `accMap` 用来快速按账号标识找账号。
- `accTest` 保存运行时账号测试状态。

模型别名和 DeepSeek 模型类型映射主要在 `internal/config/models.go`。

## 5. OpenAI Chat 主链路

主入口是 `internal/httpapi/openai/chat/handler_chat.go` 的 `ChatCompletions`。

请求进入后按这个顺序执行：

1. `h.Auth.Determine(r)` 做鉴权，决定本次用哪个 DeepSeek token 或配置账号。
2. 读取 JSON body。
3. `preprocessInlineFileInputs` 处理内联文件输入。
4. `promptcompat.NormalizeOpenAIChatRequest(...)` 把请求转成 `StandardRequest`。
5. `applyCurrentInputFile(...)` 按配置决定是否把长历史整理成 `上下文.txt` 上传给 DeepSeek。
6. `h.DS.CreateSession(...)` 创建 DeepSeek 会话。
7. `h.DS.GetPow(...)` 获取 PoW。
8. `stdReq.CompletionPayload(sessionID)` 生成 DeepSeek completion payload。
9. `h.DS.CallCompletion(...)` 调 DeepSeek 网页接口。
10. 根据 `stream` 选择流式或非流式输出。

这里的关键是 `StandardRequest`。它定义在 `internal/promptcompat/standard_request.go`，里面有：

- 原始模型和解析后的 DeepSeek 模型
- 归一化后的消息
- 工具定义
- 工具选择策略
- 最终 prompt
- thinking/search 开关
- ref file ids
- 透传字段

`CompletionPayload` 最终会把 `FinalPrompt` 放进 DeepSeek payload 的 `prompt` 字段。

## 6. Claude 和 Gemini 不是独立主链路

现在的 Claude/Gemini 兼容层大多先转成 OpenAI Chat，再复用 OpenAI Chat 主链路。

Claude 入口：

- `internal/httpapi/claude/handler_messages.go`
- `internal/httpapi/claude/standard_request.go`

Gemini 入口：

- `internal/httpapi/gemini/handler_generate.go`
- `internal/httpapi/gemini/convert_request.go`

它们的共同点是：

1. 读取原始 Claude/Gemini 请求。
2. 用 `translatorcliproxy.ToOpenAI(...)` 转成 OpenAI Chat 请求。
3. 修改 thinking、model、stream 等兼容字段。
4. 内部调用 `OpenAI.ChatCompletions(...)`。
5. 非流式响应再用 `translatorcliproxy.FromOpenAINonStream(...)` 转回目标协议。
6. 流式响应用 `translatorcliproxy.NewOpenAIStreamTranslatorWriter(...)` 边收 OpenAI SSE 边转目标协议 SSE。

所以调试 Claude/Gemini 问题时，要先判断问题发生在三层中的哪一层：

1. Claude/Gemini 原始请求转 OpenAI。
2. OpenAI Chat 主链路转 DeepSeek。
3. OpenAI 响应转回 Claude/Gemini。

## 7. promptcompat 是兼容层的核心

`internal/promptcompat` 的职责是把 API 结构化消息变成 DeepSeek 网页聊天能吃的 prompt。

关键文件：

- `request_normalize.go`：把 OpenAI Chat 请求转成标准请求。
- `standard_request.go`：定义内部标准请求和 DeepSeek completion payload。
- `message_normalize.go`：归一化 OpenAI 消息。
- `prompt_build.go`：组合消息归一化、工具提示注入、最终 prompt 渲染。
- `tool_prompt.go`：生成工具调用说明，要求模型按 DSML 输出工具调用。
- `history_transcript.go`：把长历史整理成当前请求上下文文件。
- `internal_tool_event_filter.go`：过滤 prompt 中可见的内部工具事件和 UI 噪声。

主流程是：

```text
API JSON messages
  -> NormalizeOpenAIMessagesForPrompt
  -> injectToolPrompt
  -> prompt.MessagesPrepareWithThinking
  -> FinalPrompt
  -> DeepSeek completion payload.prompt
```

如果启用了当前输入文件机制，长历史会变成：

```text
历史消息
  -> BuildOpenAICurrentInputContextTranscript
  -> 上传为 上下文.txt
  -> prompt 变成“最新上下文已经做成文件发你了”
  -> ref_file_ids 带上文件 id
```

## 8. 工具调用链路

工具调用分输入侧和输出侧。

输入侧：

1. API 请求带 `tools` 或 Claude/Gemini 对应工具字段。
2. 兼容层抽取工具名和 schema。
3. `tool_prompt.go` 把工具定义写进 prompt。
4. prompt 要求模型用 DSML 形式输出工具调用。

输出侧：

1. DeepSeek 返回的是文本或 SSE。
2. 流式 runtime 先判断输出像不像工具调用。
3. 对有工具的请求，通常先 buffer，不急着把正文吐给客户端。
4. finalize 阶段用 `internal/toolcall` 解析 DSML。
5. 再转成 OpenAI tool_calls、Claude tool_use、Gemini functionCall。

这也是为什么工具相关 bug 容易出现在流式路径。流式时模型可能只吐出半个标签，runtime 需要避免把半截 `<tool_calls>` 泄漏给客户端。

## 9. 流式响应链路

通用 SSE 消费器是 `internal/stream/engine.go` 的 `ConsumeSSE`。

它负责：

- 从 DeepSeek SSE body 读取解析后的行。
- 处理 context cancelled。
- 处理 keepalive。
- 处理无内容超时和 idle timeout。
- 在上游结束或 handler 要求停止时调用 finalize。

DeepSeek SSE 的解析和收集在：

- `internal/sse/parser.go`
- `internal/sse/consumer.go`

OpenAI Chat 流式输出在：

- `internal/httpapi/openai/chat/chat_stream_runtime.go`

Claude 旧的原生流式 runtime 在：

- `internal/httpapi/claude/stream_runtime_core.go`
- `internal/httpapi/claude/stream_runtime_finalize.go`

Gemini 旧的原生流式 runtime 在：

- `internal/httpapi/gemini/handler_stream_runtime.go`

当前 Claude/Gemini 主路径更多依赖 `internal/translatorcliproxy/stream_writer.go` 做 OpenAI SSE 到目标协议 SSE 的翻译。

## 10. 为什么一直加正则是被动修复

项目里确实有不少正则和字符串兜底，集中在：

- `internal/promptcompat/internal_tool_event_filter.go`
- `internal/promptcompat/history_transcript.go`
- `internal/toolcall/*`
- `internal/toolstream/*`
- 各协议的 output clean / sanitize 逻辑

这些正则在处理什么？

它们主要是在已经变成纯文本的内容里猜测：

- 哪些是用户真正说的话。
- 哪些是助手真正回答。
- 哪些是工具调用结构。
- 哪些是工具结果。
- 哪些是 Droid 或终端 UI 的内部事件。
- 哪些是模型输出的畸形 DSML。

问题根因是语义边界丢失。

原本 API 里消息、工具调用、工具结果、内部事件都有结构化边界。进入 DeepSeek 网页 prompt 前，如果这些东西被压平成普通文本，后面只能靠正则猜。每多一种 UI 形态、多一种标签变体、多一种截断方式，就会多一个正则。

所以正则只能当最后一道防线，不能当主设计。

正确理解应该是：

1. 主链路负责保留语义边界：协议请求归一化、工具定义归一化、历史状态归一化。
2. promptcompat 负责把结构化语义渲染成可控 prompt。
3. current input file 负责把长历史变成明确的工作状态和完整时间线。
4. stream runtime 负责在输出侧识别和还原工具调用。
5. 正则只兜底已经泄漏进纯文本的历史 UI 噪声和畸形标签。

## 11. 看问题时的定位顺序

后面再遇到“加一个正则修一下”的问题，先按这个顺序定位：

1. 请求从哪个协议入口进来：OpenAI、Claude、Gemini 还是 Responses。
2. 进入 OpenAI Chat 主链路前，原始请求有没有被正确转换。
3. `StandardRequest` 里的 `Messages`、`ToolsRaw`、`ToolChoice`、`FinalPrompt` 是否正确。
4. 是否启用了 current input file，历史是否被整理成 `上下文.txt`。
5. 内部工具事件是在进入 prompt 前就被混进文本，还是输出时才泄漏。
6. DeepSeek 返回的是普通正文、合法 DSML、半截 DSML，还是畸形工具文本。
7. 最终响应转换发生在 OpenAI runtime，还是 Claude/Gemini translator。

能在第 2 到第 4 步解决的，就不应该拖到第 7 步靠正则擦屁股。

## 12. 一张总流程图

```text
客户端
  |
  | OpenAI / Claude / Gemini / Responses API
  v
HTTP handler
  |
  | 鉴权、选择账号、解析请求
  v
协议转换层
  |
  | Claude/Gemini 多数先转成 OpenAI Chat
  v
promptcompat
  |
  | 消息归一化、工具提示注入、历史整理、生成 FinalPrompt
  v
DeepSeek Client
  |
  | CreateSession -> GetPow -> CallCompletion
  v
DeepSeek 网页聊天 SSE
  |
  | SSE parser / stream engine
  v
响应 runtime / translator
  |
  | OpenAI chunks / Claude events / Gemini chunks
  v
客户端
```

## 13. 关键结论

这个项目的基本盘是“协议适配 + prompt 渲染 + SSE 翻译”。

正则不是主链路。它只是为了在语义边界已经丢失时兜底。如果想减少被动修复，排查时要先看结构化数据在哪一步被压成了不可区分的文本，再看是不是应该在归一化、历史整理、工具状态或流式 runtime 层修，而不是继续给最终输出补 pattern。
