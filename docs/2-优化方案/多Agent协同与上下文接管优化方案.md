# 多 Agent 协同与上下文接管 (Handoff) 优化方案

## 1. 背景 (Background)

在 Multica 的多端 Agent 协同架构中，任务（Issue）的执行者可能会在不同的 Agent 之间流转。例如，一个 Issue 最初由 Mac 电脑上的 `mac-codex` 负责，中途可能被重新分配给 Linux 服务器上的 `linux-codex`。

目前，当发生负责人切换时，新接手的 Agent 主要依赖读取 Issue 的历史评论来重建上下文（Context Hydration）。然而，这种依赖非结构化自然语言的交接方式存在以下痛点：
1. **上下文丢失或理解偏差**：新 Agent 可能无法准确从长篇大论的评论中提取出当前确切的代码进度和下一步计划。
2. **重复劳动**：如果前一个 Agent 没有明确说明已完成的工作，新 Agent 可能会重新拉取代码或重复执行分析。
3. **Token 消耗与注意力分散**：对于长周期 Issue，评论区可能长达数万字，直接全量喂给新 Agent 容易导致 Token 超限或 LLM 注意力丢失。

## 2. 目标 (Objective)

设计并实现一套标准化的**分布式 Agent 协作与上下文接管（Handoff）机制**，确保：
1. **无缝切换**：跨设备、跨 Agent 切换负责人时，工作能够丝滑继续，不丢失进度。
2. **状态明确**：新接手的 Agent 能够以极低的成本（Token 和时间）准确获取当前任务进度、代码分支状态和下一步计划。
3. **系统鲁棒性**：将 Issue 追踪系统真正打造为所有 Agent 的“共享大脑”和“单一事实来源（Single Source of Truth）”。

## 3. 方案 (Solution)

为了实现上述目标，后续开发应围绕以下四个核心机制进行落地：

### 3.1 强制 Checkpoint（状态快照）机制
- **触发时机**：要求 Agent 在每次完成一个关键子任务，或者在监听到“负责人被更改/剥夺”的事件时，必须触发 Checkpoint 逻辑。
- **执行动作**：Agent 必须在 Issue 评论区或专门的状态字段中，留下一份**结构化的 Handoff Summary（交接摘要）**。
- **数据结构示例**：
  ```json
  {
    "current_progress": "已完成代码拉取和初步 AST 分析",
    "next_steps": ["执行 grep 查找特定调用链", "生成三维架构图数据"],
    "working_branch": "feat/issue-SIY-2",
    "unresolved_issues": "无"
  }
  ```

### 3.2 系统级状态标签 (Machine-readable State)
- **机制**：除了人类可读的评论，系统应引入机器可读的状态标签。
- **实现**：利用 Issue 的 Label 或自定义属性字段（如 `status: analyzing`, `status: coding`, `branch: feat/xxx`）来实时记录核心状态。
- **优势**：新 Agent 醒来后，优先读取这些结构化字段，无需阅读长文本即可知道该去哪个分支拉代码、当前处于什么研发阶段。

### 3.3 代码与环境状态自动同步 (Git Sync)
- **机制**：文本上下文与代码状态必须对齐。
- **实现**：新 Agent 在接手任务并解析出 `working_branch` 后，其初始化工作流的第一步必须是执行 `git fetch` 和 `git checkout <working_branch>`。
- **异常处理**：如果存在未提交的本地修改（Dirty Workspace），系统需规范前置 Agent 必须完成 `git commit` 或 `git stash` 才能释放任务。

### 3.4 上下文压缩与智能下发 (Context Compression)
- **机制**：解决长 Issue 导致的 Token 爆炸问题。
- **实现**：服务端（Multica Server）在向新 Agent 下发上下文前，引入一个轻量级的“摘要压缩”中间件。
- **逻辑**：利用较小参数的 LLM（或基于规则）对历史评论进行总结，过滤掉中间的试错日志，仅将**核心决策、最终状态和最新的 Checkpoint** 发送给新接手的 Agent。

## 4. 多模型业务独立 YAML 配置方案

### 4.1 目标与范围

当前多个服务端业务调用共享一份全局 `llm.yaml`（或同一组环境变量），模型、参数和 prompt 之间没有业务边界。多模型场景下，一个业务调整模型或 prompt 容易影响其他业务，也无法单独进行版本管理和热更新。

本方案将服务端的每个业务型 LLM 调用拆成一个独立 YAML 文件。文件是可审查、可版本化的声明式配置；运行时加载后生成该业务自己的不可变配置快照。本文只覆盖服务端业务调用，不改变 Agent CLI 的模型、登录和运行时配置。

### 4.2 目录结构与固定映射

部署时将配置目录以只读方式挂载到服务端（以下为示例路径，实际目录由部署固定）：

```text
/etc/multica/llm/
├── chat-title.yaml
├── subissue-suggest.yaml
├── handoff-compress.yaml
└── chat-quick-actions.yaml
```

业务名由服务端的固定 registry 映射到文件，调用方只传递内部的业务枚举或调用对应的业务入口，不接收任意文件路径。即使未来 API 接受业务名，也只能从 registry 中选择，不能由请求拼接目录或文件名。

| 业务 key | 固定文件 | 使用场景 | 输出类型 | 允许的模板变量 |
| --- | --- | --- | --- | --- |
| `chat-title` | `chat-title.yaml` | 根据首条用户消息生成 chat 标题 | `text` | `source_text` |
| `subissue-suggest` | `subissue-suggest.yaml` | 从评论拆解子 issue 并生成预览 | `json` | `comment_text`、`issue_identifier`、`issue_title`、`siblings`、`candidate_parents` |
| `handoff-compress` | `handoff-compress.yaml` | 压缩负责人切换时的评论上下文 | `json` | `issue_title`、`comments` |
| `chat-quick-actions` | `chat-quick-actions.yaml` | 为最新 Agent 回复生成后续操作建议 | `json` | `conversation_context`、`latest_user_message`、`latest_agent_message`、`already_suggested` |

文件名必须与 `business` 字段和 registry 完全匹配。未知文件只记录告警，不会被请求动态发现；未知业务直接返回“业务未配置”，不会回退到另一个业务的文件。

### 4.3 单业务 YAML schema

每个文件都遵循同一个顶层 schema，业务差异只体现在模型参数、模板变量白名单和输出 schema。`api_key_env` 是环境变量名，不是 API key；YAML 中禁止出现真实密钥。

以下是 `subissue-suggest` 的完整示例：

```yaml
# /etc/multica/llm/subissue-suggest.yaml
version: 1
business: subissue-suggest
enabled: true

llm:
  provider: openai-compatible
  base_url: https://llm-gateway.example.com/v1
  api_key_env: MULTICA_SUBISSUE_SUGGEST_API_KEY
  model: gpt-5.6-luna
  temperature: 0.3
  max_completion_tokens: 4096
  timeout_ms: 180000

prompt:
  system: |-
    You split a discussion into independently executable Multica sub issues.
    Return JSON only and preserve all decisions already made in the discussion.
  user_template: |-
    要拆解的评论原文：
    {{comment_text}}

    当前 issue：{{issue_identifier}} {{issue_title}}

    已有的兄弟子 issue：
    {{siblings}}

    候选父 issue：
    {{candidate_parents}}

output:
  format: json
  json_schema:
    type: object
    required: [subissues]
    properties:
      subissues:
        type: array
        items:
          type: object
          required: [title, description, stage, depends_on_titles, confidence]
          properties:
            title: {type: string}
            description: {type: string}
            stage: {type: integer, minimum: 1}
            depends_on_titles: {type: array, items: {type: string}}
            suggested_parent_identifier: {type: [string, "null"]}
            confidence: {type: number, minimum: 0, maximum: 1}
```

字段约定如下：

- `version` 是正整数，用于 schema 升级；不支持的版本只会使当前业务配置失效。
- `business`、`enabled`、`llm`、`prompt`、`output` 必填；`business` 必须是 registry 中的固定 key。
- `llm.api_key_env` 只保存环境变量名。服务端在发起请求时读取对应环境变量，日志、状态接口和错误信息都不得输出其值。
- `llm.base_url`、`model`、`temperature`、`max_completion_tokens`、`timeout_ms` 控制该业务的调用边界；每个业务可独立调整。
- `prompt.system` 和 `prompt.user_template` 必须是字符串。模板只允许使用该业务白名单中的 `{{variable}}`，不支持表达式、include、文件读取或动态函数。
- `output.format` 仅允许 `text` 或 `json`。`json` 必须同时提供可校验的 `json_schema`；服务端在持久化或返回前仍需做业务级字段校验。

### 4.4 加载、快照与调用流程

服务端启动时只扫描 registry 中已知的四个文件，并为每个业务独立执行“读取 → YAML 解析 → schema 校验 → 模板编译 → 发布快照”。调用链固定为：

```text
业务入口
  -> 固定 business registry
  -> 该业务的 ConfigSnapshot
  -> 白名单模板渲染
  -> 该快照指定的 LLM provider/model
  -> output 格式与业务 schema 校验
```

每个 `ConfigSnapshot` 包含已校验的 `version`、业务配置、编译后的模板、来源文件和校验摘要，并以原子方式替换同一业务的旧快照。请求开始后只使用这一份快照，配置轮询不能让一次请求同时看到新旧两个版本。业务之间不共享可变的 prompt、模型参数或输出 schema。

### 4.5 六条边界约束（必须保持）

1. **模板变量仅限业务白名单。** registry 为每个业务声明允许的变量集合；加载时拒绝未知变量，渲染时拒绝缺失变量。变量值只能作为数据插入，不能改变模板结构、读取文件或执行代码。
2. **按业务隔离配置快照。** 每个文件独立解析、校验和发布；某个文件损坏时，只影响对应业务。其他业务继续使用各自的快照，不能因为共享一个配置对象而一起降级。
3. **只读挂载采用轮询热更新。** 配置目录不由服务端写回，不能依赖只在本机可靠的文件监听事件。服务端按固定间隔轮询文件的修改时间、大小和内容摘要，发现变化后在后台解析，校验通过才原子替换快照；轮询和解析失败不能阻塞业务请求。
4. **迁移期保留兼容回退。** 新业务文件优先级最高；新文件不存在时，按固定业务映射从旧全局 `llm.yaml` 生成兼容配置。冷启动遇到新文件损坏时可回退到旧配置；运行中已有有效快照的业务继续使用最后一次成功快照。回退只能发生在同一业务，不能跨业务借用配置，并必须记录原因。
5. **记录配置加载状态。** 每个业务至少记录 `active`、`disabled`、`fallback_legacy`、`stale` 或 `error` 状态，以及来源、版本、校验摘要、最近成功加载时间和最近错误原因。状态日志、指标和诊断接口不得包含 API key 或完整 prompt。
6. **Agent CLI 配置独立。** Claude、Codex 等 Agent CLI 的登录、模型、运行时参数和环境变量由 Agent/运行时配置管理；它们不读取业务 YAML，也不继承业务的 `model`、`base_url` 或 `api_key_env`。业务 LLM 配置只服务于服务端的四个业务入口。

### 4.6 迁移期间的故障语义

- `enabled: false` 是该业务的明确关闭，不应因为旧全局配置存在而重新启用。
- 新文件首次加载失败且没有可用旧配置时，该业务进入 `error`，调用按既有业务语义降级（例如标题保留原值、handoff 不写摘要），不能影响其他业务。
- 已经成功加载过的新文件在后续轮询中变坏时，保留最后一次成功快照并标记 `stale`，直到新文件恢复有效；不能在每个请求中重复解析坏文件。
- 旧全局回退只用于迁移窗口。四个业务全部迁移并完成观测后，再删除回退分支和全局配置入口，避免形成永久的双重真相来源。

### 4.7 后续落地顺序

按“先建立热加载能力，再逐个迁移业务”的顺序实施，每一步都保留原有业务的超时、输出校验和降级行为：

1. **实现配置 registry、schema 校验、不可变快照、只读目录轮询热加载、加载状态记录和同业务 legacy fallback。** 这一阶段不改四个调用场景，先用测试验证单文件损坏隔离、原子切换和回退优先级。
2. **迁移 `chat-title`。** 先迁移最简单的文本输出，验证模型、参数、system/user template 独立版本化，同时保留生成失败时的静默原标题和 CAS 防覆盖行为。
3. **迁移 `subissue-suggest`。** 将拆解规则和 JSON 输出 schema 移入 `subissue-suggest.yaml`，保留候选父 issue、stage、依赖关系和用户确认前的解析校验。
4. **迁移 `handoff-compress`。** 将 handoff 摘要字段、评论窗口和 JSON 校验移入独立配置；保留 30 秒边界、已有手写摘要优先和失败不阻塞负责人切换的语义。
5. **迁移 `chat-quick-actions`。** 将短超时、上下文裁剪、三条建议和语言规则移入独立配置；保留异步生成、并发上限、pending 占位和刷新失败提示。
6. **完成迁移验收后下线全局配置。** 逐业务确认加载状态、热更新、失败隔离和回滚路径，再移除旧 `llm.yaml` 与全局 `MULTICA_LLM_*` 作为业务调用入口；Agent CLI 配置继续独立存在。

验收至少覆盖：四个业务只能命中各自固定文件；未知路径不能被请求注入；任一 YAML 损坏不影响其他业务；只读挂载更新能在轮询后切换快照；无效新配置能按规则保持最后有效快照或回退旧配置；加载状态不泄漏密钥；Agent CLI 的配置和业务 YAML 互不读取。

---

> **给后续开发 LLM 的提示 (Instructions for Future LLMs)**：
> 当你被要求实现“Agent 切换”、“上下文接管”或“Handoff”功能时，请严格参考本文档的 4 个方案和第 4 节的业务 LLM 配置约束进行代码设计。重点关注服务端事件分发（WebSocket/Webhook）、Agent 端的 Checkpoint 生成逻辑、固定 business registry、按业务隔离的配置快照，以及 Git 协同机制的自动化。
