#!/usr/bin/env bash
# Replay the four rejected scoring scripts through tg-score.
#
# Every script ever submitted to Telegraph was rejected, and each author had to
# spend an on-chain registration to find out why. These are their binaries,
# pulled from the IPFS URLs on the public registry. This script runs the gate
# against them locally and prints the reason each one failed — the same reason
# the network gave, without the transaction.
set -euo pipefail
cd "$(dirname "$0")/.."

CORPUS=${CORPUS:-data/corpus-iter.json}
CLI=./tg-score/tg-score

[ -x "$CLI" ] || { echo "build the CLI first: make cli" >&2; exit 1; }

# Registration ID -> the verbatim RejectionReason the network recorded.
declare -A REASON=(
  [reg5-chat-completion]="rank agreement below threshold (0.60), got: map[AGENT_TASK:0.111 ...]"
  [reg8-sports-score]="structural validation failed: self-match (0.0000) did not beat unrelated cross-match (0.0000)"
  [reg11-weather-check]="candidate scores collapsed: stdev=0.0000 <= threshold 0.0500"
  [reg13-sports-score]="rank agreement below threshold (0.60), got: map[AGENT_TASK:0.044 ...]"
)

for f in data/rejected/*.wasm; do
  n=$(basename "$f" .wasm)
  printf '\n%s\n' "$(printf '=%.0s' {1..78})"
  printf 'REGISTRATION %s\n' "$n"
  printf 'network said: %s\n' "${REASON[$n]:-unknown}"
  printf '%s\n' "$(printf '=%.0s' {1..78})"
  "$CLI" gate -c "$CORPUS" -pool-epochs "$f" || true
done
