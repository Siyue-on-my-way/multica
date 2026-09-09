# 将项目分发到 Codex、Claude Code、Grok 等运行时

本手册说明首次注册后，如何让一台电脑上的所有 provider runtime 使用本次新功能。

## 先记住三个边界

- 一台电脑对应一个 daemon。Claude、Codex、Grok、Cursor 等 runtime 共用这个 daemon 的 `multica` 二进制，因此升级一次 daemon 即可，不要逐个 runtime 升级。
- 服务端 `/mnt/a-opensource-tools/multica/docker/.env` 只配置服务端和本机部署脚本；它不会自动传给 `47.251`、`cqtexfengsy-cc` 或其他远端 daemon。
- `MULTICA_UPDATE_REPO` 必须出现在目标电脑的 daemon 进程环境中。旧版 `0.4.18` 不认识该变量，第一次升级必须先手工安装包含 `b2ebe15ec` 的同平台二进制。

## 1. 先配置服务端更新源

在 Multica 服务端执行下面整段命令。它会配置 fork、重建后端、同步本机 daemon，并验证后端容器看到的值：

```bash
cd /mnt/a-opensource-tools/multica

UPDATE_REPO='Siyue-on-my-way/multica'
if grep -q '^MULTICA_UPDATE_REPO=' docker/.env; then
  sed -i "s#^MULTICA_UPDATE_REPO=.*#MULTICA_UPDATE_REPO=${UPDATE_REPO}#" docker/.env
else
  printf '\nMULTICA_UPDATE_REPO=%s\n' "$UPDATE_REPO" >> docker/.env
fi

./restart.sh --backend --force-build
docker-compose -f docker/docker-compose.yml exec multica-backend printenv MULTICA_UPDATE_REPO
/usr/local/bin/multica version
```

服务端这里的值只保证服务端和 `8.148` 这台本机 daemon 使用 fork；其他电脑仍要在各自的 daemon 环境中设置同名变量。

## 2. 给旧 daemon 做一次 bootstrap

### 2.1 在已有源码的构建机生成各平台二进制

下面命令要求构建机已经有 Go 1.26，并且当前源码包含 `b2ebe15ec`。它只生成第一次手工替换需要的裸二进制，不会修改目标电脑：

```bash
cd /mnt/a-opensource-tools/multica
set -eu
git merge-base --is-ancestor b2ebe15ec HEAD

VERSION='0.4.41'
COMMIT='b2ebe15ec'
DATE="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
LDFLAGS="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}"
mkdir -p /tmp/multica-bootstrap-b2

(cd server && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$LDFLAGS" -o /tmp/multica-bootstrap-b2/multica-linux-amd64 ./cmd/multica)
(cd server && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags "$LDFLAGS" -o /tmp/multica-bootstrap-b2/multica-darwin-arm64 ./cmd/multica)
(cd server && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "$LDFLAGS" -o /tmp/multica-bootstrap-b2/multica-windows-amd64.exe ./cmd/multica)

sha256sum /tmp/multica-bootstrap-b2/*
```

如果构建机不是 `/mnt/a-opensource-tools/multica`，把第一行的目录替换为包含该提交的源码目录。Mac Intel 还要额外构建 `GOARCH=amd64`；目标架构不一致时不要安装。

### 2.2 Linux 目标机

先把 `multica-linux-amd64` 复制到目标机的 `/tmp/`。例如在构建机执行：

```bash
scp /tmp/multica-bootstrap-b2/multica-linux-amd64 <ssh-user>@<linux-host>:/tmp/multica-linux-amd64
```

然后在目标 Linux 机执行：

```bash
set -eu
MULTICA_BIN="$(command -v multica)"
multica daemon stop 2>/dev/null || true
sudo install -m 0755 /tmp/multica-linux-amd64 "$MULTICA_BIN"

export MULTICA_UPDATE_REPO='Siyue-on-my-way/multica'
grep -qxF "export MULTICA_UPDATE_REPO='Siyue-on-my-way/multica'" ~/.bashrc 2>/dev/null || \
  printf "\nexport MULTICA_UPDATE_REPO='Siyue-on-my-way/multica'\n" >> ~/.bashrc

multica version
multica daemon start
multica daemon status
```

如果 daemon 由 systemd 或其他 supervisor 启动，把 `MULTICA_UPDATE_REPO=Siyue-on-my-way/multica` 加入 supervisor 的环境后再重启；shell 的 `export` 只对从该 shell 启动的进程生效。

### 2.3 Mac M1/M2 目标机

先把 `multica-darwin-arm64` 复制到目标机的 `/tmp/`。例如在构建机执行：

```bash
scp /tmp/multica-bootstrap-b2/multica-darwin-arm64 <ssh-user>@<mac-host>:/tmp/multica-darwin-arm64
```

然后在目标 Mac 执行：

```bash
set -eu
MULTICA_BIN="$(command -v multica)"
multica daemon stop 2>/dev/null || true
sudo install -m 0755 /tmp/multica-darwin-arm64 "$MULTICA_BIN"

export MULTICA_UPDATE_REPO='Siyue-on-my-way/multica'
grep -qxF "export MULTICA_UPDATE_REPO='Siyue-on-my-way/multica'" ~/.zshrc 2>/dev/null || \
  printf "\nexport MULTICA_UPDATE_REPO='Siyue-on-my-way/multica'\n" >> ~/.zshrc

multica version
multica daemon start
multica daemon status
```

### 2.4 Windows 目标机

先把 `multica-windows-amd64.exe` 复制到目标机，例如复制到 `C:\Temp\multica-windows-amd64.exe`。在管理员 PowerShell 中执行：

```powershell
$multicaPath = (Get-Command multica).Source
multica daemon stop 2>$null
Copy-Item 'C:\Temp\multica-windows-amd64.exe' $multicaPath -Force

[Environment]::SetEnvironmentVariable('MULTICA_UPDATE_REPO', 'Siyue-on-my-way/multica', 'User')
$env:MULTICA_UPDATE_REPO = 'Siyue-on-my-way/multica'

multica version
multica daemon start
multica daemon status
```

关闭当前 PowerShell，再打开一个新的 PowerShell，确认用户级环境变量仍然存在：

```powershell
$env:MULTICA_UPDATE_REPO
multica version
```

四个平台的 `multica version` 都必须看到 commit `b2ebe15ec` 后，才进入下一步。若版本仍为 `0.4.18`，先检查实际路径 `command -v multica` / `Get-Command multica`，并确认 daemon 已停止后再替换正在使用的文件。

## 3. 发布一个包含新代码的 semver release

`multica runtime update` 不是从源码分支下载，它只读取 `MULTICA_UPDATE_REPO` 对应 GitHub 仓库的 Release 资产。发布版本必须包含 `b2ebe15ec`，并且必须有 `checksums.txt` 以及 GoReleaser 生成的 versioned 资产，例如 `multica-cli-0.4.41-linux-amd64.tar.gz`。

当前 fork 的 release workflow 只做校验，不会替 fork 发布 release。因此可以在已经登录 GitHub CLI 的干净 clone 中执行下面命令，用 GoReleaser 生成资产，再手动创建 GitHub Release：

```bash
export UPDATE_REPO='Siyue-on-my-way/multica'
export RELEASE_VERSION='0.4.41'

gh auth status
git clone "https://github.com/${UPDATE_REPO}.git" /tmp/multica-release
cd /tmp/multica-release
git fetch origin main
git checkout main
git pull --ff-only origin main
git merge-base --is-ancestor b2ebe15ec HEAD
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
  --notes "Contains context compression and runtime session updates."
```

如果 fork 已经有一个由其他流程发布、且明确包含 `b2ebe15ec` 的 release，可以跳过这一节；不要把旧的 `10e852b9b` release 当作本次版本。

## 4. 发布后按 daemon 更新

先确认没有运行中的任务。更新会暂停该 daemon 的新任务 claim，运行中的任务不应被强制中断；等待机器空闲后再执行。当前工作区每台 daemon 选一个代表性 runtime 即可：

| 电脑 | 代表性 runtime ID | 当前状态 |
|---|---|---|
| `8.148` | `8bef980e-eb53-4bfd-a211-62816f1f9766` | 已是 `b2ebe15ec`，可在 release 发布后统一到稳定版本 |
| `47.251` | `2455438d-78b2-42b6-8736-9ab5025952cd` | 旧版，先完成 Linux bootstrap |
| `cqtexfengsy-cc` | `46f7ab3b-5bb6-4301-ad97-b61a6db08e72` | 旧版，先完成 Linux bootstrap |
| `siyue-mac-m1` | `dab7eae8-0659-42fa-bddf-41c3c9f4198f` | 当前 offline，先启动 Mac daemon |

发布 `v0.4.41` 后，在能访问该工作区的机器执行：

```bash
export TARGET_VERSION='v0.4.41'

multica runtime update 8bef980e-eb53-4bfd-a211-62816f1f9766 \
  --target-version "$TARGET_VERSION" --wait --output json

multica runtime update 2455438d-78b2-42b6-8736-9ab5025952cd \
  --target-version "$TARGET_VERSION" --wait --output json

multica runtime update 46f7ab3b-5bb6-4301-ad97-b61a6db08e72 \
  --target-version "$TARGET_VERSION" --wait --output json

multica runtime update dab7eae8-0659-42fa-bddf-41c3c9f4198f \
  --target-version "$TARGET_VERSION" --wait --output json

multica runtime list --output json
```

每台 daemon 只执行一次。若 runtime ID 发生变化，先用 `multica runtime list --output json` 找到同一 daemon 下任意一个 online runtime，再替换命令中的 ID；不要为同一 daemon 下的 Claude、Codex、Grok 等各执行一次。

## 5. 常见失败原因

| 现象 | 处理方式 |
|---|---|
| `GitHub API returned 404` | `TARGET_VERSION` 没有对应 Release，或 Release 在别的仓库；检查 `UPDATE_REPO` 和 tag |
| `no matching release asset` | Release 缺少对应平台/架构的 `multica-cli-<version>-<os>-<arch>` 资产；用 GoReleaser 重新生成 |
| 仍然显示 `0.4.18` | 远端 daemon 没有读取服务端 `.env`；在目标机的 daemon 环境设置 `MULTICA_UPDATE_REPO`，确认停止旧进程后再替换二进制 |
| `runtime update deferred ... active` | 该机器仍有运行中的任务；等任务完成后重试，不要强制杀 daemon |
| `checksum manifest checksums.txt not present` | Release 发布不完整；必须同时上传 `checksums.txt` |
