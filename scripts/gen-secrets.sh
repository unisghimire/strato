#!/bin/sh
# Generate the secrets Strato needs and print export lines / .env entries.
# Usage: ./scripts/gen-secrets.sh >> .env
set -eu

echo "STRATO_AUTH_JWT_SECRET=$(openssl rand -base64 48 | tr -d '\n')"
echo "STRATO_ENCRYPTION_MASTER_KEY=$(openssl rand -base64 32 | tr -d '\n')"
echo "STRATO_POSTGRES_PASSWORD=$(openssl rand -hex 16)"
echo "STRATO_S3_SECRET_KEY=$(openssl rand -hex 20)"
