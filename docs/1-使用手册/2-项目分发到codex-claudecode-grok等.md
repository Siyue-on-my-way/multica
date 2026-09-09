# 将项目分发到 Codex、Claude Code、Grok 等运行时

本手册说明如何把 fork 构建的 CLI/daemon 分发到各台电脑，让一台电脑上的所有 provider runtime 用上最新能力。

## 先记住三个边界

- 一台电脑对应一个 daemon。Claude、Codex、Grok、Cursor 等 runtime 共用这个 daemon 的 `multica` 二进制，因此升级一次即可，不要逐个 runtime 升级。
- 服务端 `/mnt/a-opensource-tools/multica/docker/.env` 只配置服务端和本机部署脚本；它不会自动传给其他远端 daemon。
- `MULTICA_UPDATE_REPO=Siyue-on-my-way/multica` 必须出现在目标电脑的 daemon 进程环境中。官方版 `0.4.18` 不认识该变量且更新源指向官方仓库——新机器和旧机器统一用自托管一键安装解决（见下）。

## 分发通道总览

| 通道 | 用途 | 入口 |
|---|---|---|
| **自托管下载通道**（首选） | 新机器首次接入；服务端 CLI/daemon 代码变更后的重新分发 | `http://8.148.26.166:2080/downloads/`，一键脚本 `install.sh` / `install.ps1` |
| **fork GitHub Release + `multica runtime update`** | 已在线机器的就地升级 | `Siyue-on-my-way/multica` 的 Release |
| **手工替换二进制**（兜底） | 下载通道和 Release 都不可用时 | 本页"手工 bootstrap"一节 |

## 1. 维护自托管下载通道

服务端每次改动触及 CLI/daemon 的代码后，从 HEAD 重建全部平台并重新发布通道，防止通道带着旧构建：

```bash
cd /mnt/a-opensource-tools/multica
scripts/publish-cli-downloads.sh
```

脚本会构建 linux-amd64 / darwin-arm64 / windows-amd64 三个二进制，重新生成 `checksums.txt` 与 `manifest.json`（含当前 commit），并刷新一键安装脚本。通道由 nginx 以静态文件方式挂在 `/downloads/`（`docker/nginx/downloads/`，二进制不入库，其余产物入 Git 保持可审计）。

通道当前构建可用 `http://8.148.26.166:2080/downloads/manifest.json` 核对：

```json
{
  "channel": "siyue-fork",
  "version": "v0.4.41-siyue.1-dirty",
  "commit": "456722116",
  "platforms": { "linux-amd64": "...", "darwin-arm64": "...", "windows-amd64": "..." }
}
```

## 2. 新机器接入（首选路径）

按平台执行一键安装，脚本自带 SHA-256 校验、替换二进制、写入 `MULTICA_UPDATE_REPO` 并启动 daemon：

```bash
# macOS / Linux
curl -fsSL http://8.148.26.166:2080/downloads/install.sh | bash
```

```powershell
# Windows（管理员 PowerShell）
irm http://8.148.26.166:2080/downloads/install.ps1 | iex
```

之后的登录、绑定和验证步骤见：

- [Mac 与 Linux 接入步骤](./1-mac-linux连接本服务的步骤.md)
- [Windows 接入步骤](./1-win电脑连接本服务的步骤.md)

安装成功的判定标准：`multica version` 显示 commit `456722116`（或更高），且 daemon 环境中 `MULTICA_UPDATE_REPO=Siyue-on-my-way/multica`。

## 3. 已在线机器的就地升级

前提：fork 仓库已发布包含目标代码的 semver Release（见第 4 节），且目标机 daemon 环境已有 `MULTICA_UPDATE_REPO`。

先确认目标机器没有运行中的任务——更新会暂停该 daemon 的新任务 claim，运行中的任务不应被强制中断，等机器空闲再执行。每台 daemon 用 `multica runtime list --output json` 选任意一个 online 的 runtime 作为代表，只执行一次：

```bash
export TARGET_VERSION='v0.4.41-siyue.1'
multica runtime update <runtime-id> --target-version "$TARGET_VERSION" --wait --output json
multica runtime list --output json
```

若 runtime ID 变化，先用 `multica runtime list --output json` 找到同一 daemon 下任意一个 online runtime 替换；不要为同一 daemon 下的 Claude、Codex、Grok 各执行一次。

> 没有可用 Release 时（例如刚改完代码还没发版），直接重跑 `scripts/publish-cli-downloads.sh` 后让机器重跑一键安装脚本，同样能升级到最新构建。

## 4. 发布一个包含新代码的 fork Release

`multica runtime update` 只读取 `MULTICA_UPDATE_REPO` 对应 GitHub 仓库的 Release 资产。发布版本必须有 `checksums.txt` 以及 GoReleaser 生成的 versioned 资产，例如 `multica-cli-0.4.41-linux-amd64.tar.gz`。在已登录 GitHub CLI 的干净 clone 中执行：

```bash
export UPDATE_REPO='Siyue-on-my-way/multica'
export RELEASE_VERSION='0.4.41'

gh auth status
git clone "https://github.com/${UPDATE_REPO}.git" /tmp/multica-release
cd /tmp/multica-release
git fetch origin main
git checkout main
git pull --ff-only origin main
git tag -a "v${RELEASE_VERSION}" -m "release v${RELEASE_VERSION}"
git push origin "v${RELEASE_VERSION}"
git checkout --detach "v${RELEASE_VERSION}"

# 需要预先安装 goreleaser v2。
goreleaser release --clean --skip=publish
gh release create "v${RELEASE_VERSION}" \
  dist/multica-cli-${RELEASE_VERSION}-* \
  dist/checksums.txt \
  --repo "$UPDATE_REPO" \
  --title "v${RELEASE_VERSION}" \
  --notes "Fork release for self-hosted deployment."
```

## 5. 兜底：手工 bootstrap 二进制

当下载通道与 GitHub Release 都不可达（如内网隔离）时，在已有源码和 Go 1.26 的构建机上手工构建并替换：

```bash
cd /mnt/a-opensource-tools/multica
set -eu
VERSION='0.4.41'
COMMIT="$(git rev-parse --short HEAD)"
DATE="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
LDFLAGS="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}"
mkdir -p /tmp/multica-bootstrap

(cd server && CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -ldflags "$LDFLAGS" -o /tmp/multica-bootstrap/multica-linux-amd64        ./cmd/multica)
(cd server && CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -ldflags "$LDFLAGS" -o /tmp/multica-bootstrap/multica-darwin-arm64       ./cmd/multica)
(cd server && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "$LDFLAGS" -o /tmp/multica-bootstrap/multica-windows-amd64.exe  ./cmd/multica)

sha256sum /tmp/multica-bootstrap/*
```

把对应平台文件复制到目标机后替换（替换前先 `multica daemon stop`，再 `sudo install -m 0755 <file> "$(command -v multica)"` / Windows 下 `Copy-Item ... $multicaPath -Force`），并确认目标机 daemon 环境已有 `MULTICA_UPDATE_REPO`（写法见两份接入手册）。目标架构不一致时不要安装（Mac Intel 需另构建 `GOARCH=amd64`）。

## 6. 常见失败原因

| 现象 | 处理方式 |
|---|---|
| `multica version` 仍是官方 `0.4.18`，或能力缺失 | 机器上跑过官方安装脚本/`multica update`。重跑自托管一键安装，然后**新开终端**重新验证 commit 与 `MULTICA_UPDATE_REPO` |
| `GitHub API returned 404` | `TARGET_VERSION` 没有对应 Release，或 Release 在别的仓库；检查 `UPDATE_REPO` 和 tag |
| `no matching release asset` | Release 缺少对应平台/架构的 `multica-cli-<version>-<os>-<arch>` 资产；用 GoReleaser 重新生成 |
| `runtime update deferred ... active` | 该机器仍有运行中的任务；等任务完成后重试，不要强制杀 daemon |
| `checksum manifest checksums.txt not present` | Release 发布不完整；必须同时上传 `checksums.txt` |
| 一键安装报 `SHA-256 mismatch` | 下载通道与 checksums.txt 不一致；在服务端重跑 `scripts/publish-cli-downloads.sh` 后重试 |
| `unsupported architecture` | 一键安装只覆盖 linux-amd64 / darwin-arm64 / windows-amd64；其他架构走手工 bootstrap |
