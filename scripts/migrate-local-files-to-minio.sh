#!/usr/bin/env bash
# Copy the legacy local upload volume to MinIO and rewrite database URLs.
#
# Dry-run is the default. Use --apply only after stopping Multica and taking a
# database backup. Legacy files are retained for the configured grace period;
# --delete-legacy --apply is a separate, guarded cleanup step.
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
ENV_FILE="${MIGRATE_ENV_FILE:-$REPO_ROOT/docker/.env}"

if [[ ! -f "$ENV_FILE" ]]; then
  echo "[file-migration] env file not found: $ENV_FILE" >&2
  exit 1
fi

read_env_value() {
  local name="$1"
  local line=""
  line="$(grep -E "^${name}=" "$ENV_FILE" | tail -n 1 || true)"
  line="${line#*=}"
  line="${line#\"}"
  line="${line%\"}"
  printf '%s' "$line"
}

if [[ -z "${DATABASE_URL:-}" ]]; then
  DATABASE_URL="$(read_env_value DATABASE_URL)"
fi
if [[ -z "${DATABASE_URL:-}" ]]; then
  DATABASE_URL="$(read_env_value POSTGRES_URL)"
fi
if [[ -z "${MINIO_BUCKET:-}" ]]; then
  MINIO_BUCKET="$(read_env_value MINIO_BUCKET)"
fi
if [[ -z "${MINIO_ENDPOINT:-}" ]]; then
  MINIO_ENDPOINT="$(read_env_value MINIO_ENDPOINT)"
fi
if [[ -z "${MINIO_PUBLIC_ENDPOINT:-}" ]]; then
  MINIO_PUBLIC_ENDPOINT="$(read_env_value MINIO_PUBLIC_ENDPOINT)"
fi
if [[ -z "${MINIO_ACCESS_KEY:-}" ]]; then
  MINIO_ACCESS_KEY="$(read_env_value MINIO_ACCESS_KEY)"
fi
if [[ -z "${MINIO_SECRET_KEY:-}" ]]; then
  MINIO_SECRET_KEY="$(read_env_value MINIO_SECRET_KEY)"
fi
if [[ -z "${MINIO_USE_SSL:-}" ]]; then
  MINIO_USE_SSL="$(read_env_value MINIO_USE_SSL)"
fi
if [[ -z "${MINIO_REGION:-}" ]]; then
  MINIO_REGION="$(read_env_value MINIO_REGION)"
fi
if [[ -z "${CLOUDFRONT_DOMAIN:-}" ]]; then
  CLOUDFRONT_DOMAIN="$(read_env_value CLOUDFRONT_DOMAIN)"
fi
if [[ -z "${MIGRATE_MINIO_RETAIN_FOR:-}" ]]; then
  MIGRATE_MINIO_RETAIN_FOR="$(read_env_value MIGRATE_MINIO_RETAIN_FOR)"
fi

# The checked-in deployment env is written for containers. This wrapper runs
# on the host, where the shared middleware ports are published on loopback.
# Keep the container-facing endpoint as the public URL source before changing
# only the SDK connection endpoint to loopback. Otherwise --apply would store
# 127.0.0.1 in database rows that the backend/browser cannot resolve.
if [[ -z "${MINIO_PUBLIC_ENDPOINT:-}" ]]; then
  MINIO_PUBLIC_ENDPOINT="$MINIO_ENDPOINT"
fi
DATABASE_URL="${DATABASE_URL/host.docker.internal/127.0.0.1}"
MINIO_ENDPOINT="${MINIO_ENDPOINT/host.docker.internal/127.0.0.1}"

export DATABASE_URL MINIO_BUCKET MINIO_ENDPOINT MINIO_PUBLIC_ENDPOINT MINIO_ACCESS_KEY MINIO_SECRET_KEY
export MINIO_USE_SSL MINIO_REGION CLOUDFRONT_DOMAIN MIGRATE_MINIO_RETAIN_FOR
export LOCAL_UPLOAD_DIR="${MIGRATE_LOCAL_UPLOAD_DIR:-$REPO_ROOT/docker/volumes/backend_uploads}"

if ! command -v go >/dev/null 2>&1; then
  echo "[file-migration] Go is required to run the bundled migration command" >&2
  exit 1
fi

cd "$REPO_ROOT/server"
exec go run ./cmd/migrate_local_files_to_minio "$@"
