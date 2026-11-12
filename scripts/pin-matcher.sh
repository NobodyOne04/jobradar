#!/usr/bin/env bash
# Pin the matcher (ADR 0001): record one commit of the sibling checkout, its package
# version, and a copy of its contract. Usage: scripts/pin-matcher.sh <commit|branch>
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
src="${MATCHER_SRC:-$root/../jobradar-matcher}"
ref="${1:?usage: scripts/pin-matcher.sh <commit|branch>}"

git -C "$src" fetch -q origin
sha="$(git -C "$src" rev-parse --verify "$ref^{commit}")"
if ! git -C "$src" merge-base --is-ancestor "$sha" origin/main; then
  echo "pin-matcher: $sha is not on the matcher's origin/main" >&2
  exit 1
fi

version="$(git -C "$src" show "$sha:pyproject.toml" | sed -n 's/^version = "\(.*\)"$/\1/p' | head -n 1)"
if [[ -z "$version" ]]; then
  echo "pin-matcher: no version in pyproject.toml at $sha" >&2
  exit 1
fi

# Read everything before writing anything, so a failure never leaves a half-updated pin.
contract="$(git -C "$src" show "$sha:openapi.yaml")"

mkdir -p "$root/contracts"
printf '%s\n' "$contract" > "$root/contracts/matcher.openapi.yaml"
echo "$version" > "$root/contracts/MATCHER_VERSION"
echo "$sha" > "$root/contracts/MATCHER_REF"
echo "pinned matcher $version at $sha"
