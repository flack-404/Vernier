#!/usr/bin/env bash
# Submit a scoring module to Telegraph: pin to IPFS, register on-chain, poll status.
#
# NOTHING here broadcasts a transaction unless you pass --send. Every command is
# safe to run and inspect first; `register` prints exactly what it would do and
# stops.
#
#   ./scripts/submit.sh pin                     upload the wasm to Pinata and verify it reads back
#   ./scripts/submit.sh register <url>          DRY RUN: show the call, estimate gas
#   ./scripts/submit.sh register <url> --send   broadcast it
#   ./scripts/submit.sh status                  poll the registry
#
# Environment:
# Reads .env from the repo root if present. Accepts either naming convention:
#   JWT               or PINATA_JWT   Pinata JWT, from app.pinata.cloud > API Keys
#   wallet_private_key or PRIVATE_KEY the wallet that will own the registration
#   INTENTS           comma-separated canonical intent names (default WEB_SEARCH)
#                     registerWasm takes an ARRAY, so one registration can cover
#                     several intents
#   RPC_URL           Base Sepolia RPC               (default https://sepolia.base.org)
#   WASM              module to submit               (default build/vernier.wasm)
set -euo pipefail
cd "$(dirname "$0")/.."

# Load .env if present. Values are never echoed by this script; only variable
# NAMES and derived public data (addresses, hashes, CIDs) are ever printed.
if [ -f .env ]; then
  set -a
  # shellcheck disable=SC1091
  . ./.env
  set +a
fi

# Accept either the canonical names or the ones a Pinata/wallet UI hands you, so
# a working .env does not have to be rewritten to match this script.
PINATA_JWT=${PINATA_JWT:-${JWT:-${PINATA_API_JWT:-}}}
PRIVATE_KEY=${PRIVATE_KEY:-${wallet_private_key:-${WALLET_PRIVATE_KEY:-}}}
PINATA_API_KEY=${PINATA_API_KEY:-${API_Key:-${API_KEY:-}}}
PINATA_API_SECRET=${PINATA_API_SECRET:-${API_Secret:-${API_SECRET:-}}}
export PINATA_JWT PRIVATE_KEY

# cast wants a 0x-prefixed key; wallet exports often omit it.
case "${PRIVATE_KEY:-}" in
  "" ) ;;
  0x* ) ;;
  * ) PRIVATE_KEY="0x$PRIVATE_KEY" ;;
esac

DIAMOND=0xac683bFa8F1C892E23e8300d14c20678C6FC0CA3   # Base Sepolia, chain 84532
NODE=${NODE:-http://13.237.89.59:7044}
RPC_URL=${RPC_URL:-https://sepolia.base.org}
# registerWasm's third argument is string[], not string. The published docs say
# `string intent`; the deployed selector is registerWasm(bytes32,string,string[])
# = 0x19238d1c, and calling the single-string form reverts with
# "Diamond: Function does not exist". Verified against the live loupe.
INTENTS=${INTENTS:-${INTENT:-WEB_SEARCH}}
WASM=${WASM:-build/vernier.wasm}
GATEWAY=${GATEWAY:-https://gateway.pinata.cloud/ipfs}

die() { printf '\nerror: %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null || die "$1 is not installed"; }

# ── pin ──────────────────────────────────────────────────────────────────────
cmd_pin() {
  have curl; have jq
  [ -f "$WASM" ] || die "$WASM not found — run: make wasm"
  [ -n "${PINATA_JWT:-}" ] || die "no Pinata JWT found. Set JWT (or PINATA_JWT) in .env —
       app.pinata.cloud > API Keys > New Key, and copy the JWT, not the key/secret pair."

  local size sha
  size=$(stat -c%s "$WASM")
  sha=$(sha256sum "$WASM" | cut -d' ' -f1)
  printf 'file    %s\n' "$WASM"
  printf 'size    %s bytes (%.1f MB)\n' "$size" "$(echo "$size/1048576" | bc -l)"
  printf 'sha256  0x%s\n\n' "$sha"

  printf 'uploading to Pinata (this is ~24 MB, give it a minute) ...\n'
  local resp cid
  resp=$(curl -sS --max-time 900 \
    -H "Authorization: Bearer $PINATA_JWT" \
    -F "file=@$WASM" \
    -F 'pinataMetadata={"name":"vernier.wasm"}' \
    https://api.pinata.cloud/pinning/pinFileToIPFS) || die "upload failed"

  cid=$(echo "$resp" | jq -r '.IpfsHash // empty')
  [ -n "$cid" ] || die "no CID in Pinata response: $resp"
  printf '\npinned  %s\n' "$cid"
  printf 'url     %s/%s\n\n' "$GATEWAY" "$cid"

  # Read it straight back. A URL the validator cannot fetch, or that returns
  # different bytes than you pinned, fails activation in a way whose rejection
  # string will not tell you that IPFS was the problem.
  printf 'verifying the gateway serves the same bytes back ...\n'
  local tmp got
  tmp=$(mktemp)
  curl -sSL --max-time 900 -o "$tmp" "$GATEWAY/$cid" || die "gateway fetch failed"
  got=$(sha256sum "$tmp" | cut -d' ' -f1)
  rm -f "$tmp"
  if [ "$got" != "$sha" ]; then
    die "gateway returned DIFFERENT bytes (sha256 $got). Do not register this URL."
  fi
  printf 'OK      gateway bytes match, sha256 0x%s\n\n' "$got"
  printf 'next:   ./scripts/submit.sh register %s/%s\n' "$GATEWAY" "$cid"
}

# ── register ─────────────────────────────────────────────────────────────────
cmd_register() {
  have cast
  local url=${1:-}
  local send=${2:-}
  [ -n "$url" ] || die "usage: ./scripts/submit.sh register <wasmUrl> [--send]"
  [ -f "$WASM" ] || die "$WASM not found"
  [ -n "${PRIVATE_KEY:-}" ] || die "no wallet key found. Set wallet_private_key (or PRIVATE_KEY) in .env"

  local sha addr bal chain
  sha=0x$(sha256sum "$WASM" | cut -d' ' -f1)
  addr=$(cast wallet address --private-key "$PRIVATE_KEY")
  chain=$(cast chain-id --rpc-url "$RPC_URL")
  bal=$(cast balance "$addr" --rpc-url "$RPC_URL")

  [ "$chain" = "84532" ] || die "RPC is chain $chain, expected 84532 (Base Sepolia)"

  cat <<EOF

  ── registerWasm ────────────────────────────────────────────────────────────
  contract   $DIAMOND   (Base Sepolia, chain $chain)
  from       $addr
  balance    $(cast to-unit "$bal" ether) ETH
  selector   $(cast sig 'registerWasm(bytes32,string,string[])')   (facet 0xb711...B1BF)

  wasmHash   $sha
  wasmUrl    $url
  intents    [$INTENTS]
  ────────────────────────────────────────────────────────────────────────────
EOF

  [ "$bal" != "0" ] || die "wallet has no ETH — fund it at a Base Sepolia faucet first"

  printf '\nestimating gas ...\n'
  cast estimate "$DIAMOND" 'registerWasm(bytes32,string,string[])' "$sha" "$url" "[$INTENTS]" \
      --rpc-url "$RPC_URL" --from "$addr" \
    || die "gas estimate reverted — the call would fail. Check every intent name is
       canonical (GET /engine/v1/intents) and that this address has not already
       registered this hash."

  if [ "$send" != "--send" ]; then
    printf '\nDRY RUN. Nothing was broadcast.\n'
    printf 'To send:  ./scripts/submit.sh register %s --send\n' "$url"
    return 0
  fi

  printf '\nbroadcasting ...\n'
  cast send "$DIAMOND" 'registerWasm(bytes32,string,string[])' "$sha" "$url" "[$INTENTS]" \
    --rpc-url "$RPC_URL" --private-key "$PRIVATE_KEY"

  printf '\nregistered. Poll with: ./scripts/submit.sh status\n'
}

# ── status ───────────────────────────────────────────────────────────────────
cmd_status() {
  have curl; have jq
  local first=${INTENTS%%,*}
  curl -sS --max-time 60 "$NODE/engine/v1/intents/$first/wasm" \
    | jq -r '.wasm[] | "#\(.RegistrationID)  \(.IntentID)  [\(.ActivationStatus|ascii_upcase)]
    author  \(.AuthorAddress)
    hash    \(.WasmHash)
    url     \(.WasmURL)
    errors  \(.EvalErrorCount)\(if .RejectionReason != "" then "\n    REASON  " + .RejectionReason else "" end)\n"'
}

case "${1:-}" in
  pin)      shift; cmd_pin "$@" ;;
  register) shift; cmd_register "$@" ;;
  status)   shift; cmd_status "$@" ;;
  *) sed -n '2,17p' "$0" | sed -e 's/^#$//' -e 's/^# //' ;;
esac
