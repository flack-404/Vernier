# Findings

Every quantitative claim in this repository, with the command that reproduces it.

Snapshot: **epoch 285**, 27 August 2026. Leaderboard data is point-in-time; re-run
`tg-score pull` before relying on any number here.

Three of these findings **overturned parts of the plan this repository was built
from**. Those are marked ⚠ and stated first, because they change what a builder
should do.

---

## ⚠ 1. The headroom is 0.0063, not 0.29

**The prior research record claimed:**

> "Forking the baseline clears the hardest check with ~0.29 of headroom — and that
> headroom is exactly what a bounded correction spends."

That was measured on 6 intents across 3 epochs — 12 intent-epoch groups, all passing
comfortably. Measured across **every rankable intent** on current epochs, the weakest
is far tighter:

```
$ tg-score subset -c data/corpus-full.json -o data/corpus-recent.json -per-intent 0 -since 271
$ tg-score gate -c data/corpus-recent.json -pool-epochs build/baseline.wasm

    threshold 0.60 · mean rho +0.8406 · 20 groups measured
    weakest CHAT_COMPLETION at +0.6065 — headroom +0.0065

    CHAT_COMPLETION      150   +0.6065  PASS
    STOCK_PRICE           36   +0.6345  PASS
    LANGUAGE_GENERATION  160   +0.6924  PASS
    ...
    TOKEN_HOLDER_COUNT    24   +0.9350  PASS
```

**The pure baseline fork clears gate 3 by six thousandths on its weakest intent.**

This inverts the design premise. The plan was "spend the 0.29 of headroom on a 0.12
correction". There is no headroom to spend. A correction that touched
`CHAT_COMPLETION` at all would very likely sink the submission.

**Why the design survives anyway** — and it is luck turned into a property, not
foresight: `CHAT_COMPLETION` is one of the intents where the correction is
*structurally incapable* of firing. The measured effect of the layer on that intent
is exactly +0.0000. See finding 4.

**Consequence for anyone building here:** do not budget headroom you have not
measured on the intents you will actually be judged on, and remember you do not
choose those intents.

---

## ⚠ 2. The corpus is 39× larger than the published sample

`GET /scores` caps a page at 500 rows but reports the true total, and accepts
`offset`. The prior record used a single page.

```bash
$ curl -s 'http://13.237.89.59:7044/scores?limit=1' | jq '{total}'
{ "total": 19526 }

$ tg-score pull -o data/corpus-full.json
  rows              19526
  intents           46
  epochs            [1 ... 285]
  empty answers     7614 (39.0%)
  published score 0 7740 (39.6%)
```

**19,526 rows, 46 intents, 285 epochs** rather than 500 rows, 6 intents, 3 epochs.
This is what makes the rest of this document possible: gate 3 can be measured on
every intent the network has ever scored, instead of the six that happened to land
in the first page.

**Only 21 of the 46 intents are rankable.** The other 25 have no group where the
published scores differ — usually every row is 0 — so Spearman is undefined and they
can neither pass nor fail a candidate.

```bash
$ tg-score corpus -c data/corpus-full.json
```

| | rows |
|---|---:|
| total | 19,526 |
| in groups smaller than 4 | 4,960 |
| in groups with a constant reference | 308 |
| **in rankable groups** | **14,258** across 1,914 groups, 21 intents |

The 8 largest rankable intents are, in order: `TASK_COMPLETION`, `CHAT_COMPLETION`,
`LANGUAGE_GENERATION`, `AGENT_TASK`, `WEB_SEARCH`, `WEATHER_FORECAST`,
`WEATHER_CHECK`, `NEWS_SEARCH`. Six of those eight appear in **both** live rejection
maps, which is the evidence that the network's evaluation set is essentially the
high-volume rankable intents.

---

## ⚠ 3. Whether epochs are pooled decides whether the baseline itself passes

The network reports one agreement figure per intent, which implies it pools epochs.
It matters enormously. Both runs below use the same corpus
(`data/corpus-iter.json`, 1,574 rows, 12 groups per intent) and differ only in
grouping:

```bash
$ tg-score gate -c data/corpus-iter.json build/baseline.wasm              # per intent-epoch
    mean rho +0.8386 · 217 groups measured
    28 groups below 0.60          <- FAIL

$ tg-score gate -c data/corpus-iter.json -pool-epochs build/baseline.wasm # per intent
    mean rho +0.8480 · 21 groups measured
    0 groups below 0.60           <- PASS
```

**Split by epoch, an unmodified fork of Telegraph's own published baseline fails 28
of 217 groups.** Pooled, it passes everywhere.

Since the rejection map is keyed by intent alone, pooling is the likelier reading.
But this is an inference, not a fact, and it is the largest single risk to any
fork-based submission. `tg-score gate` defaults to the strict per-epoch reading and
takes `-pool-epochs` for the other.

---

## ⚠ 3b. The evaluation set is ~7 intents, and one of them cannot be tested locally

Every rejection map contains **exactly seven** intents. Six are common to all of them:

```
  #5, #6  AGENT_TASK  IMAGE_VERIFICATION  LANGUAGE_GENERATION  TASK_COMPLETION
          WEATHER_CHECK  WEATHER_FORECAST  WEB_SEARCH
  #13     AGENT_TASK  NEWS_SEARCH         LANGUAGE_GENERATION  TASK_COMPLETION
          WEATHER_CHECK  WEATHER_FORECAST  WEB_SEARCH

  intersection: AGENT_TASK, LANGUAGE_GENERATION, TASK_COMPLETION,
                WEATHER_CHECK, WEATHER_FORECAST, WEB_SEARCH
```

Six fixed, one varying. Note that #5 and #6 are the *same binary* submitted from two
addresses, so this is really two independent samples, not three — treat the pattern
as suggestive rather than settled.

Two consequences, pulling in opposite directions:

**`CHAT_COMPLETION` appears in no rejection map.** It is the second-largest rankable
intent in the corpus (2,756 rows across 285 groups) and it is *also* the intent where
the baseline's agreement is thinnest at 0.6065 (finding 1). If it is genuinely outside
the evaluation set, the tightest margin in this whole analysis is never tested.

**`IMAGE_VERIFICATION` is in the evaluation set and cannot be tested locally at all.**
The network reported `IMAGE_VERIFICATION:0.3057863` for registration #5. In the public
corpus that intent has 570 rows and **zero** rankable groups — every group's published
scores are identical, so Spearman is undefined and no local harness can produce a
figure for it. The network is evidently ranking against reference data that `/scores`
does not expose.

That is an irreducible gap. `tg-score` can tell you a great deal before you submit; it
cannot tell you everything, and this document would be dishonest if it implied
otherwise.

---

## ⚠ 3c. Rank agreement is a moving target, and stale epochs fail the gate

This one nearly caused a wrong conclusion. Pooling **40 groups per intent** (3,555
rows, epochs 219–285) instead of the most recent 12, the unmodified baseline **fails**:

```
$ tg-score gate -c data/corpus-validate.json -pool-epochs build/baseline.wasm

[3] RANK AGREEMENT                                               FAIL
    mean rho +0.8192 · weakest WEATHER_CHECK at +0.5180 — headroom -0.0820

    WEATHER_CHECK       254   +0.5180  FAIL
    WEATHER_FORECAST    304   +0.5695  FAIL
```

Both of those intents are in the network's evaluation set. Taken at face value this
says a perfect fork of Telegraph's own published baseline would be rejected.

It is not that. Splitting the same rows by epoch window shows a strong trend:

| intent | epochs ≤250 | 251–270 | 271–285 |
|---|---:|---:|---:|
| WEATHER_CHECK | +0.3584 | +0.5283 | **+0.8825** |
| WEATHER_FORECAST | +0.4847 | +0.7283 | **+0.8497** |
| CHAT_COMPLETION | +0.5741 | +0.7919 | +0.6065 |
| TASK_COMPLETION | +0.9329 | +0.8639 | +0.9281 |

**Agreement between the published baseline and the deployed scorer has been improving
sharply over time**, at least on the weather intents. The old rounds are what drag the
pooled figure under the gate; on current data both pass comfortably.

Since the network replays current rounds, the recent-epoch figure is the operative
one — but this is an inference, and it is the reason `tg-score subset` grew a
`-since` flag. Measure on the epochs you will actually be judged against:

```bash
tg-score subset -c data/corpus-full.json -o data/corpus-recent.json -per-intent 0 -since 271
```

**The general lesson is worth more than the specific numbers.** A rank-agreement
figure is only meaningful against a stated epoch window. Any claim of the form "the
baseline passes the gate" that does not say *when* is unfalsifiable, and this document
made that mistake before measuring it.

---

## 4. 70% of rankable rows contain no digit in their ground truth

This is the property the whole design rests on, and it is why the layer costs no
headroom.

```bash
$ tg-score simulate -c data/corpus-iter.json -pool-epochs build/baseline.wasm
```

| intent | rankable rows | GT contains a digit | GT contains hex/CVE |
|---|---:|---:|---:|
| TASK_COMPLETION | 2,778 | 0 (0.0%) | 0 |
| CHAT_COMPLETION | 2,756 | 0 (0.0%) | 0 |
| LANGUAGE_GENERATION | 2,668 | 0 (0.0%) | 0 |
| AGENT_TASK | 1,806 | 0 (0.0%) | 0 |
| WEB_SEARCH | 1,290 | 101 (7.8%) | 0 |
| WEATHER_CHECK | 723 | 723 (100%) | 0 |
| ONCHAIN_TX_LOOKUP | 180 | 180 (100%) | 180 (100%) |
| **all 21 intents** | **14,258** | **2,747 (19.3%)** | **239 (1.7%)** |

Every detector is gated on the ground truth containing something checkable, so on the
four largest intents they cannot fire. The silence profile confirms it end to end —
the layer fires on **0 of 120** `CHAT_COMPLETION` rows:

```
SILENCE PROFILE (where the layer can and cannot fire)
  AGENT_TASK              84     0    0.0%   silent
  CHAT_COMPLETION        120     0    0.0%   silent
  LANGUAGE_GENERATION    130     0    0.0%   silent
  RESEARCH_QUERY          54     0    0.0%   silent
  TASK_COMPLETION        132     0    0.0%   silent
  ...
  SSL_VERIFICATION         8     6   75.0%
```

**Note this replaces the "adaptive cap per intent" mitigation in the original design,
which cannot be built:** `rank_answer` receives no intent parameter, so the module
never learns which intent it is scoring. Gating on the shape of the ground truth
achieves the same end without needing to know.

---

## 5. The correction improves agreement rather than spending it

Measured on `data/corpus-recent.json` (1,468 rows, 20 intents, epochs 271–285):

```
                                  BASELINE  VERNIER   CHANGE
  mean rank agreement               0.8406     0.8450   +0.0044
  weakest intent (CHAT_COMPLETION)  0.6065     0.6065   +0.0000
  intents failing the 0.60 gate          0          0
  Catch-Rate catches                             5 on 2 intents
```

Per intent, the largest movements:

| intent | baseline | vernier | change |
|---|---:|---:|---:|
| STORM_ALERT | 0.8108 | 0.8378 | **+0.0270** |
| ONCHAIN_TX_LOOKUP | 0.7329 | 0.7584 | **+0.0255** |
| WALLET_BALANCE_CHECK | 0.8719 | 0.8869 | **+0.0150** |
| CRYPTO_PRICE | 0.8869 | 0.8974 | +0.0105 |
| WEATHER_CHECK | 0.8825 | 0.8909 | +0.0084 |
| NEWS_SEARCH | 0.9294 | 0.9250 | −0.0044 |
| TVL_LOOKUP | 0.8846 | 0.8794 | −0.0052 |
| CHAT_COMPLETION | 0.6065 | 0.6065 | **+0.0000** |

Where the layer fires, it agrees with the network's own ranking *better* than the raw
baseline does. That is the strongest available evidence that the detectors are
catching real errors rather than adding noise.

---

## 6b. ISO dates are not identifiers

The identifier detector originally treated ISO dates like transaction hashes. On a
live `WEATHER_FORECAST` round that cost a correct answer 0.03 for nothing:

```
GROUND TRUTH  Here is the 7-day weather forecast for Tokyo starting from
              2026-09-01T00:00:00Z UTC, with a cutoff before 2026-09-07T23:59:59Z...
MINER ANSWER  {"as_of":"2026-08-26", ...}

  GT ids        date:2026-09-01  date:2026-09-07
  identifier  -0.0300   (50% of identifiers reproduced)
```

The ground truth's dates are **the range the caller asked about**, echoed from the
question. The answer's date is its own `as_of` stamp. Neither is a claim the other
must reproduce, and a forecast legitimately carries a different one. A transaction
hash or a CVE identifier has no such excuse — those are invented or correct.

Excluding dates removed a −0.0723 regression on `SSL_VERIFICATION` and lifted mean
agreement from 0.8406 to 0.8450 with the weakest intent unchanged.

---

## 6c. A ground-truth density ceiling sounded right and measured wrong

Since the numeric detector's model is "the ground truth asserts a headline figure",
a ground truth carrying 54 numbers — a 7-day forecast table — asserts no headline
figure, and picking two is guessing. Silencing the detector above a density ceiling
is the obvious fix.

Measured, it makes things worse on current data:

```
$ for m in 0 8 12 20 40; do tg-score simulate -c data/corpus-recent.json     -pool-epochs -brief -max-gt-nums $m build/baseline.wasm; done

max-gt=0    mean=0.8450  worst=CHAT_COMPLETION 0.6065  fail=0  fired=20.8%  catch=5
max-gt=8    mean=0.8151  worst=CHAT_COMPLETION 0.6065  fail=0  fired=13.8%  catch=4
max-gt=12   mean=0.8367  worst=CHAT_COMPLETION 0.6065  fail=0  fired=16.1%  catch=5
max-gt=20   mean=0.8202  worst=CHAT_COMPLETION 0.6065  fail=0  fired=17.9%  catch=5
max-gt=40   mean=0.8209  worst=CHAT_COMPLETION 0.6065  fail=0  fired=18.4%  catch=5
```

No ceiling wins. The detector was helping on dense numeric answers, and the ceiling
silenced it there. **Shipped with the ceiling disabled**, and kept as a configurable
knob so the trade-off can be re-measured rather than re-argued — it does help
marginally once stale epochs are pooled in, which may matter if the network's replay
window turns out to be longer than assumed.

This one is worth stating plainly: the reasoning was sound and the measurement said
no. That is the entire argument for building the harness before the scorer.

---

## 6. Coverage-based numeric checking is wrong in two opposite directions

The first implementation penalised an answer in proportion to how many of the ground
truth's figures it failed to reproduce. Measured against live rows, it broke twice:

**It punished a correct answer for not echoing a timestamp.** `STOCK_PRICE` epoch
285, miner `txlens`:

```
GROUND TRUTH  The current share price of Microsoft (MSFT) is $496.35 as of
              August 26, 2026, at 12:59:59 PM Pacific Time.
MINER ANSWER  {"price_usd":496.37,...,"summary":"MSFT is $496.37"}

  GT numbers    $496.35  26  2026  12  59  59
  numeric     -0.0640   (20% of ground-truth figures reproduced)
```

The answer is correct to four decimal places. It was penalised 0.064 for omitting the
ground truth's **clock**, and that single row inverted `STOCK_PRICE`'s ranking
(−0.0308 agreement).

**It let a number flood escape entirely.** Same intent and epoch, miner
`kriterion-pramagraph` returned **6,972 numeric tokens** and scored 100% coverage
with zero penalty — with that many numbers, everything matches by accident.

Both have one root cause: coverage of ground-truth numbers is the wrong metric. The
fix is to penalise **contradiction, never omission** — the answer asserts a
*different* value — plus a flood guard, because matching uses a relative tolerance
and an answer spraying K numbers covers roughly `K × 2 × tolerance` of the value
space by chance. 900 consecutive integers match any headline below 900 outright.

Both cases are locked in as regression tests in
`tg-score/internal/detect/detect_test.go`.

Result of the redesign, both measured on `data/corpus-iter.json`:

| | coverage-based | contradiction-based |
|---|---:|---:|
| firing rate | 26.75% | 21.60% |
| mean agreement | 0.8472 | **0.8509** |
| weakest intent | 0.6037 | **0.6063** |
| STOCK_PRICE | −0.0308 | **+0.0000** |
| Catch-Rate catches | 2 | **6** |

---

## 7. Ground truth is sometimes itself a refusal, and those rows are noise

1.3% of live rows (247 of 19,526) have a ground truth that begins by refusing —
concentrated in `WEATHER_FORECAST` (136) but present across 12 intents.

Their published scores span the entire range, which is why the layer returns the
baseline untouched on them rather than trying to judge:

```
[FINANCIAL_DATA] score=1.0000
  GT: "I don't have the exact All-in Sustaining Costs (AISC) for Freeport-McMoRan..."
  MA: "I don't have verified data on Freeport-McMoRan's All-in Sustaining Costs..."

[WEATHER_FORECAST] score=0.9965
  GT: "Sorry, I can't provide the exact 48-hour hourly weather forecast for Tokyo..."
  MA: {"current":{"chance_of_rain":33,...}}   <- a real forecast
```

**A refusal mimicking a refusal scored a perfect 1.0000.** A substantive answer
against the same kind of reference scored 0.9965. The reference ordering on these
rows carries no information, so any correction applied there is uncorrelated noise
that can only erode rank agreement — and rewarding the mimicry is the gaming vector
the anti-gaming half of detector 3 exists to close.

---

## 8. The Go reference and the Rust module must use identical float precision

`tg-score verify` compares the shipping Rust correction layer against its Go
reference on every row. It found a real disagreement on a live `TOKEN_HOLDER_COUNT`
row where every component matched and only the total differed:

```
  FIELD                      RUST           GO
  total                 -0.090000    -0.090000   <-- differs
  numeric               -0.000000    -0.000000
  identifier            -0.030000    -0.030000
  refusal               -0.060000    -0.060000
```

The Rust summed in `f32`; the Go summed in `f64` and narrowed once. `float32(-0.03) +
float32(-0.06)` is not `float32(-0.03 + -0.06)`. The Go reference now computes
penalties in `float32` to mirror the module exactly.

Number *values* stay `f64` on both sides — answers carry figures like
`3977664413963610716` wei, which `f32` cannot hold to seven significant digits.

```bash
$ tg-score verify -c data/corpus-full.json build/vernier.wasm
[C] CROSS-CHECK  —  Rust correction layer vs Go reference
    PASS  19526/19526 rows agree exactly
```

---

## 9. All four rejected binaries trap on real rows

Pulled from their IPFS URLs on the public registry. Each is 2–10 KB, so none embeds a
transformer — these are minimal scorers.

Every one of them fails on at least one live row:

```
EvalErrorCount 1 — rows this module could not score
  e.g. 6b271c1d-...: write 1940656 bytes at 1048624: out of module memory bounds
```

They use a fixed bump allocator that never grows linear memory, and the corpus
contains a 1.9 MB row. `alloc` returns a pointer past the end of memory and the host
write fails.

The registry exposes an `EvalErrorCount` field, so the network counts these and
carries on rather than treating them as fatal. `tg-score` does the same.

---

## ⚠ 9b. `registerWasm` takes an ARRAY of intents, not one

The published signature is wrong. Every source we had — the protocol docs and the
parent research record — documents:

```solidity
function registerWasm(bytes32 wasmHash, string wasmUrl, string intent)
    external returns (uint256 registrationId);   // selector 0xfe1e40f7
```

Calling that reverts:

```
execution reverted: Diamond: Function does not exist
```

The deployed Diamond exposes 160 selectors across 21 facets, and `0xfe1e40f7` maps
to the zero address. Brute-forcing candidate signatures against the live loupe finds
the real one:

```solidity
function registerWasm(bytes32 wasmHash, string wasmUrl, string[] intents);
// selector 0x19238d1c, on a dedicated single-function facet
// 0xb71185428dD6932B9EaAd6eAEf696A822992B1BF
```

Reproduce:

```bash
D=0xac683bFa8F1C892E23e8300d14c20678C6FC0CA3
cast call $D 'facetAddress(bytes4)(address)' 0xfe1e40f7 --rpc-url https://sepolia.base.org
#   0x0000000000000000000000000000000000000000     <- not registered
cast call $D 'facetAddress(bytes4)(address)' 0x19238d1c --rpc-url https://sepolia.base.org
#   0xb71185428dD6932B9EaAd6eAEf696A822992B1BF     <- the WASM registry facet
```

`deregisterEntity(uint256,uint8)` (0x59134d1c) and
`registerMiner(string,bytes32,address,uint256,string[])` (0x876b2422) are both present
exactly as documented, so this is specific to `registerWasm`.

**Two consequences.** A submission built against the documented signature burns a
transaction on a revert and gets no rejection string to learn from — the call never
reaches the registry. And more usefully: **one registration can cover several
intents**, which the single-string signature hid. Since a script is evaluated on
intents it does not choose anyway, the array costs nothing extra to widen.

---

## 10. The registry's `WasmHash` is not the SHA-256 of the served bytes

Checked for all four rejected binaries. It matches neither the raw file digest nor
the CIDv0 multihash:

| Registration | registry `WasmHash` | sha256 of served bytes | CIDv0 digest |
|---|---|---|---|
| #5 | `34220f72…` | `fd1ed57d…` | `496ddd2b…` |
| #8 | `13d668d1…` | `c377ce4d…` | `7d408299…` |
| #11 | `941e0421…` | `57d12050…` | `8cdbb4f5…` |
| #13 | `bfcd262d…` | `e27a34ee…` | `eab7cb85…` |

The network evidently fetched and evaluated these modules — it rejected them on
scoring grounds, not integrity grounds — so the field does not appear to gate
evaluation.

**RESOLVED, 27 Aug 2026.** Registering Vernier settled it: the contract stores
exactly the `bytes32` you pass, and passing the SHA-256 of the raw bytes round-trips
intact. From the `WasmRegistered` event of
`0x788a730d61637b0a1bde5b271591b29271644ddeac07b1dbd5abba0deeb79d98`:

```
  wasmHash   0xce745cb06df214480cb8bfabf3f6987faac944519de8b90623c48435b63fa6b0
  $ sha256sum build/vernier.wasm
             ce745cb06df214480cb8bfabf3f6987faac944519de8b90623c48435b63fa6b0
```

So the documented meaning is correct and the field is simply not verified against
the fetched content. The mismatch on the four rejected registrations is therefore
*theirs* — each registered a hash that does not describe the bytes at their own
`WasmURL`. Harmless for activation, since nothing checks it, but it means none of
those four could prove which binary they actually submitted.

Pass the real digest. `scripts/submit.sh` computes it from the file it uploads and
verifies the gateway serves the same bytes back before it will register.

---

## 11. Renaming the crate changes the binary hash but not its behaviour

The project was renamed from Plumbline to Vernier on 27 Aug 2026. Renaming the crate
permutes the WASM type section — same length, same types, different order — so the
digest changes while the module does not:

```
  as plumbline   ce745cb06df214480cb8bfabf3f6987faac944519de8b90623c48435b63fa6b0
  as vernier     b9904e2962bc2f62aa0adc6062da68b6e0e6eca0fa85c12f040a4559254c28a6
  both           24,192,934 bytes
```

Verified rather than assumed, across every row of the evaluation corpus:

```
$ tg-score compare -c data/corpus-recent.json build/plumbline.wasm build/vernier.wasm

  rows compared      1468
  bit-identical      1468 (100.00%)
  changed            0 (0.00%)
  largest change     0.0000

  The two modules are behaviourally identical on this corpus.
```

The gate results are also unchanged: mean rank agreement +0.8450, weakest intent
`CHAT_COMPLETION` at +0.6065, all three gates pass under both digests.

This is the same effect noted in the toolchain section below — our fork is not
byte-identical to upstream's build either, for exactly this reason — and it is why
every identity claim in this repository is behavioural, checked on real inputs,
rather than a digest comparison. It also cost a second on-chain registration; see
[SUBMISSION.md](SUBMISSION.md).

---

## Toolchain

```
cargo 1.95.0 · rustc 1.95.0 · wasm32-unknown-unknown
go 1.23.4 · wazero v1.8.2
telegraph-wasm-baseline @ dfa0cf7fda72789267811ba2190f61a8eaacedf6
```

Fork integrity is verified rather than assumed:

```bash
$ tg-score verify -c data/corpus-smoke.json \
    -baseline build/baseline.wasm \
    -upstream /tmp/tgw/target/wasm32-unknown-unknown/release/telegraph_scoring.wasm \
    build/vernier.wasm

[A] FORK INTEGRITY  PASS  306/306 rows scored bit-identically
```

Note that our fork's binary is **not** byte-identical to upstream's — renaming the
crate permutes the WASM type section — so the claim is behavioural identity on real
inputs, which is what actually matters and what is checked.
