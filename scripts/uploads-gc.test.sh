#!/usr/bin/env bash
# scripts/uploads-gc.sh 的行为测试（SIY-153）：引用判定、宽限期保护、失败中止。
#
# 全程 mock psql（fixture 引用集合），上传目录用临时目录；
# 断言：只有「无引用且过宽限期」的文件被删除，数据库失败时绝不删除。
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

assert_contains() { # <string> <expected-substring> <label>
  local haystack="$1" needle="$2" label="$3"
  grep -Fq -- "$needle" <<<"$haystack" || fail "$label: expected to contain '$needle'
Observed:
$(sed 's/^/  /' <<<"$haystack")"
}

assert_gone() { [[ ! -e "$tmp_dir/uploads/$1" ]] || fail "$2: $1 仍存在，应已被删除"; }
assert_kept() { [[ -e "$tmp_dir/uploads/$1" ]] || fail "$2: $1 不见了，被误删！"; }

# --- 夹具：一个有引用、有孤儿、有残留的上传目录 -----------------------------
uploads="$tmp_dir/uploads"
ws="workspaces/11111111-1111-1111-1111-111111111111"
u="users/22222222-2222-2222-2222-222222222222"
mkdir -p "$uploads/$ws" "$uploads/$u" "$uploads/skills/failed"

# 被引用的文件（旧 mtime —— 引用优先于年龄，必须保留）
echo ref-object > "$uploads/$ws/ref-a.png"
echo meta > "$uploads/$ws/ref-a.png.meta.json"
echo ref-avatar  > "$uploads/$u/avatar.png"
echo intent-obj  > "$uploads/$ws/intent-obj.bin"
touch -d '30 days ago' "$uploads/$ws/ref-a.png" "$uploads/$ws/ref-a.png.meta.json" \
  "$uploads/$u/avatar.png" "$uploads/$ws/intent-obj.bin"

# 无引用且过期的孤儿（连同 sidecar 与 .tmp 残留 —— 应全部删除）
echo orphan > "$uploads/$ws/orphan-old.png"
echo meta   > "$uploads/$ws/orphan-old.png.meta.json"
echo stale  > "$uploads/$ws/.orphan-old.png.tmp"
# 无引用但仍在宽限期内（必须保留）
echo fresh > "$uploads/$ws/orphan-fresh.log"
# 无引用过期的孤儿 sidecar / tmp（对象本体已不在，应删除）
echo meta > "$uploads/$ws/lost-sidecar.png.meta.json"
echo tmp  > "$uploads/$u/.lost-obj.tmp"
touch -d '30 days ago' "$uploads/$ws/orphan-old.png" "$uploads/$ws/orphan-old.png.meta.json" \
  "$uploads/$ws/.orphan-old.png.tmp" "$uploads/$ws/lost-sidecar.png.meta.json" "$uploads/$u/.lost-obj.tmp"

# 非纳管目录（skills 收件箱自带生命周期，永不触碰）
echo inbox > "$uploads/skills/failed/drop.zip"
touch -d '30 days ago' "$uploads/skills/failed/drop.zip"

# --- mock psql：固定返回引用集合 ---------------------------------------------
make_psql() { # <mode: ok|error|garbage>
  cat >"$tmp_dir/fake-psql" <<EOF
#!/usr/bin/env bash
cat >/dev/null   # 消费 stdin 里的 SQL
case "\${FAKE_PSQL_MODE:-$1}" in
  ok)      printf 'refs-begin\n'
           printf '%s\n' "$ws/ref-a.png" "$u/avatar.png" "$ws/intent-obj.bin" "$ws/gone-missing.png"
           printf 'refs-end\n' ;;
  error)   echo "psql: could not connect" >&2; exit 1 ;;
  garbage) echo "<html>gateway timeout</html>" ;;
esac
EOF
  chmod +x "$tmp_dir/fake-psql"
}
make_psql ok

gc() { # 运行脚本，捕获输出（便于断言）
  UPLOADS_DIR="$uploads" UPLOADS_GC_PSQL="$tmp_dir/fake-psql" \
    "$root_dir/scripts/uploads-gc.sh" "$@"
}

# --- 1. dry-run（默认）：只报告，一个文件都不动 ------------------------------
out="$(gc)"
assert_contains "$out" "dry-run：未删除任何文件" "dry-run 声明"
assert_contains "$out" "数据库引用: 4 个 key" "引用计数"
assert_contains "$out" "被引用但文件缺失        1 个" "引用完整性对账"
assert_kept "$ws/orphan-old.png" "dry-run 不删除"

# --- 2. --apply：删孤儿及其残留，引用/宽限期/非纳管全部保留 ------------------
out="$(gc --apply)"
assert_contains "$out" "已删除 5 个文件" "删除计数（孤儿对象+sidecar+tmp+孤儿sidecar+孤儿tmp）"
assert_gone "$ws/orphan-old.png" "apply"
assert_gone "$ws/orphan-old.png.meta.json" "apply"
assert_gone "$ws/.orphan-old.png.tmp" "apply"
assert_gone "$ws/lost-sidecar.png.meta.json" "apply"
assert_gone "$u/.lost-obj.tmp" "apply"
# 有引用的业务数据完好无损（验收标准 3）
assert_kept "$ws/ref-a.png" "有引用对象"
assert_kept "$ws/ref-a.png.meta.json" "有引用 sidecar"
assert_kept "$u/avatar.png" "有引用头像"
assert_kept "$ws/intent-obj.bin" "台账引用对象"
# 宽限期保护在途/最近变更
assert_kept "$ws/orphan-fresh.log" "宽限期内孤儿"
# 非纳管目录永不触碰
assert_kept "skills/failed/drop.zip" "skills 收件箱"

# --- 3. 数据库失败：中止且绝不删除 ------------------------------------------
make_psql error
echo keep > "$uploads/$ws/orphan-old.png"; touch -d '30 days ago' "$uploads/$ws/orphan-old.png"
out="$(set +e; gc --apply 2>&1)" || true
assert_contains "$out" "数据库引用查询失败" "DB 失败中止"
assert_kept "$ws/orphan-old.png" "DB 失败时不删除"

# --- 4. 输出不符合信封（假成功）：中止且绝不删除 ----------------------------
make_psql garbage
out="$(set +e; gc --apply 2>&1)" || true
assert_contains "$out" "拒绝继续" "信封校验中止"
assert_kept "$ws/orphan-old.png" "假成功时不删除"

# --- 5. 脚本卫生：不允许出现 UPLOADS_DIR 定向 rm -f 之外的危险删除 ----------
sweeps='rm -rf|rm -r |mkfs|dd if='
if grep -Eq "$sweeps" "$root_dir/scripts/uploads-gc.sh"; then
  fail "uploads-gc.sh 出现危险删除命令:
$(grep -En "$sweeps" "$root_dir/scripts/uploads-gc.sh")"
fi

echo "uploads-gc tests passed"
