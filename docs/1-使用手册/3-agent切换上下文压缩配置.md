# Agent 切换时的上下文压缩配置

> 首次注册运行时、首次手工 bootstrap、`MULTICA_UPDATE_REPO` 持久化和发布后按 daemon 更新，请先执行 [运行时注册与版本分发手册](./2-项目分发到codex-claudecode-grok等.md)。本页只说明上下文压缩和切换动作，不重复完整的机器升级流程。

Multica 的 handoff 是可迁移的业务检查点，不是 Provider transcript 的副本。自动摘要存放在 `derived_summary`，Agent 手写的 checkpoint 存放在 `manual_checkpoint`；对外的 `handoff_summary` 仍表示当前生效的 checkpoint，手写内容优先于 LLM 摘要。

## 工作原理

服务端生成摘要时会把以下内容交给 handoff LLM：

- Issue 标题、描述、acceptance criteria 和 metadata；
- 祖先 Issue 背景、工作分支和 Agent 状态；
- 最近评论及其 thread parent 关系；
- 附件元数据；
- 最近一次 terminal task 的 result、error 和 failure reason。

摘要写入时带有 Issue revision、最新 comment ID、来源 task、版本和耗时，并通过 handoff version 做 CAS。Issue 有新的语义活动时，旧摘要会被标记为 `stale`；手写 checkpoint 不会被强制压缩覆盖。

新 task claim 时，服务端同时生成 Context Manifest。Manifest 记录 Issue revision、handoff version、祖先 refs、实际纳入的 comment IDs、已知但未纳入的 IDs、来源 task 和 branch checkpoint。Agent 首轮应先对账；需要细节时再用 `multica issue comment list --thread ... --tail 30` 回源读取。

## 三种动作

```text
refresh_summary  只刷新 derived_summary，不入队，也不取消运行中的 task
new_session      复用来源 task 的 workdir，但启动新的 Provider session
retry            按来源 task 重试；仅在失败未污染且 runtime 相容时恢复 Provider session
```

示例：

```bash
multica issue rerun <issue-id> --action refresh_summary --output json
multica issue rerun <issue-id> --action new_session --output json
multica issue rerun <issue-id> --action retry --output json
```

需要停止运行中的 task 时，使用独立的 Cancel API；rerun 不会取消 running task：

```bash
multica issue runs <issue-id> --active --output json
multica issue cancel-task <run-id> --issue <issue-id> --output json
```

## 配置 LLM

在服务端 `.env` 或环境变量中配置 handoff LLM：

```env
MULTICA_LLM_API_KEY=sk-xxxxxxxxxxxxxxxx
MULTICA_LLM_BASE_URL=https://api.openai.com/v1
MULTICA_LLM_DEFAULT_MODEL=gpt-4o-mini
```

也可以通过业务配置为 `handoff-compress` 单独指定 provider/model。修改配置后重启服务端。

## 验证

1. 执行 `multica issue rerun <issue-id> --action refresh_summary --output json`，确认响应包含 `compression_status`、`source_revision` 和 `latency`。
2. 执行一次 `new_session` 或 `retry`，在 claim payload 中确认存在 `context_manifest`。
3. 检查服务端日志中的摘要耗时、CAS conflict 和覆盖率指标。
4. 通过 `multica issue get <issue-id> --output json` 检查 handoff 状态；不要把 workdir 中的历史文件当作事实源。

## 降级与注意事项

| 场景 | 行为 |
|------|------|
| LLM 未配置、评论为空或调用失败 | 保留现有 checkpoint，task 分发不被阻塞 |
| 手写 checkpoint 已存在 | 继续保留手写内容；自动摘要可独立刷新 |
| Issue revision 或 handoff version 在 LLM 返回期间变化 | CAS 拒绝旧结果，下一次刷新重新生成 |
| retry 来源 task 的 session 不安全或 runtime 不同 | 复用 workdir（若仍可用），启动新的 Provider session |
| 需要停止 active run | 调用 `/api/issues/{id}/cancel`，不要依赖 rerun |
