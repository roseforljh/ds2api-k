# ds2api 优化计划

## 概览

| # | 等级 | 模块 | 问题 | 改动量 | 风险 |
|---|------|------|------|--------|------|
| 1 | P0 | config/store | 写锁持有期间执行磁盘 I/O | 小 | 低 |
| 2 | P0 | account/pool | bumpQueue O(n) 线性移位 | 小 | 低 |
| 3 | P1 | config/models | 模型别名关键词启发式误匹配 | 中 | 中 |
| 4 | P1 | config/store | Token 清除逻辑与 Save 重复 | 小 | 低 |
| 5 | P2 | chat/handler | Vercel 路由判断混入核心函数 | 小 | 中 |
| 6 | P2 | deepseek/client | proxyClients 缓存无清理 | 小 | 低 |
| 7 | P2 | chat/handler | ChatCompletions 单函数职责过多 | 中 | 中 |
| 8 | P3 | chat/runtime | sendChunk 静默丢弃序列化错误 | 小 | 低 |
| 9 | P3 | account/pool | Waiter 队列无界增长 | 小 | 低 |

---

## 1. [P0] Store 写锁持有期间执行磁盘 I/O

**文件**: `internal/config/store.go`

**现状** (line 253-269):
```go
func (s *Store) saveLocked() error {
    // 调用者持有 s.mu.Lock()
    persistCfg := s.cfg.Clone()
    persistCfg.ClearAccountTokens()
    b, err := json.MarshalIndent(persistCfg, "", "  ")
    // ...
    writeConfigBytes(s.path, b)
    // ...
}
```

`saveLocked` 的 4 个调用点全部在 `s.mu.Lock()` 之后：
- `Update()` (line 219)
- `Replace()` (line 210)
- `UpdateAccountToken()` (line 186)
- `Save()` (line 233)

每次 Token 刷新或 WebUI 改配置，JSON 序列化 + 文件写入在锁内完成，阻塞所有读者的 `RLock()`。

**影响量化**:
- JSON 序列化：取决于 config 大小，通常 <1ms
- 文件写入（含 fsync）：SSD 上 1-5ms，HDD 上 5-20ms
- 在此期间所有 API 读取被阻塞（每个请求至少读 1 次 config）

**方案**: 将序列化 + 文件写入移到锁外

实施步骤：
1. `saveLocked` 拆分为两个函数：`prepareSaveLocked()` 返回待写入数据，`commitSave()` 执行 I/O
2. 修改 `Update`、`Replace`、`UpdateAccountToken`：
   - 锁内：修改 `s.cfg` + 调用 `prepareSaveLocked()` 获取 `[]byte`
   - 释放锁
   - 锁外：`writeConfigBytes(s.path, data)`
3. `Save()` 已经自己获取锁，调整内部调用即可
4. 恢复失败处理：锁外 I/O 失败时，仅打日志（config 已在内存生效，下次 Save 时会重写）

**验证**:
- 运行 `go test ./internal/config/... -v`
- 手动：启动服务，WebUI 修改配置，同时发 API 请求，确认无阻塞

---

## 2. [P0] bumpQueue O(n) 线性移位

**文件**: `internal/account/pool_acquire.go:83-92`

**现状**:
```go
func (p *Pool) bumpQueue(accountID string) {
    for i, id := range p.queue {        // O(n) 查找
        if id != accountID { continue }
        p.queue = append(p.queue[:i], p.queue[i+1:]...)  // O(n) 移位
        p.queue = append(p.queue, accountID)
        return
    }
}
```

每次 `Acquire`（包括 `tryAcquire` 和 `acquireLocked`）都调用一次。对 N 个账号，每次获取都是 O(n) 时间 + O(n) 内存分配。

**影响量化**:
- 10 账号：不可感知
- 100 账号：每次 Acquire 约 200ns slice 操作
- 1000 账号：每次 Acquire 约 2μs，高并发下可累积

**方案**: 用索引指针循环代替物理移位

实施步骤：
1. 在 `Pool` 结构体中新增 `nextIdx int` 字段
2. 删除 `bumpQueue` 方法
3. 修改 `tryAcquire`：从 `nextIdx` 开始循环遍历 queue，而不是每次从 0 开始
4. 被选中的账号 index 记录为新的 `nextIdx`
5. 修改 `acquireLocked`（target 指定场景）：不调 bumpQueue，只增加 inUse 计数
6. `Reset()` 时重置 `nextIdx = 0`

**备选方案**: `container/ring` 标准库，但需要额外维护 ring 和 map 的索引关系，代码复杂度略高。

**验证**:
- 运行 `go test ./internal/account/... -v`
- 额外测试：100 账号并发场景，确认轮转均匀性

---

## 3. [P1] 模型别名关键词启发式匹配

**文件**: `internal/config/models.go:351-374`

**现状**:
```go
useVision  := strings.Contains(model, "vision")
useReasoner := strings.Contains(model, "reason") ||
    strings.Contains(model, "reasoner") ||
    strings.HasPrefix(model, "o1") || strings.HasPrefix(model, "o3") ||
    strings.Contains(model, "opus") || strings.Contains(model, "slow") ||
    strings.Contains(model, "r1")
useSearch  := strings.Contains(model, "search")
```

**潜在错误映射**:
- `gemini-3.1-flash-search` 匹配 `useSearch=true` → `deepseek-v4-flash-search`（正确）
- `gemini-3.1-flash-reasoning-search` 匹配 `useReasoner=true, useSearch=true` → `deepseek-v4-pro-search`（可能错误）
- 未来新模型 `o4-mini-vision` 会匹配 `o1/o3` 前缀不匹配 → `useReasoner=false` → 落到 default `deepseek-v4-flash`（可能非用户预期）

**根因**: 这段 fallback 逻辑在精确别名匹配失败后才触发（`resolveCanonicalModel`），但 `strings.Contains` 匹配过于宽泛。

**方案**: 收紧关键词匹配边界，仅在已知前缀下生效

实施步骤：
1. 将 `useVision` 限定在 `gemini-` 前缀下
2. 将 `useReasoner` 限定在 `o1`/`o3`/`o4` 前缀 + `r1` + `opus` 关键词（opus 只在 `claude-` 前缀下）
3. 将 `useSearch` 限定在 `gemini-` 前缀下
4. 删除 `slow` 关键词（过于宽泛）
5. 对于不匹配任何已知前缀的模型名，返回 error 而非默认 fallback

**验证**:
- 运行 `go test ./internal/config/... -v -run Model`
- 新增测试用例覆盖边界：`gemini-3.1-flash-reasoning-search`、`o4-mini-vision`、`claude-haiku-4-5-search`

---

## 4. [P1] Token 清除逻辑与 Save 重复

**文件**: `internal/config/store.go`

**现状**: `Save()` (line 233) 和 `saveLocked()` (line 253) 是两段几乎相同的代码：
```go
func (s *Store) Save() error {
    s.mu.Lock()
    defer s.mu.Unlock()
    // ... 同样逻辑 ...
    persistCfg := s.cfg.Clone()
    persistCfg.ClearAccountTokens()
    b, err := json.MarshalIndent(persistCfg, "", "  ")
    // ...
}
```
```go
func (s *Store) saveLocked() error {
    // 调用者持有 s.mu.Lock()
    // ... 同样逻辑 ...
}
```

**方案**: `Save()` 内部直接调用 `saveLocked()`

实施步骤：
1. 将 `Save()` 简化为：`s.mu.Lock(); defer s.mu.Unlock(); return s.saveLocked()`
2. 移除 `Save()` 中的重复代码块
3. `ExportJSONAndBase64()` 保留独立的 `ClearAccountTokens()`（它是读取场景，不写入文件）

**验证**:
- 运行 `go test ./internal/config/... -v`
- `go build ./...` 确认编译通过

---

## 5. [P2] Vercel 路由判断混入核心函数

**文件**: `internal/httpapi/openai/chat/handler_chat.go:22-34`

**现状**:
```go
func (h *Handler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
    if isVercelStreamReleaseRequest(r) { ... }
    if isVercelStreamPowRequest(r) { ... }
    if isVercelStreamPrepareRequest(r) { ... }
    // 核心逻辑
}
```

**方案**: 将 Vercel 专属路由在 `router.go` 中单独注册

实施步骤：
1. 在 `Handler` 上暴露三个公开方法：`HandleVercelStreamRelease`、`HandleVercelStreamPow`、`HandleVercelStreamPrepare`
2. 在 `router.go` 中注册 `/v1/chat/completions/{stream,release,pow,prepare}` 等路径到对应方法
3. 从 `ChatCompletions` 中删除 3 个 if 分支
4. 确认 Vercel 部署使用的路径格式，保持兼容

**验证**:
- 确认 Vercel 端路径格式后验证路由正确注册
- 运行 `go build ./...`

---

## 6. [P2] proxyClients 缓存无清理

**文件**: `internal/deepseek/client/client_core.go:30,43`

**现状**:
```go
type Client struct {
    proxyClientsMu sync.RWMutex
    proxyClients   map[string]requestClients  // 永不清除
    // ...
}
```

**方案**: 在 `Pool.Reset()` 时同步清理已不存在的账号对应的 proxy client

实施步骤：
1. 在 `Client` 上新增 `PruneProxyClients(activeIDs map[string]bool)` 方法
2. 在 `Pool.Reset()` 末尾（或 `server.NewApp()` 中 Pool 初始化后）调用清理
3. 清理逻辑：遍历 `proxyClients`，删除不在 `activeIDs` 中的 key

**验证**:
- 运行 `go test ./internal/deepseek/client/... -v`
- 手动：添加账号 → 发起请求 → 删除账号 → 确认 proxy client 已清除

---

## 7. [P2] ChatCompletions 单函数职责过多

**文件**: `internal/httpapi/openai/chat/handler_chat.go:22-130`

**现状**: 一个 110 行的函数承担 10+ 个独立关注点。

**方案**: 逐步拆分，不改行为

分三步：

**Phase A**: 提取 Vercel 路由（见 #5）

**Phase B**: 提取请求预处理为独立方法
```go
func (h *Handler) prepareChatRequest(r *http.Request) (*auth.RequestAuth, map[string]any, promptcompat.StandardRequest, error)
```
包含：Body 解析、文件预处理、Prompt 标准化、CurrentInputFile 注入

**Phase C**: 提取核心执行流程
```go
func (h *Handler) executeChat(ctx context.Context, a *auth.RequestAuth, stdReq promptcompat.StandardRequest) (*chatResult, error)
```
包含：Session 创建、PoW 获取、Completion 调用

`ChatCompletions` 最终保留：Auth + Prepare + Execute + Format 四步。

**验证**:
- 运行 `go test ./internal/httpapi/openai/chat/... -v`
- 运行 `go build ./...`

---

## 8. [P3] sendChunk 静默丢弃序列化错误

**文件**: `internal/httpapi/openai/chat/chat_stream_runtime.go:99`

**现状**:
```go
func (s *chatStreamRuntime) sendChunk(v any) {
    b, _ := json.Marshal(v)
    _, _ = s.w.Write([]byte("data: "))
    _, _ = s.w.Write(b)
    // ...
}
```

**方案**: 序列化失败时记录日志

实施步骤：
1. 将 `b, _ := json.Marshal(v)` 改为 `b, err := json.Marshal(v)`
2. 如果 `err != nil`，记录 `config.Logger.Error` 并写入一个 error chunk 到 SSE 流
3. 继续 `sendDone()` 结束流

**验证**:
- 运行 `go build ./...`
- 运行 `go test ./internal/httpapi/openai/chat/... -v -run Stream`

---

## 9. [P3] Waiter 队列无界

**文件**: `internal/account/pool_acquire.go:15-47`

**现状**:
```go
waiter := make(chan struct{})
p.waiters = append(p.waiters, waiter)  // 无上限
```

`maxQueueSize` 已在 `canQueueLocked` 中使用，但只影响"是否允许新请求入队"的判断，不影响 waiter channel 本身的 append。

**方案**: append waiter 之前用 `maxQueueSize` 限制

实施步骤：
1. 在 `AcquireWait` 中，append waiter 之前检查 `len(p.waiters) >= p.maxQueueSize`
2. 若已满，直接返回 `(Account{}, false)` 而非排队

实际上当前逻辑是：`canQueueLocked` 返回 false → 直接返回 false，不会走到 append。所以只有 `canQueueLocked` 返回 true 时才 append，理论上受 `maxQueueSize` 限制。

**重新审视**: `canQueueLocked` 的实现需要验证是否覆盖了 waiter 队列长度检查。如果已覆盖，此项降级为"已实现，无需改动"。

**验证**: 阅读 `pool_limits.go` 中的 `canQueueLocked` 实现，确认是否包含 `len(p.waiters) >= maxQueueSize` 检查。

---

## 执行顺序

```
Sprint 1（本周）
  ├── #1  Store 写锁 I/O（P0）
  ├── #2  bumpQueue O(n)（P0）
  ├── #4  Token 清除重复（P1）
  └── #8  sendChunk 错误日志（P3）

Sprint 2（下周）
  ├── #3  模型别名关键词（P1）
  ├── #5  Vercel 路由分离（P2）
  ├── #6  proxyClients 清理（P2）
  └── #9  Waiter 队列确认（P3）

Sprint 3（后续）
  └── #7  ChatCompletions 拆分（P2）
```

Sprint 1 四条改动量极小且互不依赖，可以一次性提交。
Sprint 2 涉及行为变更，需要更充分的测试。
Sprint 3 是纯重构，不急于做。
