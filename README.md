# Vernier

**Track 2 submission — compiled module:**
<https://github.com/flack-404/Vernier/raw/main/dist/vernier.wasm>

| | |
|---|---|
| registration ID | **9** |
| sha256 | `b9904e2962bc2f62aa0adc6062da68b6e0e6eca0fa85c12f040a4559254c28a6` |
| tx | [`0xc27c13a8…990998af`](https://sepolia.basescan.org/tx/0xc27c13a87834b08e60197ca2c1336b3d3a828efeada09feba69674b8990998af) on Base Sepolia (84532) |
| IPFS | `QmQaQsjHpFWWci9BU4kVL9gixJxJXSnzznLkf1EAmio5qQ` |
| intents | `WEB_SEARCH`, `FINANCIAL_DATA` |

The file above, the bytes IPFS serves at the registered URL, and a fresh `make rebuild`
all produce the same digest. Check any of them:

```bash
curl -sL https://github.com/flack-404/Vernier/raw/main/dist/vernier.wasm | sha256sum
```

---

**A scoring script for Telegraph Protocol, and the test rig that proves it will activate
before you spend a transaction finding out.**

Five scoring scripts have been submitted to Telegraph. All five were rejected. There
are zero active scripts on the network, and there is no supported way to test one
locally — every author discovered their failure by registering on-chain and reading a
cryptic string back off the registry.

This repository contains two things:

| | |
|---|---|
| **`vernier.wasm`** | A scoring module: Telegraph's own baseline, plus a correction layer capped at 0.12 that fires only where the baseline is provably blind |
| **`tg-score`** | A CLI that reproduces Telegraph's three activation gates on your laptop, and the Catch-Rate promotion table |

`tg-score` works on **anybody's** module, not just this one. Run it against the four
already-rejected binaries and it reproduces the network's verdict on all four —
including one rejection string character-for-character — without a transaction.

---

## The problem

Telegraph's judge scores by *meaning*. It embeds the question, the correct answer and
the miner's answer with MiniLM-L6-v2, measures cosine similarity, adds a BM25 lexical
signal and a length signal, and combines the four.

That works for essays. On facts it fails completely, because

> *"Solana is trading at **$94.06**"*
> *"Solana is trading at **$94,060**"*

point in almost exactly the same direction. Same words, same subject, same shape.

**It checks whether two answers are about the same thing, not whether they say the
same thing** — and roughly half of Telegraph's marketplace is prices, balances,
addresses and identifiers.

This is not hypothetical. From a live `WALLET_BALANCE_CHECK` round:

| | ground truth | miner's answer | score |
|---|---|---|---:|
| | balance **0 ETH** on **Arbitrum** | `0.009135` on arbitrum | **0.9920** |
| | balance **0 ETH** on **Arbitrum** | `3.977` on **ethereum** | **0.9901** |

One reports a balance that is not zero. The other reports a different balance *on a
different chain*. Both score 0.99.

## Why the obvious fix keeps failing

Write a stricter judge. Five people did. All five were rejected.

Telegraph will not activate a script unless its ranking of miners correlates at
**0.60 or better** with the incumbent's — per intent, on intents you do not choose.
A rigorous exact-match scorer disagrees everywhere, the correlation goes *negative*
(`WEB_SEARCH:-0.677` in a real rejection), and the gate closes before anyone reads
your logic.

> **A genuinely better judge disagrees with a flawed one, so it is rejected for being
> right.**

Full details, with the verbatim strings: **[docs/ACTIVATION-GATES.md](docs/ACTIVATION-GATES.md)**.

## What Vernier does instead

It **is** the baseline — same four signals, same weights, same embedded MiniLM
weights, forked from Telegraph's published source — with a bounded correction layer
that only speaks up where the baseline provably cannot see:

1. **Numeric contradiction.** The ground truth asserts a figure and the answer
   asserts a *different* one. Contradiction, never omission.
2. **Identifier infidelity.** Transaction hashes, contract addresses, CVE IDs. These
   match or they do not; there is no nearly-correct hash.
3. **Refusal, in both directions.** A refusal against a substantive ground truth is
   penalised — and when the *ground truth is itself a refusal*, the baseline score is
   returned untouched, so nobody profits from mimicking a broken reference.

### Why "Vernier"

A vernier is the second scale on a caliper — the short one that slides along the
main scale. It does not measure independently and it does not replace the main
scale; you read the two together, and it resolves the fraction the main scale is
too coarse to show.

That is exactly this design. The baseline scorer is the main scale, and it is not
wrong so much as **too coarse**: MiniLM cosine similarity cannot resolve `$94.06`
from `$94,060`, because at its resolution those are the same reading. Vernier adds
the digit the main scale cannot show, and only where there is a digit to add — on
the four largest intents its ground truth contains no numbers at all, so it reads
out zero and the main scale stands alone.

### Why the correction is exactly 0.12

Telegraph's genesis parameters fix the window, and both halves appear in the same
section of the whitepaper:

```
delta_c       = 0.15   deviate further from consensus and a validator running
                       your script is penalised (Category C)
delta_promote = 0.10   score at least this far BELOW canonical on one answer
                       canonical rated above 0.70, and your script is
                       automatically promoted to Canonical after 3 epochs

        0.10  <  |correction|  <  0.15      ->      0.12
```

Small enough to stay inside consensus. Large enough to trigger automatic promotion.

### Why it is safe — measured, not argued

Every detector is gated on the ground truth containing something checkable. Across
**14,258 live rows** in rankable intent groups:

| intent | rankable rows | ground truths containing a digit |
|---|---:|---:|
| TASK_COMPLETION | 2,778 | **0** |
| CHAT_COMPLETION | 2,756 | **0** |
| LANGUAGE_GENERATION | 2,668 | **0** |
| AGENT_TASK | 1,806 | **0** |
| *these four* | *10,008 — 70% of the corpus* | **0** |

Not "few". **None.** The numeric and identifier detectors *cannot fire* on the four
largest intents, so rank agreement there is unchanged **exactly**, not approximately.

That matters more than it first appears, because those are also the intents with the
least headroom. Measured against the live corpus:

```
  1,468 rows · 20 intents · epochs 271-285

                                     BASELINE  VERNIER
  mean rank agreement                  0.8406     0.8450
  weakest intent (CHAT_COMPLETION)     0.6065     0.6065
  intents failing the 0.60 gate             0          0
  Catch-Rate catches                            5 on 2 intents
```

**The correction costs no headroom at all, and raises mean agreement above a pure
fork** — because where it does fire, it agrees with the network better than the raw
baseline does.

### Measure on the epochs you will be judged on

Rank agreement is not stationary. Pooling 40 epochs per intent instead of the recent
15, the **unmodified baseline fails** — `WEATHER_CHECK` at 0.5180, `WEATHER_FORECAST`
at 0.5695, both in the network's evaluation set. Split by epoch window the reason is
plain:

| intent | epochs ≤250 | 251–270 | 271–285 |
|---|---:|---:|---:|
| WEATHER_CHECK | +0.3584 | +0.5283 | **+0.8825** |
| WEATHER_FORECAST | +0.4847 | +0.7283 | **+0.8497** |

Agreement has been improving sharply; stale rounds are what sink the pooled figure.
The network replays current rounds, so current epochs are the operative measurement —
`tg-score subset -since 271`. Any claim that "the baseline passes the gate" which does
not say *when* is unfalsifiable. See [docs/FINDINGS.md](docs/FINDINGS.md) §3c.

---

### What this cannot tell you

Every rejection map on the registry lists exactly seven intents, and one of them —
`IMAGE_VERIFICATION` — has **zero rankable groups** in the public `/scores` corpus.
The network ranks it against reference data the endpoint does not expose, so no local
harness can produce a figure for it.

`tg-score` closes most of the gap between "I think this will activate" and "I know it
will". It does not close all of it. Plan to register, read the rejection string and
iterate — on testnet `BondAmount` is 0, so iteration is free.

## Quick start

```bash
make cli                                    # build tg-score
tg-score pull -o data/corpus-full.json      # snapshot the live /scores endpoint
tg-score subset -c data/corpus-full.json -o data/corpus-iter.json -per-intent 12

make wasm                                   # build both WASM variants
make gate                                   # run the three gates against vernier.wasm
make verify                                 # prove the fork is intact and Rust == Go
```

### The commands

```
tg-score pull        snapshot /scores into a local replay corpus
tg-score corpus      summarise a corpus: rows, intents, epochs, group sizes
tg-score subset      select whole rankable groups into a smaller iteration corpus
tg-score gate        the three activation gates, by name, on any module
tg-score simulate    apply the correction layer to cached scores and re-gate
tg-score explain     show what the layer saw on a row and why it fired
tg-score catch       Catch-Rate table: rows that would trigger promotion
tg-score compare     diff two scoring modules row by row
tg-score verify      prove the fork is intact and Rust matches its Go reference
tg-score registry    live activation status and rejection reasons
```

`gate` exits non-zero when a module would be rejected, so it drops into CI directly.

---

## Layout

```
vernier/        the WASM scoring module (Rust, no_std)
  src/lib.rs        forked from telegraph-wasm-baseline; weights untouched
  src/corrections.rs  the correction layer and the derivation of every constant
  src/scan.rs       byte scanners for numbers and identifiers — no regex, no_std
tg-score/         the harness (Go, wazero)
  internal/gate/    the three gates and the Catch-Rate table
  internal/detect/  Go reference implementation of the correction layer
  internal/wasmrt/  the memory ABI, which is what silently kills submissions
docs/
  ACTIVATION-GATES.md   the three gates, documented from the live registry
  DESIGN.md             why the correction is shaped the way it is
  FINDINGS.md           measurements, including ones that overturned our own plan
  FORK-BASE.txt         exactly what was changed from upstream, and why each is safe
  results/              captured output behind every number quoted here
scripts/demo-rejected.sh  replay the four rejected binaries
```

## How it is verified

`tg-score verify` makes three claims and checks each against real rows:

```
[A] FORK INTEGRITY     PASS  306/306 rows scored bit-identically to
                             telegraph-wasm-baseline. The correction layer is the
                             only change this fork makes.
[B] CORRECTION BOUNDS  PASS  no row had its score raised
                       PASS  no row exceeded the 0.12 clamp
[C] CROSS-CHECK        PASS  19526/19526 rows agree exactly between the shipping
                             Rust layer and its Go reference implementation
```

[C] is what makes `tg-score simulate` legitimate. Tuning the layer by rebuilding the
module costs eight minutes a round; simulating against cached baseline scores costs
milliseconds. That shortcut is only honest if the Go implementation computes the
same function the Rust actually ships — so the tool proves it, on every row, and the
build fails if they ever diverge.

## Results

The shipping module against the three gates, on current epochs:

```
$ make gate

[1] STRUCTURAL SELF-MATCH   PASS   self 0.8605 vs cross 0.1740, 20/20 pairs correct
[2] SCORE DISPERSION        PASS   stdev 0.3056 against a 0.05 floor
[3] RANK AGREEMENT          PASS   mean +0.8450, weakest CHAT_COMPLETION +0.6065

VERDICT: all three gates pass — this module would activate.
```

Raw output for this and every other figure quoted here is in
[docs/results/](docs/results/).

## Submitted

Registered on Base Sepolia and pinned to IPFS. Transaction hashes, digests and the
reasoning behind the intent choice are in **[docs/SUBMISSION.md](docs/SUBMISSION.md)**.

## Reproducing every number in this README

Every quantitative claim here was measured against the live network or the real
scoring binary. The commands are in **[docs/FINDINGS.md](docs/FINDINGS.md)**, which
also records the measurements that **overturned** parts of the original plan.

## Licence

MIT, matching `telegraph-wasm-baseline`, which this forks. See [LICENSE](LICENSE).
