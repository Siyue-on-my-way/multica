# 一次真实 run 的上下文 dump

本目录保存 SIY-121 的一次真实 assignment/Ownership run 素材。数据来自 Multica CLI 的 issue run 记录、daemon 观测日志、Provider transcript 的 user_message，以及本次执行环境内实际生成的 Brief 和 sidecar。文件内容已脱敏，保留字段形状和运行标记。

## 文件

| 文件 | 内容 |
| --- | --- |
| task-payload.json | 以 server/internal/daemon/types.go:63-159 为准的 Task wire payload 快照；_capture 记录观测来源和脱敏说明 |
| prompt.txt | Codex app-server 实际收到的完整 user_message；本次日志的 prompt_bytes=1564 |
| brief.AGENTS.md | 本次 Codex workdir 中生成的 AGENTS.md 脱敏全文 |
| .agent_context/issue_context.md | 本次 sidecar 的 assignment/checkpoint 内容，摘要已并入 task payload 的 handoff_summary |
| .multica/daemon_task_context.json | 本次 sidecar 的最小身份标记，实际内容在执行环境中可见 |
| .multica/project/resources.json | 本次项目资源 sidecar；项目有 project context，但 resources 为空 |

## 这次 capture 发生了什么

- Task ID：0f3ea789-bab1-4d74-92f2-6c56384b4bcf
- Issue：SIY-121
- 时间：2026-09-05 04:26:44 UTC 开始
- Provider：Codex app-server
- 事件：direct / issue assignment；不是 comment trigger
- daemon 观测：resume_session=false、reuse_workdir=false、trigger_comment_id=""、inline_system_prompt=false、mcp_config=false、repos=0
- Brief 通过 AGENTS.md 文件投递；本次没有把完整 Brief 作为 inline system prompt 传给 Codex
- prompt.txt 是 Provider transcript 里的 user_message，不是把 AGENTS.md 和 Prompt 粗暴拼成一个文件
- 当前 run 的 assignment Prompt 没有 [NEW COMMENT]，这是因为 TriggerCommentID 为空；源码级 comment 变体保留在 task-payload.json 的 _capture.marker_examples 和课件中
- ancestor 的结构和 [Background source: Issue X] 标记保留在 Task payload；Prompt 是否直接呈现 ancestor 取决于 claim response 与当前 Provider 执行路径，不能用未观测的字符串替代真实 transcript

## 脱敏规则

1. agent、runtime、workspace 身份替换成 <agent-id>、<runtime-id>、<workspace-id>。
2. 代码仓库和工作目录替换成 <repo>、<workdir>。
3. token、邮箱和 Provider 私密配置替换成 [REDACTED]；绝不导出 MULTICA_TOKEN 的实际值。
4. skill content、MCP secret 和其他凭据不属于教学样本，只保留字段或明确的 redaction marker。
5. Issue ID、Task ID、祖先 Issue ID 和 source marker 保留，因为它们是理解 claim 关联关系所需的非秘密结构。

## 读取方式

先看 task-payload.json 的 _capture 和连续性字段，再对照 prompt.txt 和 brief.AGENTS.md：

1. payload 显示这次是 fresh assignment；
2. Prompt 显示当前回合的 Ownership 和 checkpoint；
3. Brief 显示稳定的运行时契约；
4. ancestor、handoff 和 sidecar 解释为什么 agent 切换后仍能恢复业务上下文；
5. 若要对照 comment run，请用课件中的源码级 [NEW COMMENT] 样例，不要把它误判为这次 capture 的实际输入。

