#!/usr/bin/env bash
# 用户上传数据生命周期清理（SIY-153）：只依据「数据库引用 + 保留期」清理，
# 绝不按文件年龄直接删除。两个条件缺一不可：
#   1. 数据库无引用 —— attachment.url、四类 avatar_url、
#      channel_media_pending_object / issue_source_context_object_intent 台账；
#   2. 修改时间早于宽限期（默认 14d）—— 保护在途上传、崩溃残留与最近变更。
#
# 纳管范围（白名单）：users/ 与 workspaces/ 两个前缀。skills/ 收件箱有
# 自带生命周期（watcher 自清理），永不触碰；其它未知前缀只报告不删除，
# 避免未来新增上传生产者时被误清。
#
# 用法:
#   uploads-gc.sh              # 默认 dry-run：只报告，不删除任何文件
#   uploads-gc.sh --apply      # 执行删除（仍受上述两条规则约束）
#   uploads-gc.sh --help
#
# 环境变量:
#   UPLOADS_DIR            上传根目录（默认 <repo>/docker/volumes/backend_uploads）
#   DATABASE_URL           数据库连接串；缺省从 UPLOADS_GC_ENV_FILE 读取
#   UPLOADS_GC_ENV_FILE    数据库配置来源（默认 <repo>/docker/.env）
#   UPLOADS_GC_PSQL        psql 命令模板（测试/特殊部署钩子；SQL 走 stdin，
#                          需 -A -t 输出；未设置时自动选择宿主机 psql 或
#                          docker exec 一个 postgres 容器）
#   UPLOADS_GC_GRACE       宽限期：14d / 12h / 30m / 秒数（默认 14d）
#   UPLOADS_GC_LOCK_FILE   运行锁（默认 /tmp/multica-uploads-gc.lock）
#
# 退出码: 0 成功（含 dry-run）；1 参数/环境/数据库失败（绝不删除）；2 帮助。
#
# 安全机制:
#   - 默认 dry-run；--apply 才删除；
#   - 数据库不可用或引用查询输出不符合约定信封（refs-begin/refs-end）时中止；
#   - flock 防并发；删除路径始终位于 UPLOADS_DIR 内（find 相对路径推导）。

set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

UPLOADS_DIR="${UPLOADS_DIR:-$REPO_ROOT/docker/volumes/backend_uploads}"
UPLOADS_GC_ENV_FILE="${UPLOADS_GC_ENV_FILE:-$REPO_ROOT/docker/.env}"
UPLOADS_GC_GRACE="${UPLOADS_GC_GRACE:-14d}"
UPLOADS_GC_LOCK_FILE="${UPLOADS_GC_LOCK_FILE:-/tmp/multica-uploads-gc.lock}"
UPLOADS_GC_PSQL="${UPLOADS_GC_PSQL:-}"

APPLY=false

usage() {
  sed -n '2,42p' "$0" | sed 's/^# \{0,1\}//'
  exit 2
}

for arg in "$@"; do
  case "$arg" in
    --apply) APPLY=true ;;
    --help|-h) usage ;;
    *) echo "[uploads-gc] 未知参数: $arg（--help 查看用法）" >&2; exit 1 ;;
  esac
done

fail() { echo "[uploads-gc] $*" >&2; exit 1; }

# --- 宽限期解析：14d / 12h / 30m / 秒数 -------------------------------------
grace_seconds() {
  case "$1" in
    *[d]) echo $(( ${1%d} * 86400 )) ;;
    *[h]) echo $(( ${1%h} * 3600 )) ;;
    *[m]) echo $(( ${1%m} * 60 )) ;;
    *[0-9]) echo "$1" ;;
    *) fail "无法解析宽限期: $1（支持 14d / 12h / 30m / 秒数）" ;;
  esac
}
GRACE_SECONDS="$(grace_seconds "$UPLOADS_GC_GRACE")"
CUTOFF_EPOCH=$(( $(date +%s) - GRACE_SECONDS ))

[[ -d "$UPLOADS_DIR" ]] || fail "上传目录不存在: $UPLOADS_DIR（用 UPLOADS_DIR 指定）"

# --- 运行锁：并发只允许一个实例 ---------------------------------------------
mkdir -p -- "$(dirname -- "$UPLOADS_GC_LOCK_FILE")"
exec 8>"$UPLOADS_GC_LOCK_FILE"
if ! flock -n 8; then
  echo "[uploads-gc] 另一个实例正在运行（$UPLOADS_GC_LOCK_FILE），本次跳过"
  exit 0
fi

# --- 数据库连接 ---------------------------------------------------------------
# DATABASE_URL 优先取环境变量，否则从 env 文件解析（不整文件 source，
# 避免引入无关变量）。宿主机 psql 不可用时，回退到 docker exec 一个
# postgres 容器（通过容器内 unix socket 连接，无需宿主机端口可达）。
if [[ -z "${DATABASE_URL:-}" && -f "$UPLOADS_GC_ENV_FILE" ]]; then
  DATABASE_URL="$(grep -E '^DATABASE_URL=' "$UPLOADS_GC_ENV_FILE" | tail -n1 | cut -d= -f2- | tr -d '"' || true)"
fi
[[ -n "${DATABASE_URL:-}" ]] || fail "缺少 DATABASE_URL（环境变量或 $UPLOADS_GC_ENV_FILE）"

db_user="" db_pass="" db_host="" db_port="5432" db_name=""
parse_database_url() {
  local rest authority userpass hostport
  rest="${DATABASE_URL#*://}"; rest="${rest%%\?*}"
  authority="${rest%%/*}"
  db_name="${rest#*/}"; [[ "$authority" = "$rest" ]] && db_name=""
  userpass="${authority%%@*}"; hostport="${authority##*@}"
  db_user="${userpass%%:*}"
  [[ "$userpass" == *:* ]] && db_pass="${userpass#*:}"
  db_host="${hostport%%:*}"
  [[ "$hostport" == *:* ]] && db_port="${hostport##*:}"
}
parse_database_url
[[ -n "$db_name" && -n "$db_user" ]] || fail "无法从 DATABASE_URL 解析出数据库与用户"

PSQL_DESC=""
psql_host_cmd() { # 宿主机 psql 可用时的调用命令
  local host="$db_host"
  [[ "$host" == "host.docker.internal" || "$host" == "localhost" ]] && host="127.0.0.1"
  printf 'env PGPASSWORD=%s psql -A -t -q -v ON_ERROR_STOP=1 -h %s -p %s -U %s -d %s' \
    "$(printf '%q' "$db_pass")" "$host" "$db_port" "$db_user" "$db_name"
}

if [[ -n "$UPLOADS_GC_PSQL" ]]; then
  PSQL_DESC="UPLOADS_GC_PSQL"
elif command -v psql >/dev/null 2>&1 && psql --version >/dev/null 2>&1; then
  UPLOADS_GC_PSQL="$(psql_host_cmd)"
  PSQL_DESC="宿主机 psql"
else
  # 在 docker ps 里找 postgres 容器：优先端口映射匹配 DATABASE_URL 的端口。
  local_pg_container=""
  while IFS=$'\t' read -r cname cimage cports; do
    [[ -n "$cname" ]] || continue
    [[ "$cimage" == *postgres* || "$cports" == *'->5432/'* ]] || continue
    if [[ ":${cports}" == *":${db_port}->"* ]]; then
      local_pg_container="$cname"; break
    fi
    [[ -z "$local_pg_container" ]] && local_pg_container="$cname"
  done < <(docker ps --format '{{.Names}}\t{{.Image}}\t{{.Ports}}' 2>/dev/null || true)
  [[ -n "$local_pg_container" ]] || fail "宿主机无可用 psql，且未找到 postgres 容器（可设 UPLOADS_GC_PSQL）"
  UPLOADS_GC_PSQL="docker exec -i -e PGPASSWORD=$(printf '%q' "$db_pass") $local_pg_container psql -A -t -q -v ON_ERROR_STOP=1 -U $db_user -d $db_name"
  PSQL_DESC="docker exec $local_pg_container"
fi

# --- 引用查询 -----------------------------------------------------------------
# 引用提取与后端 KeyFromURL 一致：取 '/uploads/' 之后的部分；台账表存的是
# 原始 key。两个意图台账的所有状态（pending/deleting/tombstoned）都算引用 ——
# 对象删除由各自的 reconciler/sweeper 负责，本脚本永不越权。
REFS_SQL="$(cat <<'SQL'
SELECT 'refs-begin';
SELECT DISTINCT k FROM (
  SELECT substring(url FROM '/uploads/(.*)$')        AS k FROM attachment WHERE url LIKE '%/uploads/%'
  UNION
  SELECT substring(avatar_url FROM '/uploads/(.*)$') AS k FROM "user"    WHERE avatar_url LIKE '%/uploads/%'
  UNION
  SELECT substring(avatar_url FROM '/uploads/(.*)$') AS k FROM agent     WHERE avatar_url LIKE '%/uploads/%'
  UNION
  SELECT substring(avatar_url FROM '/uploads/(.*)$') AS k FROM squad     WHERE avatar_url LIKE '%/uploads/%'
  UNION
  SELECT substring(avatar_url FROM '/uploads/(.*)$') AS k FROM workspace WHERE avatar_url LIKE '%/uploads/%'
  UNION
  SELECT storage_key FROM channel_media_pending_object
  UNION
  SELECT storage_key FROM issue_source_context_object_intent
) refs
WHERE k IS NOT NULL AND k <> '' AND position('..' IN k) = 0;
SELECT 'refs-end';
SQL
)"

tmp_err="$(mktemp)"
if ! refs_out="$(printf '%s\n' "$REFS_SQL" | bash -c "$UPLOADS_GC_PSQL" 2>"$tmp_err")"; then
  err_tail="$(tail -n2 "$tmp_err" | tr '\n' ' ')"
  rm -f "$tmp_err"
  fail "数据库引用查询失败（$PSQL_DESC）—— 为安全起见不执行任何清理。$err_tail"
fi
rm -f "$tmp_err"

# 信封校验：任何"假成功"的空输出（如被 mock 的 docker exec）都会在这里被拦下。
# （不用 head/tail 取首尾行 —— pipefail 下提前退出会触发 SIGPIPE。）
trimmed="$(printf '%s' "$refs_out" | sed '/^[[:space:]]*$/d')"
first_line="${trimmed%%$'\n'*}"
last_line="${trimmed##*$'\n'}"
[[ "$first_line" == "refs-begin" && "$last_line" == "refs-end" ]] || \
  fail "引用查询输出不符合预期信封（$PSQL_DESC）—— 拒绝继续"

declare -A REFS=()
ref_count=0
while IFS= read -r key; do
  [[ -n "$key" ]] || continue
  REFS["$key"]=1
  ref_count=$((ref_count + 1))
done < <(printf '%s\n' "$refs_out" | sed '1d;$d')

# --- 磁盘扫描 ---------------------------------------------------------------
# 纳管 users/ 与 workspaces/ 两个前缀下的文件；其余（skills/ 收件箱等）
# 只计入"非纳管"统计，永不删除。
declare -a PENDING=()
while IFS= read -r -d '' f; do
  rel="${f#"$UPLOADS_DIR"/}"
  PENDING+=("$rel")
done < <(find "$UPLOADS_DIR" -type f \( -path "$UPLOADS_DIR/users/*" -o -path "$UPLOADS_DIR/workspaces/*" \) -print0)

unmanaged_bytes=0
while IFS= read -r -d '' f; do
  sz=$(stat -c %s -- "$f" 2>/dev/null || echo 0)
  unmanaged_bytes=$((unmanaged_bytes + sz))
done < <(find "$UPLOADS_DIR" -mindepth 1 -type f ! -path "$UPLOADS_DIR/users/*" ! -path "$UPLOADS_DIR/workspaces/*" -print0)

# --- 判定 --------------------------------------------------------------------
# 每个文件按其推导出的"对象 key"参与判定：
#   对象     workspaces/ws/x.png           -> workspaces/ws/x.png
#   sidecar  workspaces/ws/x.png.meta.json -> workspaces/ws/x.png
#   残留tmp  workspaces/ws/.x.png.tmp      -> workspaces/ws/x.png
# 被引用 -> 保留；宽限期内 -> 保留；否则进入删除集。
declare -A DELETE_SET=()   # rel -> 1
referenced_count=0 referenced_bytes=0
grace_count=0 grace_bytes=0
delete_count=0 delete_bytes=0
missing_referenced=0
derive_object_key() { # <rel> -> 对象 key（stdout）
  local rel="$1" base dir
  case "$rel" in
    *.meta.json) printf '%s' "${rel%.meta.json}" ;;
    */.*.tmp)
      dir="${rel%/*}"; base="${rel##*/}"
      printf '%s/%s' "$dir" "${base:1:${#base}-5}" ;;
    *) printf '%s' "$rel" ;;
  esac
}

for rel in ${PENDING[@]+"${PENDING[@]}"}; do
  obj_key="$(derive_object_key "$rel")"
  fpath="$UPLOADS_DIR/$rel"
  read -r _mtime fsize <<<"$(stat -c '%Y %s' -- "$fpath" 2>/dev/null || echo '0 0')"

  if [[ -n "${REFS[$obj_key]+x}" ]]; then
    referenced_count=$((referenced_count + 1)); referenced_bytes=$((referenced_bytes + fsize))
    continue
  fi
  if (( _mtime >= CUTOFF_EPOCH )); then
    grace_count=$((grace_count + 1)); grace_bytes=$((grace_bytes + fsize))
    continue
  fi
  DELETE_SET["$rel"]=1
  # 对象进入删除集时，其 sidecar 与残留 tmp 一并加入（无独立判定必要）
  case "$rel" in
    *.meta.json|*.tmp) ;;
    *)
      [[ -f "$UPLOADS_DIR/$rel.meta.json" ]] && DELETE_SET["$rel.meta.json"]=1
      tmp_sib="$(dirname -- "$rel")/.$(basename -- "$rel").tmp"
      [[ -f "$UPLOADS_DIR/$tmp_sib" ]] && DELETE_SET["$tmp_sib"]=1
      ;;
  esac
done

# 删除统计以最终集合为准（含随对象捎带的 sidecar/tmp）
for rel in "${!DELETE_SET[@]}"; do
  sz=$(stat -c %s -- "$UPLOADS_DIR/$rel" 2>/dev/null || echo 0)
  delete_count=$((delete_count + 1)); delete_bytes=$((delete_bytes + sz))
done

# 引用完整性对账：被引用但磁盘上不存在的对象数（数据异常信号，只报告）
for key in ${REFS[@]+"${!REFS[@]}"}; do
  [[ -f "$UPLOADS_DIR/$key" ]] || missing_referenced=$((missing_referenced + 1))
done

# --- 输出 --------------------------------------------------------------------
human() { awk -v b="$1" 'BEGIN{s="B KB MB GB TB";split(s,u," ");i=1;while(b>=1024&&i<5){b/=1024;i++}printf (i==1?"%d%s":"%.1f%s"),b,u[i]}'; }

echo "[uploads-gc] 上传目录: $UPLOADS_DIR"
echo "[uploads-gc] 数据库引用: $ref_count 个 key（$PSQL_DESC）"
echo "[uploads-gc] 宽限期: $UPLOADS_GC_GRACE（修改时间早于 $(date -d "@$CUTOFF_EPOCH" '+%F %T' 2>/dev/null || date -r "$CUTOFF_EPOCH" '+%F %T' 2>/dev/null || echo "epoch $CUTOFF_EPOCH") 的无引用文件可删除）"
echo "----------------------------------------------------------"
printf '  %-20s %8s 个 %12s\n' "有引用（保留）" "$referenced_count" "$(human "$referenced_bytes")"
printf '  %-20s %8s 个 %12s\n' "宽限期内（保留）" "$grace_count" "$(human "$grace_bytes")"
printf '  %-20s %8s 个 %12s\n' "$([[ "$APPLY" == true ]] && echo '已删除' || echo '可删除')" "$delete_count" "$(human "$delete_bytes")"
printf '  %-20s %8s 个 %12s\n' "非纳管目录（跳过）" "-" "$(human "$unmanaged_bytes")"
printf '  %-20s %8s 个\n' "被引用但文件缺失" "$missing_referenced"
echo "----------------------------------------------------------"

if (( delete_count > 0 )) && [[ "$APPLY" == false ]]; then
  echo "[uploads-gc] 待删除明细（dry-run）:"
  for rel in "${!DELETE_SET[@]}"; do
    echo "    - $rel ($(human "$(stat -c %s -- "$UPLOADS_DIR/$rel" 2>/dev/null || echo 0)"))"
  done | sort
fi

if [[ "$APPLY" == false ]]; then
  echo "[uploads-gc] dry-run：未删除任何文件。确认无误后追加 --apply 执行删除。"
  exit 0
fi

# --- 执行删除 ----------------------------------------------------------------
if (( ${#DELETE_SET[@]} == 0 )); then
  echo "[uploads-gc] 没有满足删除条件的文件，未做任何变更。"
  exit 0
fi
for rel in "${!DELETE_SET[@]}"; do
  rm -f -- "$UPLOADS_DIR/$rel"
done
# 只清理 users/ 与 workspaces/ 下因此变空的目录（根目录与 skills/ 永不触碰）
find "$UPLOADS_DIR/users" "$UPLOADS_DIR/workspaces" -mindepth 1 -type d -empty -delete 2>/dev/null || true
echo "[uploads-gc] 已删除 $delete_count 个文件（$(human "$delete_bytes")）。"
