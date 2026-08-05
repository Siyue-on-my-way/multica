# Windows 电脑连接 Multica 服务步骤

> 服务地址：http://8.148.26.166:2080

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
multica runtime list
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
multica daemon restart
```

如需永久生效，将该行加入 PowerShell Profile（`$PROFILE`）。

**查看 daemon 日志排查问题**

```powershell
multica daemon logs -f
```
