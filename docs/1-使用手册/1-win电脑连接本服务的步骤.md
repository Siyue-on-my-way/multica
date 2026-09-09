# Windows 电脑连接 Multica 服务步骤

> 服务地址：http://8.148.26.166:2080

> 一台 Windows 电脑只启动一个 Multica 守护进程。该守护进程可以注册 Claude、Codex、Grok、Cursor 等多个运行时；安装、升级和重启都按电脑上的守护进程执行，不需要对每个 provider runtime 重复操作。

## 第一步：安装 Multica CLI

用**管理员权限**打开 PowerShell，执行：

```powershell
irm https://raw.githubusercontent.com/multica-ai/multica/main/scripts/install.ps1 | iex
```

验证安装成功：

```powershell
multica version
```

## 第二步：安装 AI 编码工具并登录

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

## 第三步：连接服务器

先把自托管服务使用的 GitHub fork 持久化到当前 Windows 用户。这个变量必须进入守护进程的环境，不能只配置在服务端的 `docker/.env`：

```powershell
[Environment]::SetEnvironmentVariable("MULTICA_UPDATE_REPO", "Siyue-on-my-way/multica", "User")
$env:MULTICA_UPDATE_REPO = "Siyue-on-my-way/multica"
```

然后执行注册：

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

## 第四步：启动 Daemon 并验证

```powershell
multica daemon start
multica daemon status
multica runtime list --output json
```

启动成功后，在 Web 界面 http://8.148.26.166:2080 → **Runtimes** 页面可以看到本机注册的 runtime（在线状态）。

## 常见问题

**daemon 找不到 `claude` / `codex` 命令**

Windows GUI 启动的进程与 PowerShell 的 PATH 可能不一致，用绝对路径指定：

```powershell
# 查找实际路径
Get-Command claude | Select-Object -ExpandProperty Source

# 设置环境变量（替换为实际路径）
$env:MULTICA_CLAUDE_PATH = "C:\Users\xxx\AppData\Roaming\npm\claude.cmd"
[Environment]::SetEnvironmentVariable("MULTICA_UPDATE_REPO", "Siyue-on-my-way/multica", "User")
$env:MULTICA_UPDATE_REPO = "Siyue-on-my-way/multica"
multica daemon restart
```

如需永久生效，将该行加入 PowerShell Profile（`$PROFILE`）。

**查看 daemon 日志排查问题**

```powershell
multica daemon logs -f
```

## 首次启用本次新功能

本次上下文压缩、Context Manifest 和拆分后的 rerun 需要守护进程至少包含提交 `b2ebe15ec`。旧版 `0.4.18` 不认识 `MULTICA_UPDATE_REPO`，因此第一次升级不能直接依赖 `multica runtime update`：

1. 从 [运行时注册与版本分发手册](./2-项目分发到codex-claudecode-grok等.md) 构建或取得 `windows-amd64` 的 `b2ebe15ec` 二进制。
2. 在管理员 PowerShell 中停止守护进程，手工替换 `multica.exe`。
3. 设置上面的用户级 `MULTICA_UPDATE_REPO`，打开一个新的 PowerShell，再启动守护进程。
4. 用 `multica version` 确认输出中的 commit 是 `b2ebe15ec`，再从服务端执行一次按 daemon 的 runtime 更新。

不要使用此前基于 `10e852b9b` 的旧附件作为本次 bootstrap 二进制。以后发布了包含 `b2ebe15ec` 的 semver release 后，每台电脑只需用一个代表性 runtime 执行一次 `multica runtime update`，同一台电脑上的其他 provider runtime 会一起使用新守护进程。
