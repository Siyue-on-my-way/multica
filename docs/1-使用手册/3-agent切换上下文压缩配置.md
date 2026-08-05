# Agent 切换时的上下文压缩配置

当 Issue 的负责人从一个 Agent 换到另一个 Agent 时，Multica 服务端会自动调用
LLM 对 Issue 的历史评论进行压缩，生成结构化的 `handoff_summary`，并在新 Agent
接手时直接注入到其工作上下文（`issue_context.md`）中。

---

## 工作原理

1. 用户在 UI 或通过 CLI 将 Issue 的负责人更改为另一个 Agent。
2. 服务端检测到 assignee 变更，触发 `compressHandoffContext`。
3. 服务端读取该 Issue 最近至多 **80 条**评论，拼接成对话记录。
4. 调用配置的 LLM 接口，生成如下结构的 JSON 摘要：
   ```json
   {
     "current_progress": "已完成代码分析和 PR 草稿",
     "next_steps": ["跑集成测试", "处理 review 意见"],
     "unresolved_issues": "auth_test.go 第 42 行存在 flaky test"
   }
   ```
5. 摘要写入 Issue 的 `handoff_summary` 字段。
6. 新 Agent 接收任务时，`handoff_summary` 连同 `working_branch` / `agent_status`
   一并注入 `issue_context.md` 的 `## Previous Agent State` 章节。

> **降级策略**：若 LLM 未配置、评论为空、或前一个 Agent 已手动写入 `handoff_summary`，
> 则跳过自动压缩，不影响任务分发。

---

## 配置方式

在服务端的 `.env` 文件（或环境变量）中添加以下三个变量：

```env
# 必填：LLM 接口 API Key
MULTICA_LLM_API_KEY=sk-xxxxxxxxxxxxxxxx

# 必填：LLM 接口的 base URL（兼容 OpenAI 协议的任意服务）
# 示例：使用 OpenAI 官方
MULTICA_LLM_BASE_URL=https://api.openai.com/v1

# 示例：使用 Azure OpenAI
# MULTICA_LLM_BASE_URL=https://your-resource.openai.azure.com/openai/deployments/your-deployment

# 示例：使用本地 Ollama
# MULTICA_LLM_BASE_URL=http://localhost:11434/v1

# 可选：指定用于压缩的模型名称（留空则使用 gpt-5.6-luna）
MULTICA_LLM_DEFAULT_MODEL=gpt-4o-mini
```

修改后重启服务端：

```bash
# Docker Compose 部署
docker compose -f docker-compose.selfhost.yml up -d --no-deps backend

# 或直接重启
make selfhost-stop && make selfhost
```

---

## 验证配置生效

1. 在 Web 界面将一个 Issue 的负责 Agent 从 A 换为 B。
2. 查看服务端日志，应出现：
   ```
   handoff compress: summary written  issue_id=xxx  comment_count=N
   ```
3. 在 Issue 详情页（或通过 CLI）确认字段已写入：
   ```bash
   multica issue get <issue-id> --output json | jq '.handoff_summary'
   ```
4. 等待 Agent B 的任务启动，其工作目录下的 `.agent_context/issue_context.md`
   应包含 `## Previous Agent State` 章节。

---

## 注意事项

| 场景 | 行为 |
|------|------|
| LLM 未配置（无 API Key / BaseURL） | 跳过压缩，不影响任务分发 |
| 前一 Agent 已手动写入 `--handoff-summary` | 跳过自动压缩，优先使用手动 checkpoint |
| Issue 无评论历史 | 跳过压缩 |
| LLM 调用超时（>30s）或返回错误 | 记录警告日志，继续正常分发任务 |
| LLM 返回格式不符 | 记录警告日志，不写入摘要 |
