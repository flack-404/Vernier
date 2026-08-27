# Design

Why Vernier is shaped the way it is, and what each constant is doing.

The short version: **a scoring script for Telegraph cannot be better than the
incumbent by being stricter, because the activation gate measures agreement rather
than accuracy.** It can only be better by being *identical* almost everywhere and
disagreeing where the incumbent is provably blind.

---

## 1. The constraint

Telegraph activates a script only if its ranking of miners correlates at 0.60 or
better with the incumbent's — per intent, on intents the author does not choose.

That rule is sensible. It stops someone uploading a broken or hostile scorer that
scrambles the rankings and wrecks every miner's earnings. But it has a vicious side
effect:

> A genuinely better judge disagrees with a flawed one, so it is rejected for being
> right.

Three of the five rejections on the public registry are exactly this, and their
correlations are not merely low, they are *negative* — `WEB_SEARCH:-0.677`. That is
the signature of a scorer that ranks the terse correct answer above the long fluent
one while the incumbent does the opposite.

So the design question is not "what would a good scorer do?" It is:

> **What is the largest correction that can be applied without moving the ranking?**

## 2. Where 0.12 comes from

Telegraph's genesis parameters answer it, and both halves sit in the same section of
the whitepaper:

```
delta_c       = 0.15   Consensus deviation. A validator whose local score deviates
                       further than this from the stake-weighted median is
                       penalised (Category C).

delta_promote = 0.10   Catch-Rate Promotion. A challenger script that scores at
                       least this far BELOW canonical, on at least one answer
                       canonical rated above 0.70, is automatically promoted to
                       Canonical for that intent after T_promote = 3 epochs.
```

Read together they define a window. A correction smaller than 0.10 can never trigger
promotion, so the script activates and then sits as a permanent challenger. A
correction larger than 0.15 puts every validator running it outside consensus.

```
        0.10  <  |correction|  <  0.15      ->      0.12
```

One number, simultaneously small enough to stay accepted and large enough to be
promoted. This is the promotion mechanic read literally and solved.

**The correction is only ever negative.** Vernier can lower a score the baseline
gave too generously; it never raises one. Raising would assert the baseline was too
harsh — a judgement the module has no evidence for — and would put the layer on the
wrong side of the Catch-Rate condition, which is defined as scoring *below*
canonical.

## 3. Why the layer is safe: self-gating, not intent-aware

The original plan called for an **adaptive cap** that shrank the correction on
intents whose measured agreement was already near 0.60.

**That cannot be built.** The ABI is:

```rust
rank_answer(q_ptr, q_len, gt_ptr, gt_len, ma_ptr, ma_len) -> f32
```

There is no intent parameter. The module never learns which intent it is scoring, so
it cannot condition on one.

The replacement is better than the original idea. **Every detector is gated on the
ground truth containing something checkable**, and that turns out to separate the
intents almost perfectly. Measured across 14,258 live rows in rankable groups:

| intent | rankable rows | ground truths containing a digit |
|---|---:|---:|
| TASK_COMPLETION | 2,778 | **0** |
| CHAT_COMPLETION | 2,756 | **0** |
| LANGUAGE_GENERATION | 2,668 | **0** |
| AGENT_TASK | 1,806 | **0** |
| WEB_SEARCH | 1,290 | 101 |
| ONCHAIN_TX_LOOKUP | 180 | 180 |

The four largest intents — 70% of the corpus — contain **no digit at all**. The
numeric and identifier detectors cannot fire there. Not "rarely fire": cannot.

This matters because those are also the intents with the least headroom.
`CHAT_COMPLETION` sits at 0.6065 against a 0.60 threshold — six thousandths of margin
— and the layer's measured effect on it is exactly **+0.0000**.

Conditioning on the *shape of the ground truth* achieves what conditioning on the
intent was meant to achieve, without needing to know the intent.

## 4. The detectors

### 1 — Numeric fidelity

Penalises **contradiction, never omission**. An answer that does not mention a figure
is incomplete; an answer that states a *different* one is wrong. Only the second is
the blind spot this layer exists to close, and the distinction is not academic — an
earlier coverage-based rule broke in both directions at once. See
[FINDINGS.md §6](FINDINGS.md).

How it works:

1. Extract the ground truth's **headline** figures — at most two. Currency and
   percentage figures come first because their notation marks them as the claim;
   otherwise the largest magnitude wins, after years and small integers have been
   dropped as the clock and the list indices.
2. Compare each against the answer's figures **of the same notation**. A price is
   checked against prices, a percentage against percentages.
3. If the answer offers no comparable figure at all, fall back to its untyped
   numbers — most miners answer in raw JSON (`"price_usd":496.37`) with no currency
   symbol anywhere, and without this the detector would be silent on the majority of
   numeric answers.
4. If the answer offers *too many* comparable figures, stay silent (see below).
5. Match within a **0.5% relative tolerance**, because ground truth is captured at one
   instant and the miner answers at another, and prices move in between.

**The flood guard.** Matching by relative tolerance means an answer spraying K numbers
covers roughly `K × 2 × tolerance` of the value space by chance: 900 consecutive
integers match any headline below 900 outright. One live `STOCK_PRICE` answer carried
**6,972 numeric tokens**. Past 48 comparable figures the detector treats the answer as
asserting nothing and stays silent — neither penalising it nor letting it buy a match.
A figure found among thousands is not a claim.

### 2 — Identifier fidelity

Transaction hashes, contract addresses, CVE identifiers, ISO dates. These are exactly
right or wrong; there is no nearly-correct transaction hash. Hallucinating one
currently costs a miner nothing.

Identifiers are scanned **before** numbers and their spans withheld from the number
scanner. Without that, `0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48` decomposes into a
dozen meaningless integers and drowns out the real numeric signal on exactly the
intents where identifiers matter most.

Hex comparison allows a prefix match at 24+ digits. Some ground truths on this network
carry malformed addresses — one live `WALLET_BALANCE_CHECK` ground truth holds a
41-hex-digit address, one digit too long — and an exact-match rule would penalise
miners for normalising them.

### 3 — Refusal, in both directions

**The quality half.** A refusal against a substantive ground truth is a non-answer.
The baseline scores it on topical similarity, and a fluent refusal is highly similar
to the question it declines.

**The anti-gaming half.** When the *ground truth is itself a refusal*, the baseline
score is returned untouched. This is 1.3% of live rows, concentrated in
`WEATHER_FORECAST`. Their published scores span the entire range — a refusal mimicking
a refusal scored a perfect **1.0000** on one `FINANCIAL_DATA` round — so the reference
ordering there carries no information. Applying a correction against a noisy reference
can only erode rank agreement, and rewarding the mimicry is precisely the gaming
vector to close.

### Not shipped: padding and staleness

The original design listed two more detectors, both marked cuttable. They are cut.

The baseline pays 0.10 for length and its BM25 rewards repetition, so padding is a
real exploit — `repetition_scores_above_exact_match` in `bm25.rs` demonstrates it. But
a padding detector fires on *prose* intents, which is exactly where there is no
headroom to spend. It would trade the design's central safety property for a signal
the length curve already partly captures. Staleness needs a clock the sandbox does not
provide.

## 5. Determinism

Validators take a stake-weighted median and penalise deviation beyond `delta_c`, so
two validators running the same binary on the same input must agree bit for bit.

- The baseline uses `libm` for every transcendental so results are IEEE-754 identical
  across platforms. **The correction layer adds no new float dependency at all** — it
  uses only `+`, `-`, `*`, `/` and comparisons, which are exactly specified in
  wasm32.
- Powers of ten in scientific notation are computed by repeated multiplication rather
  than `pow`, which is exact for small exponents and identical everywhere.
- Number *values* are `f64`; answers carry figures like `3977664413963610716` wei,
  which `f32` cannot hold to seven significant digits. The *correction* is `f32`,
  matching the composite it adjusts.
- Sort ties are broken by source position so the result never depends on sort
  stability.

That last set of choices is load-bearing in a way that is easy to underestimate:
`tg-score verify` caught a real disagreement between the Rust module and its Go
reference that came down to `f32` vs `f64` summation order. See
[FINDINGS.md §8](FINDINGS.md).

## 6. Why there are two implementations

The correction layer exists twice: in Rust (`vernier/src/corrections.rs`, which
ships) and in Go (`tg-score/internal/detect`, which does not).

Scoring 1,574 rows through the WASM module costs about eight minutes, because each row
is three MiniLM transformer inferences. Tuning a correction layer by rebuilding the
module for every parameter change is unworkable.

But the correction is a pure function of `(question, ground_truth, answer)` and the
baseline score — so it can be applied to *cached* baseline scores instead, turning an
eight-minute round trip into milliseconds. That is what `tg-score simulate` does, and
it is why the layer could be tuned against live gate outcomes at all.

That shortcut is only honest if the Go implementation computes the same function the
Rust actually ships. So `tg-score verify` proves it, on every row:

```
[C] CROSS-CHECK  —  Rust correction layer vs Go reference
    PASS  19526/19526 rows agree exactly
```

The module exports `correction_answer` specifically to make this checkable — and to
make the layer's decisions inspectable by anyone, since a scoring module otherwise
returns one `f32` with no account of how it got there.

## 7. What would change this design

- **If the gate turns out to group by intent-epoch rather than pooling epochs**, the
  unmodified baseline fails 28 of 217 groups and no fork-based approach works. This is
  the largest unresolved risk. See [FINDINGS.md §3](FINDINGS.md).
- **If the network's replay window reaches back further than a few epochs**, the
  unmodified baseline fails on `WEATHER_CHECK` and `WEATHER_FORECAST` — both of which
  are in its evaluation set. Agreement on those intents has been improving sharply
  over time, so this is a question about how much history the gate replays, and it is
  not answerable from outside. See [FINDINGS.md §3c](FINDINGS.md).
- **If `CHAT_COMPLETION`'s agreement drifts below 0.60** on the network's own corpus,
  the submission fails regardless of the correction layer, because the baseline itself
  is what sits at 0.6065.
- **If the evaluation set includes intents absent from `/scores`**, none of this is
  measurable in advance and the honest answer is to submit, read the rejection string,
  and iterate — registration is currently free.
