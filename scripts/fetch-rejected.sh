#!/usr/bin/env bash
# Fetch the scoring modules that Telegraph rejected, from the IPFS URLs recorded
# on the public registry.
#
# These are other authors' binaries. They are fetched rather than vendored into
# this repository, both to avoid redistributing someone else's work and because
# the registry is the authoritative source for what was actually submitted.
#
# Cross-check the live registry with:  tg-score registry
set -euo pipefail
cd "$(dirname "$0")/../data/rejected" 2>/dev/null || { mkdir -p "$(dirname "$0")/../data/rejected"; cd "$(dirname "$0")/../data/rejected"; }

GATEWAY=${GATEWAY:-https://gateway.pinata.cloud/ipfs}

# registration id : intent : CID   (from GET /engine/v1/intents/{id}/wasm)
ENTRIES=(
  "reg5-chat-completion:QmTHHdpnUAwEXfaReukEzWUZt8vL7W4ohgdyHcrK8oYuto"
  "reg8-sports-score:QmWmaiziPFq4y5h5EhxoVQvogihCneWHfRxbsyLHKr6N45"
  "reg11-weather-check:QmXpW6arrQ79f1qNLCpfL3XXiFqtccDRGy6r9gviRksvBt"
  "reg13-sports-score:Qme8tfEeeudgHqkudmXfzBVCzoLxN5DTT3PTWLUnRit4fS"
)

for e in "${ENTRIES[@]}"; do
  name="${e%%:*}"; cid="${e##*:}"
  printf '%-24s ' "$name"
  if curl -sSLf --max-time 120 -o "$name.wasm" "$GATEWAY/$cid"; then
    printf '%8s bytes  sha256 %s\n' "$(stat -c%s "$name.wasm")" "$(sha256sum "$name.wasm" | cut -c1-16)"
  else
    printf 'FAILED (gateway unreachable?)\n'
  fi
done

cat <<'NOTE'

Note: these sha256 digests do NOT match the WasmHash field the registry records
for the same registrations, and do not match the CIDv0 digest either. The network
evidently fetched and evaluated these modules, so the field does not appear to
gate evaluation — but what it hashes is an open question. See docs/FINDINGS.md §10.
NOTE
