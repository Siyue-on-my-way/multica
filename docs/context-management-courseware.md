# Multica 上下文管理：一次 run 的生命周期

这份课件以 SIY-121 的一次真实运行作为样本，解释 Multica 如何把业务事件、Issue 数据、运行时 Brief、任务 Prompt、祖先快照、Provider 原生会话和 handoff 状态组合成一次 agent run。代码引用均相对于仓库根目录；本地源码当前有其他未提交改动，本课件只新增文档，不覆盖这些改动。

## 0. 先回答用户的假设

用户的判断“第一次打开 agent 可以塞入大量上下文，后续聊天主要依赖 runtime 自己维护上下文；切换 agent 相当于新窗口，需要重新注入上下文”，**方向正确，但需要加上三个边界**：

1. Multica 不是只在第一次注入。每个任务都会重新生成当前任务的 Prompt，并重新 claim/enrich；变化的触发信息、模式、回复目标、评论增量和连续性声明不能只依赖 Provider 历史。
2. “后续复用”是有条件的。Issue 任务按 agent + issue 查询最近会话；只有 session、runtime、workdir、rollout 等条件都通过，才会真正 resume。workdir 可复用不等于 Provider 会话一定可恢复。
3. agent 切换后，通常不会把旧 agent 的 Provider transcript 直接接给新 agent。新 agent 会得到 Issue、祖先背景、项目上下文和上一个 agent 写入的 handoff checkpoint，再按读取协议补齐评论和状态。

因此，准确的心智模型是：

> Provider session 负责低延迟的原生对话连续性；Multica 的 Issue、评论、metadata、handoff、Brief 和 sidecar 负责可迁移、可审计的业务连续性。两者互补，任何一个都不是全部真相。

### 四种常见情形

| 情形 | Provider session | Workdir | 当前 run 仍会注入什么 |
| --- | --- | --- | --- |
| 同一 agent、同一 Issue、同一 runtime，且 rollout 存在 | 尝试 resume | 尝试 reuse | 当前 Task Prompt、模式、触发信息、评论读取规则 |
| 同一 agent、同一 Issue，但 runtime 换了 | 通常不能跨 runtime resume | 可能有共享路径，但仍需校验 | 同上；必要时从平台状态重建 |
| agent 切换 | 按新 agent 隔离查询，通常 fresh | 准备新环境或安全复用 | Issue、祖先快照、项目、handoff、Brief、历史读取协议 |
| resume 失败、rollout 丢失或 session poisoned | fresh retry | 重新准备或重新挂载 | 明确的上下文丢失声明，并要求重新读取 Issue/thread |

这也是“直接在 runtime 上聊天”和“用 Multica 管理任务”的核心区别：直接聊天主要把连续性托付给 Provider 进程；Multica 把连续性拆成 Provider transcript 加平台持久化表示，并且每次任务都重新建立边界、身份和操作协议。

## 1. 四层上下文模型

Multica 的上下文不是一个无限增长的字符串，而是按变化频率和迁移能力拆成四层。

| 层 | 主要内容 | 变化频率 | 典型载体 | 设计目标 |
| --- | --- | --- | --- | --- |
| L1 运行时 Brief | agent 身份、运行时安全、CLI、Issue workflow、技能索引、项目说明、Provider 适配 | 低到中 | AGENTS.md、CLAUDE.md、QWEN.md 等 | 稳定、可发现、可缓存 |
| L2 当前任务 Prompt | task kind、Ownership/Reply/Chat 模式、当前触发 comment、handoff 开场、回复父 ID | 每次 run | Provider 的当前 user message | 精确描述这一轮要做什么 |
| L3 ancestor / 业务背景 | 父 Issue 的标题/描述、祖先 refs、项目资源、压缩后的 handoff | Issue 变化时 | Task payload、Prompt、sidecar | agent 切换后仍可迁移 |
| L4 历史与运行状态 | 评论线程、metadata、Provider session、rollout、workdir、task result | 持续变化 | Multica DB、Provider store、文件系统 | 恢复、审计、故障诊断 |

这四层有两个重要性质：

- L1 尽量稳定，不能把每轮都变化的 initiator、连续性声明和 connected apps 塞进缓存前缀。
- L2 到 L4 不是互相替代。Prompt 只携带当前回合的强提示；历史仍应通过有预算的 CLI 读取；Provider transcript 断了以后，handoff 和 Issue 数据才是迁移的基础。

### 1.1 L1：Brief 是运行时契约，不是某个 Issue 的完整 transcript

execenv 组装 Brief 时，按 task kind 做裁剪。源码中的矩阵见 server/internal/daemon/execenv/runtime_config_sections.go:703-798：

- comment、assignment、autopilot、chat 保留完整的可用命令和技能信息；
- quick-create 保留最小创建协议，不注入不存在的 Issue 历史；
- Comment Formatting、Mentions、Attachments、Issue Metadata 等只进入需要它们的 Issue kind；
- Header、Background Task Safety、Agent Identity、Requesting User、Workspace Context、Workflow、Always Use CLI、Output 等公共段按条件发出；
- Session Continuity Notice、Task Initiator、Connected Apps 是每轮变化的数据，已从稳定 Brief 前缀移到当前 Prompt 尾部，以保持 prompt-cache 前缀稳定。

对于当前 Codex run，daemon 日志显示 inline_system_prompt=false，Provider 通过工作目录中的 AGENTS.md 发现 Brief；Brief 的具体脱敏副本在 context-run-dump/brief.AGENTS.md。

### 1.2 L2：Task Prompt 是每次任务重新计算的“这次要做什么”

server/internal/daemon/prompt.go:61-78 的 BuildPrompt 先按 task kind 选择主体，再把 per-turn blocks 追加到末尾。主要分支是：

- assignment / ownership：说明 Issue、祖先和 checkpoint，并要求遵循 Ownership 状态弧；
- comment / mention：写入 Turn mode: Reply、当前 comment 及回复父 ID；
- chat：写入聊天消息和频道语义；
- autopilot：写入 autopilot 配置和触发负载；
- quick-create：明确没有现存 Issue，并只允许一次创建调用。

评论分支会将触发内容直接嵌入：

<code>[NEW COMMENT] A user just left a new comment...</code>

这不是所有 run 都有的固定头。没有 TriggerCommentID 的 assignment run 不会产生它；把这个标记硬塞进本次 capture 会让数据失真。

perTurnContextBlocks 还会追加：

- <code>## Session Continuity Notice</code>：预期 resume 但 Provider transcript 无法恢复时；
- <code>## Task Initiator</code>：发起者归因，与 runtime owner 区分；
- connected apps：按本次任务的 runtime MCP overlay 注入。

源码在 server/internal/daemon/prompt.go:38-78，连续性文字在 server/internal/daemon/execenv/runtime_config_sections.go:399-409。

### 1.3 L3：ancestor 是可迁移的背景快照

server/internal/service/ancestor_brief.go:43-91 从当前 Issue 的 parent 开始向根 Issue 走：

- 每个祖先只取 ID、标题和 Markdown description；
- 每段前加 <code>[Background source: Issue X]</code>；
- 返回 refs，记录祖先 ID 和 updated_at；
- 正文不是永久存储的 transcript，而是在 claim 时按当前 Issue 重新生成；
- 祖先正文带有明确的“background reference only; current task instructions take precedence”语义。

本样本的 SIY-121 parent 是 SIY-59；脱敏 Task payload 中可见对应的 <code>[Background source: Issue 79f4d8ef-62a7-4cdc-837b-ecbd6c08fa73]</code> 快照。若祖先后来更新，下一次 claim 会用新的 updated_at/ref 重新构建；这比把旧 Prompt 原样转交给新 agent 更诚实。

### 1.4 L4：历史和运行状态要区分“事实源”

有四类状态经常被混淆：

- Issue / comment / metadata：业务事实和用户意图，agent 可以用 multica CLI 重新读取；
- handoff_summary：上一个 agent 主动留下的可迁移 checkpoint，适合说明已完成、下一步和未解决问题；
- Provider session / rollout：原生对话上下文，恢复快，但受 agent、runtime、账号、存储和 Provider 规则限制；
- workdir / sidecar：代码、未提交变更、AGENTS.md、.agent_context/issue_context.md 等运行环境状态。workdir 不是自动可信的历史库，仍要经过 reuse gate。

## 2. 从事件到结果的完整链路

### 2.1 事件、队列、claim

不同入口只是启动 run 的方式不同，之后都收敛到 Task：

- assignment：server/internal/service/task.go:944-1093；
- comment / mention：server/internal/service/task.go:1096-1211；
- chat：server/internal/service/task.go:1499-1617；
- webhook、schedule、manual 等 autopilot 入口也最终形成可 claim 的队列项。

一个简化的端到端图如下。图示技能 next-ai-drawio 的外部预览服务在本次环境中不可达，因此课件内保留仓库可直接渲染的 Mermaid 降级图；节点与源码链路保持一致。

~~~~mermaid
flowchart LR
  E[事件: assignment / comment / chat / autopilot] --> Q[enqueue Task]
  Q --> C[claim and enrich]
  C --> T[Task wire payload]
  T --> G{continuity gates}
  G -->|same agent + issue + runtime + rollout| R[reuse workdir and resume]
  G -->|switch / fresh / failed resume| F[prepare fresh environment]
  R --> X[execenv]
  F --> X
  X --> B[Brief file: AGENTS.md / CLAUDE.md / QWEN.md]
  X --> S[sidecar: issue_context.md and resources]
  X --> P[BuildPrompt]
  B --> V[Provider session]
  P --> V
  V --> O[result and usage]
  O --> H[finalize task and optional handoff]
  H --> I[Issue/comment/status/metadata]
~~~~

### 2.2 claim 如何丰富 Task

server/internal/handler/daemon.go:1646-2140 负责把队列行变成 daemon 可执行的 Task response，关键动作包括：

1. 读取 agent 名称、instructions、skills、custom args 和 MCP 配置；
2. 读取 Issue 的 workspace、title、description、project resources；
3. 运行 BuildAncestorBrief，填充 ancestor_brief 和 ancestor_brief_refs；
4. 读取上一 agent 写入的 WorkingBranch、AgentStatus、HandoffSummary；
5. comment task 只将成功加载、预算允许的 comment 写入 trigger/coalesced 字段；
6. 对 Issue task 按 agent + issue 查最近 session/workdir；
7. 对 chat task 按 chat session 和 runtime 校验 session 指针；
8. 返回 task-scoped AuthToken；真实 token 不进入本课件 dump。

这一步是“平台感知”的中心：daemon 不需要猜任务类型，也不需要从一个过时文件推断用户刚刚说了什么。

### 2.3 execenv 准备和 Provider 调用

server/internal/daemon/daemon.go:4898-5205 将 Task 转成 execenv.TaskContextForEnv：

- 记录 Issue、trigger、comment reply targets、new comment count、PriorSessionResumed；
- 传递 agent、project、resource、handoff、initiator 和 workspace context；
- shouldReusePriorWorkdir 通过后调用 reuseExecutionEnvironment，否则 Prepare；
- gateResumeToReusedWorkdir 检查 workdir 不能单独证明 Codex transcript 存在；
- execenv.InjectRuntimeConfig 写入 Brief；
- writeContextFiles 写入 .agent_context/issue_context.md、.multica/daemon_task_context.json 和项目资源 sidecar；
- 最后才 BuildPrompt，并把 Prompt 与 ExecOptions.ResumeSessionID 交给 Provider。

server/internal/daemon/daemon.go:5210-5558 再负责 Provider 启动和恢复：

- Codex session store 按 agent + issue 隔离；
- ResumeSessionID 只有通过前置 gates 才会交给 backend；
- current Prompt 的 prompt_bytes、mcp_config、inline_system_prompt、resume_session 等会写入 daemon log；
- resume 失败时清空 resume 参数，重新注入 cold Brief，使用 freshSessionRetryPrompt，并把“之前的 Provider context 不可用”明确写给 agent；
- 结果中的 session id、workdir、usage 和 error 会进入 task result / history。

## 3. ancestor 的预算和优先级

### 3.1 8192 token 是独立预算

ancestor_brief.go:13-19 定义：

- AncestorBriefMaxTokens = 8192；
- 混合 Markdown/CJK 用约 4 个字符估算 1 token；
- 最大 ancestor 字符预算约为 32768 个 rune；
- 另有最大深度 32，防止 parent 环或异常树无限遍历。

当加入下一段会超出预算时，代码只保留能放下的前缀，标记 truncated=true，并把被截断的祖先 ID 放进 truncated_ids；已保留的正文末尾带 <code>[truncated]</code>。因此“尽可能多上下文”是有边界的，且边界可解释、可观测。

### 3.2 优先级不是“越老越权威”

可以采用下面的阅读优先级：

1. Provider / Multica 的全局安全和 Agent Identity 约束；
2. 当前 run 的 Turn mode、当前 comment、handoff 指令和结果交付规则；
3. 当前 Issue 的直接描述和本轮 task 字段；
4. ancestor 的 <code>[Background source: Issue X]</code> 内容；
5. Provider 旧 transcript、旧 workdir 中的不确定状态。

ancestor 只是背景，不能覆盖当前 task instructions。旧 transcript 也不能覆盖新 comment：comment Prompt 会重新生成 “Focus on THIS comment” 保护。遇到冲突时，回到 Issue、comment 和 metadata 读取事实，而不是选择看起来更长的一段文本。

## 4. session 连续性、切换 agent 和上下文丢失

### 4.1 同 agent follow-up

Issue 的 resume 查询由 server/pkg/db/queries/agent.sql:732-844 支撑，关键键是 agent + issue。正常 follow-up 的 claim 会写入：

- PriorSessionID：上一个可安全复用的 Provider session；
- PriorWorkDir：上一次 run 的 workdir；
- PriorSessionResumeUnavailable：最近一次 Codex rollout 缺失时的连续性缺口信号。

chat 使用 server/pkg/db/queries/chat.sql:465-510 的 session 指针和 task fallback，但也要求 runtime 相容。manual rerun 还会优先指向用户选中的 source task，避免同一 Issue 的并行任务互相抢 transcript。

### 4.2 agent 切换不是简单复制窗口

切换 agent 后，agent 维度改变，旧 agent 的 GetLastTaskSession 结果不会被新 agent 当成自己的 Provider session。新 run 仍然有：

- 当前 Issue 的 title/description；
- ancestor refs 和重新生成的 ancestor brief；
- project context / resources；
- 上一个 agent 写入的 handoff_summary；
- 新 agent 自己的 Brief、skills、身份和 token；
- 通过 CLI 读取 comment threads、metadata 和 result 的协议。

这解释了为什么 agent 切换后仍能继续业务，却不等于“新 agent 记得旧 agent 的每一句对话”。迁移的是可验证的业务表示，不是未经校验的内存印象。

### 4.3 runtime、workdir、rollout 的三重限制

- **runtime 限制**：Issue session resume 要求 prior task 的 runtime 与当前 runtime 匹配；仅有相同 Issue 不够。
- **workdir 限制**：workdir 丢失、归属不可确认、local_directory 模式或复用 gate 失败时，必须 Prepare；workdir reuse 不是 transcript resume。
- **rollout 限制**：Codex 的 rollout 不在当前 task CODEX_HOME 时，daemon 会丢弃 resume；最近一轮丢失但存在更老 fallback 时仍设置不可用声明。
- **Provider 限制**：账号不匹配、session poisoned、400 invalid request 等均可能让 resume 失败。

fresh retry 的原则是先承认状态丢失，再重建。源码中的固定文案是：

<code>## Session Continuity Notice</code>

<code>This run was meant to continue an earlier conversation, but that session's context could NOT be restored...</code>

它要求 agent 在回复开头用一句话告知用户旧对话不可用。这是“诚实声明”而不是静默降级，防止 agent 假装记得未恢复的历史。

### 4.4 handoff 是业务层的迁移协议

上一 agent 可以通过 Issue update 写下：

- working_branch；
- agent_status；
- handoff_summary.current_progress；
- handoff_summary.next_steps；
- handoff_summary.unresolved_issues。

新 run 将它注入 issue_context.md 和 assignment Prompt。handoff 不能替代用户评论或代码事实，所以仍要按读取协议复核；它的价值是把“下一步怎么接”从 Provider transcript 提炼成跨 agent 可读的 checkpoint。

## 5. 多 Provider 的 Brief、sidecar 和缓存稳定性

### 5.1 文件投递映射

server/internal/daemon/execenv/runtime_config.go:156-209 的映射是：

| Provider | Brief 文件 |
| --- | --- |
| Claude | CLAUDE.md |
| CodeBuddy | CODEBUDDY.md |
| Qwen | QWEN.md |
| Codex、Copilot、OpenCode、DevEco、OpenClaw、Hermes、Pi、Cursor、Kimi、Kiro、Antigravity、Qoder、Traecli、Grok | AGENTS.md |
| 未知 Provider | 不写文件，退回 Prompt-only |

写入不是盲目覆盖：如果用户已有 AGENTS.md/CLAUDE.md，daemon 追加带 marker 的 managed block；重复任务只替换 managed block；cleanup 会按原始字节恢复。local_directory 任务完成后还会清理 sidecar，避免用户随后手动运行 Provider 时读到旧 Issue 和旧回复规则。

### 5.2 sidecar 的职责

server/internal/daemon/execenv/context.go:121-316：

- .agent_context/issue_context.md：assignment、触发方式和上一个 agent checkpoint；
- .multica/daemon_task_context.json：最小的 agent/issue 任务身份标记；
- .multica/project/resources.json：项目资源的 JSON 表示；
- skills：按 Provider 写到原生技能目录；Codex 通过 per-task CODEX_HOME 管理；
- sidecar manifest：记录创建的文件，local_directory cleanup 时只删除 daemon 自己创建的内容。

sidecar 是“可见的执行辅助状态”，不是替代平台数据库的秘密缓存。当前真实 run 的三个 sidecar 已被列在 context-run-dump/README.md。

### 5.3 task-kind 裁剪与 prompt-cache

runtime_config_sections.go:707-727 给出 Section × Kind 矩阵。裁剪有两个效果：

1. quick-create 不会因为默认 Issue workflow 变得很长，也不会误以为已有 Issue；
2. comment/assignment/chat 各自得到真正需要的回复、状态或聊天协议。

缓存稳定性则要求把每轮变化的字段放在 Prompt 尾部。prompt.go:38-78 明确说明，把 initiator、continuity notice、connected apps 放入 Brief 前缀会让每次 resume 都失去缓存；现在它们追加在稳定前缀后，只让本轮变化付 token 成本。

## 6. “带预算的读取协议”如何代替无限塞历史

Multica 的做法不是把全部评论和 transcript 一次性塞进 Prompt，而是把最小必要的入口和有边界的读取动作写给 agent：

- assignment / cold start：先 issue get，再 roots-only summary，最后只展开相关 thread；
- comment trigger：当前触发 comment 直接嵌入，使用 trigger thread 和 parent id；若有 coalesced comments，逐条按 thread id 处理；
- warm comment：NewCommentsSince / NewCommentCount 提供候选窗口；since 不是保证，仍要逐个 comment id 用 thread tail 校验；
- 历史过长：使用分页 cursor，不用未限定的全量拉取；
- Agent 自己的旧结果：通过 Issue runs、run messages、metadata 和 handoff 读取，而不是假设内存还在。

因此 “后续聊天不用再塞很多上下文”只在 Provider session 正常续接时成立；即便正常续接，当前 comment、模式和增量仍必须每轮重新注入；如果续接失败，读取协议是恢复业务上下文的兜底。

## 7. 本次真实 run 的观测

本次 capture 是一次 assignment/Ownership run，不是 comment trigger：

- Task：0f3ea789-bab1-4d74-92f2-6c56384b4bcf；
- Issue：SIY-121；
- Provider：Codex app-server；
- task kind：direct / issue assignment；
- daemon：prompt_bytes=1564、resume_session=false、reuse_workdir=false、trigger_comment_id=""、inline_system_prompt=false、mcp_config=false、repos=0；
- 工作区 Brief：Codex 使用 AGENTS.md，脱敏副本约 17.7 KiB；
- sidecar：.agent_context/issue_context.md、.multica/daemon_task_context.json、.multica/project/resources.json；
- Provider transcript 的 user_message 与 prompt.txt 对应，内容是 assignment wrapper、Ownership 标记、checkpoint 和 mandatory CLI reads；
- 本次原始 Prompt 没有 [NEW COMMENT]。这是正确结果，因为本次没有触发 comment；评论分支和 [NEW COMMENT] 的源码级例子仍在本课件和 task-payload.json 的 marker_examples 中；
- 本次是 agent 切换示例：上一 run 的 checkpoint 通过 handoff/issue_context 进入当前运行，但 daemon 日志明确从 fresh session/workdir 开始。

“真实”在这里指运行时实际经过 daemon 的 Task → claim → execenv → Provider 链路；“脱敏”只移除 token、邮箱、绝对路径和运行环境身份，不把没有发生的 comment 触发伪造成发生过。

## 8. 适合进一步展开的论点

1. **上下文预算**：8192 token ancestor 预算、Prompt 当前轮成本、Provider completion 预算和 comment payload 预算如何协同；应监控截断率而不只监控总 token。
2. **历史读取协议**：roots summary、thread tail、since candidate、per-id 校验的召回率和成本；可把“遗漏评论”做成可测试指标。
3. **持久化表示**：哪些事实放 Issue/metadata，哪些放 handoff，哪些只保留 Provider transcript；如何避免把临时工作目录误当成真相。
4. **缓存稳定性**：稳定 Brief 前缀与动态 Prompt suffix 的拆分，如何降低 resume 的重复输入成本，并避免为缓存而隐藏必要的当前轮信息。
5. **身份归因与权限**：runtime owner、task initiator、agent identity 和 task-scoped token 的分离；agent 可代表谁发言，不等于它拿到了谁的凭据。
6. **脱敏与教学数据**：完整结构比完整秘密更有价值；Task payload 中 auth_token、邮箱、绝对路径、Provider key 都应在导出层标成 [REDACTED]。
7. **故障注入**：删除 rollout、换 runtime、破坏 workdir、让 resume 返回 invalid request，检查是否真正出现 Session Continuity Notice 并触发重新读取。
8. **跨 Provider 对照实验**：比较同一 Task 在 Claude、Codex、Qwen 和只支持 inline prompt 的 Provider 上的 Brief 投递、缓存前缀和 cleanup 行为。

## 9. 阅读源码的最短路线

1. server/internal/daemon/types.go:63-159：Task wire shape；
2. server/internal/service/task.go:944-1211、1499-1617：事件入队；
3. server/internal/handler/daemon.go:1646-2140：claim enrich、ancestor、comment、session lookup；
4. server/internal/service/ancestor_brief.go:13-103：预算和 snapshot；
5. server/internal/daemon/execenv/runtime_config.go:156-379：Provider 文件映射和 marker cleanup；
6. server/internal/daemon/execenv/runtime_config_sections.go:703-798：task-kind Brief；
7. server/internal/daemon/execenv/context.go:121-316、1005-1059：sidecar；
8. server/internal/daemon/prompt.go:24-78、330-391：模式、评论、per-turn context；
9. server/internal/daemon/daemon.go:4898-5205、5210-5558：Prepare/Reuse、Provider、fresh retry；
10. server/pkg/db/queries/agent.sql:732-844、chat.sql:465-510：session 选择。

课件的核心结论只有一句话：

> Multica 的上下文管理不是“第一次注入，后面全靠聊天窗口”，而是“稳定 Brief + 当前 Prompt + 可迁移业务快照 + 有条件的 Provider resume + 可验证的历史读取协议”。

## 10. 关于“切换时压缩上下文并重开会话”的现状与可选改造

用户补充的问题很关键：如果切换 Agent 或 runtime 后效果不理想，压缩器到底交给新 Agent 什么？当前方案并不是“把旧窗口完整复制给新 Agent”，而是几种来源的组合。

### 10.1 当前实现实际传递什么

一次切换或 fresh run 大致会收到：

1. 当前 Issue 的 title 和 description；
2. 从当前 Issue 的 parent 开始到祖先的 title/description 快照，带 <code>[Background source: Issue X]</code>、updated_at refs 和 8192 token 独立预算；
3. 上一个 agent 写在当前 Issue 上的 handoff_summary、working_branch、agent_status；
4. 当前 agent 自己的 Brief、技能、workspace/project context 和 task-scoped 身份；
5. 若本次由 comment 触发，触发 comment 以及预算内的 coalesced comments；assignment run 则没有 [NEW COMMENT]；
6. 评论、metadata、旧结果和其他线程不一定全部进入 Prompt，而是通过 roots summary、thread tail、since candidate 和 per-id 校验协议让新 Agent按需读取；
7. 如果 continuity gate 通过，才额外复用旧 Provider session；切换 agent、runtime 不匹配、rollout 缺失或 resume 失败时，这一项为空，并应出现 Session Continuity Notice。

所以用户的担心有一个准确部分：handoff_summary 是有损压缩，ancestor brief 也只默认覆盖祖先的标题和描述，并不自动包含每个祖先的全部评论、metadata、代码状态和 Provider transcript。当前设计用“可验证的 CLI 读取协议”补偿这部分损失，但它不是“把所有历史打包后绝不遗漏”的保证。

### 10.2 三个可选方案

| 方案 | 传给新 Agent 的内容 | 优点 | 代价和风险 |
| --- | --- | --- | --- |
| A：增强 handoff 压缩 | 祖先链标题/描述、当前 Issue、关键评论摘要、代码状态和待办，带 source ID、时间和省略清单 | 首轮就有较完整的工作地图，切换体验好 | 摘要仍可能错，压缩器会消耗 token；错误摘要若没有 source refs 很难纠正 |
| B：Context manifest + 按需读取 | 首轮传递完整的祖先 refs、评论 IDs、handoff 摘要、优先级和 digest；正文按 must-read 清单由 agent 读取 | 预算可控、可审计、可重放；遗漏可以通过 ID 对账发现 | 首轮行动前多几次 CLI 读取，Provider 能否严格执行协议需要测试 |
| C：完整 fresh snapshot | 祖先链所有标题/描述、当前 Issue、按时间排序的评论全文、metadata、最近结果和代码状态一次打包 | 新 Agent 初始信息最多，适合短链路和人工复核 | Prompt 很大、易过时、重复输入破坏缓存；隐私、权限、截断和“全量也可能遗漏新消息”问题更严重 |

### 10.3 建议先评估的方向

在用户选择之前，不应直接改成 C。更稳妥的演进顺序是：

- 保留当前 Provider resume，因为同 Agent 的 warm path 成本最低；
- 把 B 做成可观测的 context manifest：祖先链完整 refs、当前 Issue 版本、handoff 版本、必须读取的 comment/thread IDs、被预算截断的 IDs；
- 把 A 的摘要作为加速层，而不是唯一事实源；每一条摘要都附 source ID、created/updated time 和 omitted list；
- 让新 Agent 在第一次写代码前完成 manifest 对账，缺失项通过 CLI 补读；
- 用“祖先覆盖率、评论 ID 覆盖率、摘要与原文事实一致率、首轮 token、resume 成功率、切换后返工率”做对照实验；
- 只有在 B 的首轮读取成本无法接受时，才评估 C 的分段/分页 snapshot，而不是无上限地把所有历史拼进一个 Prompt。

这三个方案是讨论材料，不是本次 run 的代码变更。当前真实 dump 展示的是现有方案：assignment Prompt 带 checkpoint 和读取协议，Task payload 带 ancestor/handoff 结构，Provider 从 fresh session 开始；它没有把用户新补充的 comment 伪装成当时已经存在的输入。

