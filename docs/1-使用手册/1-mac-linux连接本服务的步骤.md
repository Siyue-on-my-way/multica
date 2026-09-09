# Mac 与 Linux 接入 Multica 服务

> **服务地址：** `http://8.148.26.166:2080`

> 一台电脑只启动一个 Multica 守护进程。该守护进程可以注册 Claude、Codex、Grok、Cursor 等多个运行时；安装、升级和重启都按电脑上的守护进程执行，不需要对每个 provider runtime 重复操作。

---

## 快速对比

| | Mac 电脑 | Linux 服务器 |
|---|---|---|
| **有无浏览器** | 有 | 通常无 |
| **登录方式** | OAuth 浏览器授权 | Personal Access Token (PAT) |
| **一键安装** | `curl -fsSL http://8.148.26.166:2080/downloads/install.sh \| bash` | 同左 |
| **安装脚本额外完成** | 写入更新源、启动 daemon | 同左 |

---

## 为什么必须用本服务的一键安装

本部署是私有 fork，官方仓库 `multica-ai/multica` 上没有这些能力。用官方安装脚本装出的 `0.4.18` 有三个问题：

1. 缺少 daemon 侧新能力：Agent 首轮收不到 Context Manifest（无法对账评论）、retry 无法按门控恢复 Provider session；
2. 自动更新源指向官方仓库，以后任何一次 `multica update` 都会把机器**静默拉回官方代码**；
3. 与服务端新接口的兼容性无保障。

因此接入和升级都只使用自托管下载通道 `http://8.148.26.166:2080/downloads/`。一键脚本会下载对应平台的二进制、做 SHA-256 校验、停旧 daemon、替换二进制、写入 `MULTICA_UPDATE_REPO` 并启动 daemon。

安装成功的判定标准（两条都要满足）：

```bash
multica version
# 输出形如：v0.4.41-siyue.1-dirty (commit: 456722116, built: ...)
```

- commit 是 **`456722116`**（或更高）。`-dirty` 后缀是构建通道的标记，无功能影响；
- `MULTICA_UPDATE_REPO=Siyue-on-my-way/multica` 已进入 daemon 环境（脚本会写入 `~/.bashrc` / `~/.zshrc` 并 export）。

---

## Mac 电脑接入

### 第一步：一键安装 CLI

```bash
curl -fsSL http://8.148.26.166:2080/downloads/install.sh | bash
```

脚本只提供 Apple Silicon (arm64) 构建；安装完成后按上面的判定标准验证 `multica version` 和更新源变量。

### 第二步：一键配置并登录

安装脚本最后会启动 daemon；本机首次接入还需完成授权绑定：

```bash
multica setup self-host \
  --server-url http://8.148.26.166:2080 \
  --app-url http://8.148.26.166:2080
```

此命令会依次完成：
1. 将服务器地址写入 `~/.multica/config.json`
2. 自动打开浏览器，进入登录页（测试验证码：`777777`）
3. 授权成功后自动绑定 Workspace
4. 以新配置重启 Daemon 进程

### 第三步：验证

```bash
multica version                        # commit 应为 456722116 或更高
multica daemon status                  # 应显示 running
multica runtime list --output json     # 应能看到本机记录，状态为 online
```

日志位置：`~/.multica/daemon.log`

Web 端验证：`http://8.148.26.166:2080` → **Settings → Runtimes**

---

## Linux 服务器接入

Linux 服务器通常无图形界面，无法通过浏览器完成 OAuth，需使用 **Personal Access Token (PAT)** 登录。

### 第一步：一键安装 CLI

```bash
curl -fsSL http://8.148.26.166:2080/downloads/install.sh | bash
multica version   # commit 应为 456722116 或更高
```

### 第二步：获取 Personal Access Token

在**本地电脑的浏览器**中访问 `http://8.148.26.166:2080`，登录后进入：

**Settings → Tokens → Create Token**

复制生成的 Token（格式为 `mul_...`）。

### 第三步：配置服务器地址并登录

```bash
multica config set server_url http://8.148.26.166:2080
multica config set app_url http://8.148.26.166:2080
multica login
```

按提示粘贴刚复制的 PAT 并回车（`--token=` 为空值时会交互式提示输入，Token 不会留在 Shell 历史中）。

或者直接传入 Token：

```bash
multica login --token mul_xxxxxxxxxxxxxxxx
```

### 第四步：启动 Daemon 并验证

安装脚本已经把 `MULTICA_UPDATE_REPO` 写入 `~/.bashrc`；若 daemon 由 systemd、Supervisor 或其他服务管理器管理，还需要把同一个变量写入该管理器的环境配置——只修改 rc 文件不会改变已运行的守护进程。

```bash
multica daemon start
multica daemon status                  # 应显示 running
multica runtime list --output json     # 应能看到本机记录，状态为 online
```

在 Web 界面 **Settings → Runtimes** 确认该服务器已成功上线。

---

## 版本与能力分层

排查一台机器"哪些能力可用"时，先分两层：

| 能力层 | 内容 | 依赖 |
|---|---|---|
| **服务端能力** | 一键重开会话（入队前自动刷新过期摘要）、三种 rerun 动作、独立取消、五键摘要、claim 时组装 Context Manifest | 只在服务端，任何在线 runtime 都享有 |
| **daemon 侧能力** | Agent 首轮收到 Context Manifest 可对账评论、retry 按门控恢复会话、更新源指向 fork 防止被官方代码拉回 | 需要 fork 构建（commit ≥ `b2ebe15ec`，当前 `456722116`） |

- 机器装的是官方 `0.4.18 (commit: b312e1cbb)` → daemon 侧能力全部缺失，且 `multica update` 会拉回官方代码。处理：重跑一键安装，然后**新开一个终端**再验证。
- 机器离线 → 先开机并启动 daemon，再跑一键安装。
- 同一台电脑上的多个 provider runtime 共用 daemon，升级一次全部生效，不要逐个升级。

---

## 常用维护命令

```bash
multica daemon logs -f       # 实时查看 Daemon 日志
multica daemon stop          # 停止 Daemon
multica version              # 检查版本（判定标准见上）
multica auth status          # 查看当前登录状态
multica workspace list       # 列出已绑定的 Workspace
multica auth logout          # 退出登录
```

> **升级**：机器在线且更新源已指向 fork 时，服务端可用 `multica runtime update <runtime-id> --target-version <tag>` 升级，见[运行时注册与版本分发手册](./2-项目分发到codex-claudecode-grok等.md)；也可以随时重跑一键安装脚本到最新构建。
