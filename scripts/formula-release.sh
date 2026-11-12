#!/bin/sh
# Print the stable `url` lines of Formula/rex-tg.rb for a release tag.
# Usage: scripts/formula-release.sh v0.1.0
# The repo is private, so Homebrew cannot download a tag archive without a
# login. The formula names the tag and its commit instead; Homebrew clones
# them with git, which uses the user's own GitHub login.
# It reads git only. It does not tag, push, or edit files.
set -eu
tag="${1:-}"
[ -n "$tag" ] || { echo "usage: $0 TAG (for example v0.1.0)" >&2; exit 2; }
rev=$(git rev-parse --verify --quiet "refs/tags/${tag}^{commit}") || {
	echo "$0: no tag $tag here; run: git tag $tag && git push origin $tag" >&2
	exit 1
}
remote=$(git ls-remote origin "refs/tags/${tag}^{}" "refs/tags/${tag}" | head -1 | cut -f1)
[ -n "$remote" ] || { echo "$0: tag $tag is not on GitHub yet; run: git push origin $tag" >&2; exit 1; }
printf '  url "https://github.com/arrokh/rex-tg.git",\n      tag:      "%s",\n      revision: "%s"\n' "$tag" "$rev"
