#!/usr/bin/env bash
set -euo pipefail

# Digests are from the Sigstore v3.1.4 release assets. Pin the executable
# directly so publication does not depend on a second bootstrap binary.
case "$(uname -m)" in
  x86_64)
    architecture=amd64
    checksum=424e2c97d0b45fd7a6b608a87f090f35633f4c23ece5f092d48bba11aaf4bbfa
    ;;
  aarch64)
    architecture=arm64
    checksum=ade83b7aac62e131cacfe96d958948b2150cc9efec46a570a2649b8e27dedad2
    ;;
  *)
    echo 'Unsupported Cosign runner architecture' >&2
    exit 1
    ;;
esac

test "$(uname -s)" = Linux
directory="${RUNNER_TEMP:?}/cosign-bin"
mkdir -p "$directory"
curl --fail --location --proto '=https' --proto-redir '=https' --tlsv1.2 \
  --connect-timeout 10 --max-time 240 --retry 4 --retry-delay 2 --retry-all-errors \
  --output "$directory/cosign.tmp" \
  "https://github.com/sigstore/cosign/releases/download/v3.1.4/cosign-linux-$architecture"
printf '%s  %s\n' "$checksum" "$directory/cosign.tmp" | sha256sum --check --status
chmod 0755 "$directory/cosign.tmp"
mv "$directory/cosign.tmp" "$directory/cosign"
printf '%s\n' "$directory" >> "${GITHUB_PATH:?}"
