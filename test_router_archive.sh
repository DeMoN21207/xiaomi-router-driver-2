#!/usr/bin/env bash
set -euo pipefail

archive_path="${1:-build/vpn-manager-linux-arm64.tar.gz}"

if [[ ! -f "$archive_path" ]]; then
	echo "archive not found: $archive_path" >&2
	exit 1
fi

found_binary=0
found_bundle_info=0
while IFS= read -r entry; do
	clean="${entry#./}"
	clean="${clean%/}"
	if [[ -z "$clean" ]]; then
		echo "archive contains an empty root entry: $entry" >&2
		exit 1
	fi
	[[ "$clean" == "vpn-manager" ]] && found_binary=1
	[[ "$clean" == "bundle-info.txt" ]] && found_bundle_info=1
done < <(tar -tzf "$archive_path")

if [[ "$found_binary" != "1" || "$found_bundle_info" != "1" ]]; then
	echo "archive is missing vpn-manager or bundle-info.txt" >&2
	exit 1
fi
