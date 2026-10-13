# Container Diagnostics Capability（受限容器诊断能力）

本文档说明 Multica daemon 的可选容器诊断能力：它是什么、为什么存在、如何配置、安全边界是什么。

## 背景与根因

一次典型的故障场景：Windows 主机上 daemon 与 agent 绑定，Podman machine 和容器运行正常，用户交互式 PowerShell 能执行 `podman logs`；但 Multica agent 任务内的 PowerShell 执行 `Get-Command podman` 找不到命令。

真实执行链（源码依据）：

1. 任务绑定到某个 runtime/daemon，daemon 按任务准备 workdir 与 per-task `CODEX_HOME`。
2. daemon 组装子进程环境：`agentEnv["PATH"] = binDir + PATH`，其中 `PATH` 取自 **daemon 进程自身**的 `os.Getenv("PATH")`（`internal/daemon/daemon.go`，"Ensure the multica CLI is on PATH" 段）。
3. daemon 为每个任务写入托管 Codex `shell_environment_policy`（`inherit="all"` + `include_only` 白名单；`internal/daemon/execenv/codex_shell_env.go`）。白名单来源同样是 daemon 进程环境 + agentEnv + agent custom_env。
4. Codex 的 shell tool 子进程（Windows 上为 PowerShell）继承的正是 **daemon 进程启动时刻的 PATH 快照**。

因此这是"**同机但不同进程环境**"：daemon 的启动上下文（自启动项、服务、Desktop app、在安装 Podman 之前启动等）常常不包含按用户安装的 Podman 路径；交互式 PowerShell 的 PATH（系统 PATH + 用户 PATH 合并，注册表 `HKCU\Environment`）与 daemon 进程快照并不相同。它**不是**跨机器网络/身份问题，也不是 Podman 或 Codex 的 bug——任务 shell 环境继承 daemon 进程环境是当前产品约定（`MUL-5578` 的隔离模型）；缺的是一条显式、受控、可审计的诊断通道，以及"命令不存在"之外可操作的错误说明。

## 能力设计

`multica containers status|logs|inspect` 通过本地 daemon 的 localhost HTTP 端点（`POST /containers/diagnostics`，与 `/repo/checkout` 同一监听器、同一任务凭据认证机制）执行**固定的只读操作**：

- 引擎解析发生在 daemon 进程内（可用 `engine_path` 指定绝对路径），**不依赖任务 shell 的 PATH**；
- 直接 argv 拼装、**不经任何 shell**，任务侧输入只能是"操作 + 白名单内容器名 + 受限参数"；
- 仅允许 `ps`、`logs`、`inspect` 三个只读子命令；
- 容器名精确匹配白名单（拒绝通配符/模式/注入字符）；
- `--tail` 上限 500（默认 200）、`--since` 上限 24h、输出上限 256KiB、单次调用 30s 超时；
- 返回给任务前对疑似凭据值（键名含 key/secret/token/password）做 `***` 脱敏，键名保留以便诊断；
- 每次调用（含拒绝）写入 daemon 审计日志：op、container、task_id、agent_id、耗时、输出字节、结果。

**能力未配置时**，端点返回 404 并附"如何启用"的可操作说明；容器不在白名单时返回 403 并列出允许集——agent 能明确区分"能力不存在""无权限""引擎故障"三类原因，而不是"命令不存在"。

## 配置（管理员，仅限本机）

在 **daemon 所在主机**执行（human-only 命令，任务内被拒绝）：

```bash
multica containers configure \
  --engine podman \
  --engine-path "C:\Program Files\Podman\podman.exe" \
  --container ai-cms-api --container ai-cms-worker --container ai-cms-runtime
multica containers disable   # 停用（保留白名单于配置中）
```

等价地，它写入本机 profile 的 `~/.multica/config.json`（或 `~/.multica/profiles/<name>/config.json`）：

```json
{
  "container_diagnostics": {
    "enabled": true,
    "engine": "podman",
    "engine_path": "C:\\Program Files\\Podman\\podman.exe",
    "engine_args": ["--connection=podman-machine-default"],
    "containers": ["ai-cms-api", "ai-cms-worker", "ai-cms-runtime"]
  }
}
```

修改后**重启 daemon** 生效。

### 作用域与默认行为

- 配置是**每台机器/每 profile 本地**的，从不发送到服务器；其他 runtime、其他机器不会继承，除非在对应主机上由管理员显式配置同样的能力（与 `profile_command_overrides` 同一本地性约定）。
- 未配置（默认）时行为与历史版本完全一致：端点不存在于任何任务可见面，任务提示词逐字节不变，不影响任何 agent/runtime 的默认权限。
- 引擎参数（`engine_args`）为管理员所有，作为独立 argv 传入，任务不可控。

## 任务内使用（agent）

`multica containers status` / `logs <name> [--tail N] [--since 30m]` / `inspect <name> --output json`。
这些命令要求任务环境（`MULTICA_DAEMON_PORT` + 任务级 `MULTICA_TOKEN`，daemon 自动注入），与 `multica repo checkout` 同一约束；任务结束或 daemon 重启后凭据失效并得到明确错误。

配置了能力的 runtime，daemon 会在任务提示词的 per-turn context 中追加一小节"Container diagnostics (this runtime)"，说明允许的容器与命令；未配置的 runtime 不追加。

## 安全边界（明确不做的）

- 不开放任意宿主机 shell，不开放容器写操作（start/stop/rm/exec 等一律拒绝并审计）；
- 不要求、也不提供任何 `.env`/密钥内容；输出经脱敏；
- 不开放无认证 Podman API TCP、宽泛 SSH/防火墙通道；
- 白名单按名字精确匹配，改白名单只能由本机管理员修改本地配置文件，任务内无法提权。

## 测试

- `server/internal/daemon/container_diagnostics_test.go`：白名单/注入拒绝、argv 构造与上限、脱敏、审计、HTTP 认证与可操作错误、提示词注入开关、配置 round-trip。
- `server/internal/daemon/container_diagnostics_windows_test.go`（Windows 标签）：绝对路径 pin、相对路径拒绝、Windows 容器名字符集。
- 交叉编译验证：`GOOS=windows go build ./...`。

注意：在本仓库所在的开发环境中运行 daemon 测试时，请使用 `env -u MULTICA_TASK_CONFIG_ROOT`，否则 `cli.ProfileDir` 会解析到运行环境的任务配置根，导致一批与本次改动无关的用例失败（同理 `TestTurnModeMarkersRetired` 在当前 HEAD 上即为失败，属上游遗留，与本能力无关）。
