# Windows 电脑连接 Multica 服务步骤

> 服务地址：http://8.148.26.166:2080

> 一台 Windows 电脑只启动一个 Multica 守护进程。该守护进程可以注册 Claude、Codex、Grok、Cursor 等多个运行时；安装、升级和重启都按电脑上的守护进程执行，不需要对每个 provider runtime 重复操作。

## 为什么必须用本服务的一键安装

本部署是私有 fork，官方仓库 `multica-ai/multica` 上没有这些能力。官方安装脚本装出的 `0.4.18` 缺少 daemon 侧新能力（Agent 首轮收不到 Context Manifest、retry 无法按门控恢复会话），且自动更新源指向官方仓库——以后任何一次 `multica update` 都会把机器**静默拉回官方代码**。

因此接入和升级都只使用自托管下载通道 `http://8.148.26.166:2080/downloads/`。一键脚本会下载二进制、做 SHA-256 校验、替换旧版、设置用户级 `MULTICA_UPDATE_REPO` 并启动 daemon。

## 第一步：一键安装 Multica CLI

用**管理员权限**打开 PowerShell，执行（若报执行策略错误，先执行 `Set-ExecutionPolicy -Scope Process Bypass -Force` 再重跑）：

```powershell
irm http://8.148.26.166:2080/downloads/install.ps1 | iex
```

## 第二步：验证安装结果

安装后**新开一个 PowerShell**（让用户级环境变量生效），逐项核对：

```powershell
multica version
# 输出形如：v0.4.41-siyue.1-dirty (commit: 456722116, built: ...)

[Environment]::GetEnvironmentVariable('MULTICA_UPDATE_REPO','User')
# 应输出：Siyue-on-my-way/multica
```

判定标准（两条都要满足）：commit 是 **`456722116`** 或更高；用户级 `MULTICA_UPDATE_REPO` 等于 `Siyue-on-my-way/multica`。`-dirty` 后缀无功能影响。

不达标就重跑第一步的一键安装；达标后不要执行 `multica update`（当前通道已是最新 fork 构建）。

## 第三步：安装 AI 编码工具并登录

安装 Claude Code（推荐，需 >= 2.0.0）：

```powershell
npm install -g @anthropic-ai/claude-code
claude   # 按提示完成登录
```

或安装 Codex（需 >= 0.100.0）：

```powershell
npm install -g @openai/codex
codex    # 按提示完成登录
```

确认 daemon 能找到工具：

```powershell
Get-Command claude
claude --version
```

## 第四步：连接服务器

```powershell
multica setup self-host `
  --server-url http://8.148.26.166:2080 `
  --app-url http://8.148.26.166:2080
```

执行后会自动打开浏览器，用邮箱登录（验证码为 `777777`）完成授权。

> 如果是无浏览器环境，先在 Web 界面 Settings → Tokens 创建 Personal Access Token，然后执行：
> ```powershell
> multica login --token   # 粘贴 PAT，回车
> ```

## 第五步：启动 Daemon 并验证

```powershell
multica daemon start
multica daemon status
multica runtime list --output json
```

启动成功后，在 Web 界面 http://8.148.26.166:2080 → **Runtimes** 页面可以看到本机注册的 runtime（在线状态）。

## 版本与能力分层

| 能力层 | 内容 | 依赖 |
|---|---|---|
| **服务端能力** | 一键重开会话（入队前自动刷新过期摘要）、三种 rerun 动作、独立取消、五键摘要、claim 时组装 Context Manifest | 只在服务端，任何在线 runtime 都享有 |
| **daemon 侧能力** | Agent 首轮收到 Context Manifest 可对账评论、retry 按门控恢复会话、更新源指向 fork | 需要 fork 构建（commit ≥ `b2ebe15ec`，当前 `456722116`） |

官方 `0.4.18 (commit: b312e1cbb)` 两个 daemon 侧能力都缺失，且更新源指向官方仓库。同机多个 provider runtime 共用 daemon，升级一次全部生效。

## 常见问题

**daemon 找不到 `claude` / `codex` 命令**

Windows GUI 启动的进程与 PowerShell 的 PATH 可能不一致，用绝对路径指定：

```powershell
# 查找实际路径
Get-Command claude | Select-Object -ExpandProperty Source

# 设置环境变量（替换为实际路径）
$env:MULTICA_CLAUDE_PATH = "C:\Users\xxx\AppData\Roaming\npm\claude.cmd"
[Environment]::SetEnvironmentVariable("MULTICA_CLAUDE_PATH", $env:MULTICA_CLAUDE_PATH, "User")
multica daemon restart
```

如需永久生效，将该行加入 PowerShell Profile（`$PROFILE`）。

**版本又变回 `0.4.18` 或 `multica update` 后能力丢失**

说明机器上跑了官方安装脚本，或更新源被清掉后被官方代码覆盖。重跑第一步的一键安装即可恢复，并按第二步重新验证。

**查看 daemon 日志排查问题**

```powershell
multica daemon logs -f
```
