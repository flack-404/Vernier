# Telegraph's WASM activation gate, documented

Telegraph lets anyone register a scoring script. It does not document what a script
has to satisfy to be activated. At the time of writing **five scripts have been
submitted and five were rejected**, and every author found out why by spending an
on-chain registration and reading a cryptic string back off the registry.

This document is what we recovered. Everything below is either quoted verbatim from
the public registry or measured against live data, and each claim says which.

> **Status of this document.** The gates and their thresholds are quoted from real
> rejection strings — those are facts. How each gate is *computed* is inferred, and
> where we are guessing we say so. Corrections welcome; the reproduction commands
> are included so you can check us.

---

## Where this comes from

```bash
curl -s http://13.237.89.59:7044/engine/v1/intents/CHAT_COMPLETION/wasm | jq .
```

The endpoint returns every registration network-wide, whichever intent you ask for.
Each record carries an `ActivationStatus` and, when rejected, a `RejectionReason`.
`tg-score registry` formats it:

```
$ tg-score registry
  #5   CHAT_COMPLETION  [REJECTED]
       REASON rank agreement below threshold (0.60), got: map[AGENT_TASK:0.11178552
              IMAGE_VERIFICATION:0.3057863 LANGUAGE_GENERATION:-0.4616971 ...]
  #8   SPORTS_SCORE     [REJECTED]
       REASON structural validation failed: self-match (0.0000) did not beat
              unrelated cross-match (0.0000)
  #11  WEATHER_CHECK    [REJECTED]
       REASON candidate scores collapsed: stdev=0.0000 <= threshold 0.0500
  #13  SPORTS_SCORE     [REJECTED]
       REASON rank agreement below threshold (0.60), got: map[AGENT_TASK:0.044694155 ...]
  active scripts on the network: 0
```

---

## Gate 1 — structural self-match

**Verbatim:**

```
structural validation failed: self-match (0.0000) did not beat unrelated cross-match (0.0000)
```

**What it checks.** Score a triple where the answer is the correct one, and a triple
where the answer belongs to something else entirely. The matching one must score
higher. It is the weakest possible sanity check: *does this module distinguish a
right answer from an irrelevant one at all?*

**Both figures were 0.0000**, and that is the tell. Not two close numbers — two
zeros. A module that returns zero for everything, including a perfect answer, is
almost always failing at the **memory ABI** rather than at scoring. The host wrote
strings into WASM memory and the module read something else, or nothing.

**Budget a day here.** This is the cheapest gate to pass and the one that cost a
registration to discover.

**What we infer.** The single aggregate figure suggests the network averages over
several probe pairs. We do not know which pairs it uses; `tg-score` builds them from
the corpus, one representative per intent, each checked against the next intent's
ground truth in sorted order.

**Where we are deliberately stricter.** We also require 60% of individual pairs to
order correctly. Registration #8 orders only 4 of 21 pairs correctly yet still has a
positive *mean*, because its cross-match scores are all exactly zero. A tool that
called that PASS would be lying to you.

---

## Gate 2 — score dispersion

**Verbatim:**

```
candidate scores collapsed: stdev=0.0000 <= threshold 0.0500
```

**What it checks.** The standard deviation of your scores across the evaluation set
must exceed **0.05**. Note the operator: `<=` fails, so you must *exceed* the
threshold, not merely reach it.

**This is Telegraph's "resistance to gaming" criterion, implemented.** A script that
returns a constant — 1.0 for everybody, say — would make every miner equal and
destroy the routing signal. Gate 2 refuses it mechanically.

It also catches something subtler: a module that *technically* works but compresses
everything into a narrow band. If your scorer only ever emits 0.70–0.72, it is not
scoring, it is agreeing.

---

## Gate 3 — rank agreement

**Verbatim:**

```
rank agreement below threshold (0.60), got: map[AGENT_TASK:0.11178552
  IMAGE_VERIFICATION:0.3057863 LANGUAGE_GENERATION:-0.4616971 TASK_COMPLETION:0.047094055
  WEATHER_CHECK:-0.24522902 WEATHER_FORECAST:-0.41942906 WEB_SEARCH:-0.6774859]
```

**This is the one that kills submissions.** Three of the five rejections are this.

**What it checks.** Rank the miners with your script; rank them with the incumbent.
The two orderings must correlate at **0.60 or better**. The map is keyed **per
intent**, and a single intent below the line sinks the submission.

### Two things about that map that change how you build

**1. You are evaluated on intents you did not choose.**

Registration #5 registered for `CHAT_COMPLETION`. It was evaluated on `AGENT_TASK`,
`IMAGE_VERIFICATION`, `LANGUAGE_GENERATION`, `TASK_COMPLETION`, `WEATHER_CHECK`,
`WEATHER_FORECAST` and `WEB_SEARCH`. Registering for one narrow intent does not
narrow your exposure.

Comparing those intent names against corpus volume, the evaluation set looks like
**the intents with the most replay data**. Of 46 intents in the live corpus, only 21
have any group where the published scores differ at all — the rest are constant,
usually all zero, and carry no ranking to agree with. The intents that appear in
both rejection maps are among the largest of those 21.

**1b. The evaluation set looks like seven intents, and one is invisible from here.**

Every rejection map holds exactly seven intents, six of them common to all:
`AGENT_TASK`, `LANGUAGE_GENERATION`, `TASK_COMPLETION`, `WEATHER_CHECK`,
`WEATHER_FORECAST`, `WEB_SEARCH`. The seventh varies (`IMAGE_VERIFICATION` in one,
`NEWS_SEARCH` in the other).

`IMAGE_VERIFICATION` is the awkward one. The network scored registration #5 at
`0.3057863` on it — but in the public corpus that intent has 570 rows and **zero**
rankable groups, because every group's published scores are identical. No local
harness can produce a figure for it. The network ranks against reference data
`/scores` does not expose.

Plan for that gap rather than around it: register, read the rejection string, iterate.
Registration is currently free.

**2. Correlations go negative, and that is the trap.**

`WEB_SEARCH:-0.677` is not a weak score, it is an *inverted* one. Both rejected
scripts show the same pattern: mildly positive on some intents, strongly negative on
prose ones. That is the signature of a scorer that is stricter than the incumbent —
it ranks the careful, terse answer above the long fluent one, and the incumbent does
the opposite.

> **The gate does not reward being right. It rewards agreeing almost everywhere and
> disagreeing surgically.** Every "better, stricter scorer" fails here. This is the
> single most important fact about building for Track 2, and it is not written down
> anywhere else.

### How we compute it

Spearman rank correlation between the candidate's scores and the published scores,
per intent group. Two implementation details matter:

- **Ties.** 40% of live rounds score exactly 0, so a ten-miner group routinely holds
  six identical values. The textbook shortcut formula (`1 - 6Σd²/(n³-n)`) is only
  valid for tie-free data and reports the wrong figure on most real groups. We use
  the Pearson correlation of fractional ranks, which stays correct with ties.
- **Undefined groups.** If either side is constant the coefficient is undefined.
  Scoring that as 0.0 would fail a candidate for the corpus having nothing to test.
  We report it as `n/a` and say *which* side was constant — a constant *candidate*
  means your module is not discriminating, which is your problem; a constant
  *reference* is not.

**Open question: does the network pool epochs?** It reports one figure per intent,
which suggests pooling. It matters enormously. Measured on the same corpus with the
unmodified baseline:

| grouping | groups | mean rho | groups below 0.60 |
|---|---:|---:|---:|
| per intent (epochs pooled) | 21 | +0.848 | **0** |
| per intent-epoch | 217 | +0.839 | **28** |

Pooled, the baseline passes everywhere. Split by epoch, 28 groups fail. Since the
rejection map is keyed by intent alone, pooling is the likelier reading — but if it
is wrong, **a perfect fork of Telegraph's own baseline would be rejected**. Check
both: `tg-score gate` defaults to per-epoch and takes `-pool-epochs`.

---

## What the registry exposes beyond the reason

| Field | Meaning |
|---|---|
| `ActivationStatus` | `pending` · `active` · `rejected` · `superseded` · `deregistered` |
| `RejectionReason` | The verbatim string. Your only feedback channel |
| `EvalErrorCount` | Rows the module failed to score. Not fatal — the network counts and continues |
| `BondAmount` | Observed **0** on testnet, so iteration is currently free |
| `WasmHash` | See the caveat below |

**`EvalErrorCount` is worth designing for.** All four rejected binaries we replayed
trap on at least one live row: they use a fixed bump allocator that never grows
linear memory, and the corpus contains a 1.9 MB row. `tg-score` records these
per-row failures and carries on, the way the network appears to.

**Caveat on `WasmHash`.** The registry's `WasmHash` does **not** equal the SHA-256 of
the bytes served at `WasmURL`, and does not equal the CIDv0 digest either — we
checked all four binaries and neither matches:

| Registration | registry `WasmHash` | sha256 of served bytes |
|---|---|---|
| #5 | `34220f72…6807ac` | `fd1ed57d…06b719` |
| #8 | `13d668d1…93d360` | `c377ce4d…91930b` |
| #11 | `941e0421…2b7599` | `57d12050…000e90` |
| #13 | `bfcd262d…3171f63` | `e27a34ee…0f86e1` |

Since the network clearly fetched and evaluated these modules, the field does not
appear to gate evaluation. We do not know what it hashes. If you are registering,
do not assume your locally computed SHA-256 is what the protocol will store.

---

## Reproducing all of this

```bash
make cli
tg-score pull -o data/corpus-full.json      # ~19,500 rows, 46 intents
tg-score subset -c data/corpus-full.json -o data/corpus-iter.json -per-intent 12
tg-score registry                           # live activation status
./scripts/demo-rejected.sh                  # replay the four rejected binaries
```

Running the gate against the rejected binaries reproduces the network's own verdict
on all four, including one string character-for-character:

| Registration | Network's gate | `tg-score` |
|---|---|---|
| #11 | dispersion | **[2] FAIL** — `candidate scores collapsed: stdev=0.0000 <= threshold 0.0500` |
| #8 | structural | **[1] FAIL** |
| #5 | rank agreement | **[3] FAIL** |
| #13 | rank agreement | **[3] FAIL** |

None of them needed a transaction.
