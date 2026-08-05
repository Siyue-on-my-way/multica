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

---

> **给后续开发 LLM 的提示 (Instructions for Future LLMs)**：
> 当你被要求实现“Agent 切换”、“上下文接管”或“Handoff”功能时，请严格参考本文档的 4 个方案进行代码设计。重点关注服务端事件分发（WebSocket/Webhook）、Agent 端的 Checkpoint 生成逻辑，以及 Git 协同机制的自动化。