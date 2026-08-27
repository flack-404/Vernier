# Submission record

Track 2 (Script Authors), Telegraph Hackathon Season I. Everything below is
reproducible from this repository; nothing is asserted that was not read back off
the chain or the gateway.

## On-chain registrations

Both were broadcast from `0x95703737Ab3B9D2f1141C70d3F1bd5FbB1a9dd6D` to the
Telegraph Diamond `0xac683bFa8F1C892E23e8300d14c20678C6FC0CA3` on **Base Sepolia**
(chain 84532), via
`registerWasm(bytes32 wasmHash, string wasmUrl, string[] intents)` — selector
`0x19238d1c`.

| | first (as *Plumbline*) | current (as **Vernier**) |
|---|---|---|
| tx | `0x788a730d61637b0a1bde5b271591b29271644ddeac07b1dbd5abba0deeb79d98` | `0xc27c13a87834b08e60197ca2c1336b3d3a828efeada09feba69674b8990998af` |
| block | 46037922 | 46040195 |
| status | 1 (success) | 1 (success) |
| gas | 377,332 | 377,332 |
| wasmHash | `0xce745cb0…63fa6b0` | `0xb9904e29…54c28a6` |
| IPFS | `QmV6TFpco8SykfbYKsLzf2UeafXwdtJy12FijfYfBrp2EP` | `QmQaQsjHpFWWci9BU4kVL9gixJxJXSnzznLkf1EAmio5qQ` |
| intents | `[WEB_SEARCH, FINANCIAL_DATA]` | `[WEB_SEARCH, FINANCIAL_DATA]` |

The second registration exists because the project was renamed. Renaming the crate
permutes the WASM type section — same size, same behaviour, different digest — so the
first registration's hash could no longer be reproduced from this source tree. Rather
than leave a registered artifact this repository cannot rebuild, it was re-pinned and
re-registered under the shipping name. Registration is free (`BondAmount` 0 on
testnet).

## Why these two intents

Promotion to Canonical is per-intent (whitepaper §4.3) and requires a Catch-Rate
catch **on that intent**: a score at least `delta_promote` (0.10) below Canonical on
an answer Canonical rated above 0.70. Measured on epochs 271–285, Vernier produces
catches on exactly two:

```
  WEB_SEARCH       4 catches
  FINANCIAL_DATA   1 catch
  every other      0
```

Zero elsewhere is structural, not incidental: on the large prose intents the
correction layer is provably silent (their ground truths contain no digits), so a
catch there is impossible by construction. Registering intents where no catch can
occur would add emissions surface but no path to Canonical.

## Verifying the registration independently

```bash
cast tx 0xc27c13a87834b08e60197ca2c1336b3d3a828efeada09feba69674b8990998af \
    --rpc-url https://sepolia.base.org

# the module the registration points at, byte-for-byte:
curl -sL https://gateway.pinata.cloud/ipfs/QmQaQsjHpFWWci9BU4kVL9gixJxJXSnzznLkf1EAmio5qQ \
  | sha256sum
#   b9904e2962bc2f62aa0adc6062da68b6e0e6eca0fa85c12f040a4559254c28a6

# and that this source tree builds exactly that:
make rebuild
sha256sum build/vernier.wasm
```

## Registry indexing

As of the time of writing, `GET /engine/v1/intents/{intent}/wasm` has not surfaced
either registration, still reporting `count: 5` — the same five entries it has shown
since 16 August. Both transactions are settled on chain with the `EntityRegistered`
event present in the raw logs, and the node is otherwise healthy (epoch advancing,
scoring running, miner count grown 92 → 96).

The five existing entries were indexed essentially instantly — their `RegisteredAt`
and `UpdatedAt` differ by roughly 300 ms — so a multi-hour delay is not this
endpoint's normal behaviour. This appears to be a node-side indexing issue rather
than anything about the submissions, and it is the one thing in this project that
cannot be resolved from outside.

Poll it yourself:

```bash
./scripts/submit.sh status
tg-score registry
```
