# Mac 与 Linux 接入 Multica 服务

> **服务地址：** `http://8.148.26.166:2080`

> 一台电脑只启动一个 Multica 守护进程。该守护进程可以注册 Claude、Codex、Grok、Cursor 等多个运行时；安装、升级和重启都按电脑上的守护进程执行，不需要对每个 provider runtime 重复操作。

---

## 快速对比

| | Mac 电脑 | Linux 服务器 |
|---|---|---|
| **有无浏览器** | 有 | 通常无 |
| **登录方式** | OAuth 浏览器授权 | Personal Access Token (PAT) |
| **一键命令** | `multica setup self-host` | 需手动分步配置 |

---

## Mac 电脑接入

### 第一步：安装 CLI

```bash
curl -fsSL https://raw.githubusercontent.com/multica-ai/multica/main/scripts/install.sh | bash
```

> 脚本优先通过 Homebrew 安装；若 Homebrew 镜像 404，自动降级到 GitHub Releases，下载 `darwin-arm64` 压缩包并安装到 `/usr/local/bin/multica`。

验证安装：

```bash
multica version
# 输出示例：multica 0.4.18 (commit: b312e1cbb, built: 2026-08-04T10:01:08Z)
```

### 第二步：一键配置并登录

先把自托管服务使用的 GitHub fork 写入当前终端和 shell 配置。这个变量必须存在于守护进程的环境中，不能只写在服务端的 `docker/.env`：

```bash
export MULTICA_UPDATE_REPO=Siyue-on-my-way/multica
grep -qxF 'export MULTICA_UPDATE_REPO=Siyue-on-my-way/multica' ~/.zshrc 2>/dev/null || \
  printf '\nexport MULTICA_UPDATE_REPO=Siyue-on-my-way/multica\n' >> ~/.zshrc
```

然后执行注册：

```bash
multica setup self-host \
  --server-url http://8.148.26.166:2080 \
  --app-url http://8.148.26.166:2080
```

此命令会依次完成：
1. 将服务器地址写入 `~/.multica/config.json`
2. 自动打开浏览器，进入登录页（测试验证码：`777777`）
3. 授权成功后自动绑定 Workspace
4. 在后台启动 Daemon 进程

### 第三步：验证

```bash
multica daemon status   # 应显示 running
multica runtime list --output json    # 应能看到本机记录，状态为 online
```

日志位置：`~/.multica/daemon.log`

Web 端验证：`http://8.148.26.166:2080` → **Settings → Runtimes**

> **可选**：如需指定具体的 AI CLI 路径，可在重启 Daemon 前设置环境变量：
> ```bash
> export MULTICA_CLAUDE_PATH=$(which claude)
> export MULTICA_CODEX_PATH=$(which codex)
> multica daemon stop && multica daemon start
> ```

---

## Linux 服务器接入

Linux 服务器通常无图形界面，无法通过浏览器完成 OAuth，需使用 **Personal Access Token (PAT)** 登录。

### 第一步：安装 CLI

```bash
curl -fsSL https://raw.githubusercontent.com/multica-ai/multica/main/scripts/install.sh | bash
```

验证安装：

```bash
multica version
```

### 第二步：获取 Personal Access Token

在**本地电脑的浏览器**中访问 `http://8.148.26.166:2080`，登录后进入：

**Settings → Tokens → Create Token**

复制生成的 Token（格式为 `mul_...`）。

### 第三步：配置服务器地址

```bash
multica config set server_url http://8.148.26.166:2080
multica config set app_url http://8.148.26.166:2080
```

### 第四步：使用 Token 登录

```bash
multica login
```

按提示粘贴刚复制的 PAT 并回车（`--token=` 为空值时会交互式提示输入，Token 不会留在 Shell 历史中）。

或者直接传入 Token：

```bash
multica login --token mul_xxxxxxxxxxxxxxxx
```

### 第五步：启动 Daemon 并验证

持久化自托管 fork 更新源。若守护进程由 systemd、Supervisor 或其他服务管理器启动，还需要把同一个变量写入该管理器的环境配置；只修改 `~/.bashrc` 不会改变已经运行的守护进程：

```bash
export MULTICA_UPDATE_REPO=Siyue-on-my-way/multica
grep -qxF 'export MULTICA_UPDATE_REPO=Siyue-on-my-way/multica' ~/.bashrc 2>/dev/null || \
  printf '\nexport MULTICA_UPDATE_REPO=Siyue-on-my-way/multica\n' >> ~/.bashrc
```

```bash
multica daemon start
multica daemon status   # 应显示 running
multica runtime list --output json    # 应能看到本机记录，状态为 online
```

在 Web 界面 **Settings → Runtimes** 确认该服务器已成功上线。

> **常见问题**：Daemon 找不到 `claude` 或 `codex` 命令时，通过环境变量指定绝对路径：
> ```bash
> export MULTICA_CLAUDE_PATH=/usr/local/bin/claude
> multica daemon stop && multica daemon start
> ```
> 如需永久生效，将 `export` 语句加入 `~/.bashrc` 或 `~/.profile`。

---

## 首次启用本次新功能

本次上下文压缩、Context Manifest 和拆分后的 rerun 需要守护进程至少包含提交 `b2ebe15ec`。旧版 `0.4.18` 不认识 `MULTICA_UPDATE_REPO`，因此第一次升级不能直接依赖 `multica runtime update`：

1. 先从 [运行时注册与版本分发手册](./2-项目分发到codex-claudecode-grok等.md) 构建或取得对应平台的 `b2ebe15ec` 二进制。
2. 停止守护进程，手工替换本机的 `multica` 二进制。
3. 持久化 `MULTICA_UPDATE_REPO`，再启动守护进程。
4. 用 `multica version` 确认输出中的 commit 是 `b2ebe15ec`，再从服务端执行一次按 daemon 的 runtime 更新。

不要使用此前基于 `10e852b9b` 的旧附件作为本次 bootstrap 二进制。以后发布了包含 `b2ebe15ec` 的 semver release 后，每台电脑只需用一个代表性 runtime 执行一次 `multica runtime update`，同一台电脑上的其他 provider runtime 会一起使用新守护进程。

---

## 常用维护命令

```bash
multica daemon logs -f       # 实时查看 Daemon 日志
multica daemon stop          # 停止 Daemon
multica auth status          # 查看当前登录状态
multica workspace list       # 列出已绑定的 Workspace
multica auth logout          # 退出登录
```
