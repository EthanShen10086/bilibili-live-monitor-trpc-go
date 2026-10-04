#!/bin/sh
set -eu
REPO_ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$REPO_ROOT"
REPOSITORY=EthanShen10086/bilibili-live-monitor-trpc-go
command -v gh >/dev/null
ACCOUNT=$(gh api user --jq .login)
[ "$ACCOUNT" = EthanShen10086 ] || { printf '%s\n' 'Please use the EthanShen10086 GitHub CLI account.'; exit 1; }
[ -d .git ] || { printf '%s\n' 'Missing reviewed Git repository.'; exit 1; }
[ -z "$(git status --porcelain)" ] || { printf '%s\n' 'Working tree changed: review and commit before publishing.'; exit 1; }
python3 scripts/check-public-files.py
if git remote get-url origin >/dev/null 2>&1; then
  EXPECTED="https://github.com/$REPOSITORY.git"
  [ "$(git remote get-url origin)" = "$EXPECTED" ] || { printf '%s\n' 'Unexpected origin URL; refusing upload.'; exit 1; }
  [ "$(gh repo view "$REPOSITORY" --json visibility --jq .visibility)" = PUBLIC ] || { printf '%s\n' 'Expected public repository.'; exit 1; }
  gh auth setup-git --hostname github.com
  git push --set-upstream origin main
  gh repo view "$REPOSITORY" --json url,visibility
  exit 0
fi
[ -z "$(git remote)" ] || { printf '%s\n' 'Unexpected remote; refusing upload.'; exit 1; }
# repo create refuses an existing repository; no force push or overwrite.
gh auth setup-git --hostname github.com
gh repo create "$REPOSITORY" --public --source "$REPO_ROOT" --remote origin --push
printf '%s\n' "Published https://github.com/$REPOSITORY"
gh repo view "$REPOSITORY" --json url,visibility
