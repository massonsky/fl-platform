#!/bin/sh
set -eu
MINIO_ROOT_USER=$(cat /run/secrets/minio_user)
MINIO_ROOT_PASSWORD=$(cat /run/secrets/minio_password)
export MINIO_ROOT_USER MINIO_ROOT_PASSWORD
exec minio "$@"
