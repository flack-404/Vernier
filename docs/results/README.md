# Captured results

Raw output from the commands in [`../FINDINGS.md`](../FINDINGS.md), kept so every
number quoted in this repository can be traced to the run that produced it.

Snapshot: **epoch 285**, 27 August 2026. Regenerate with `make check` after a
`tg-score pull` — the figures will move as the network does.

| file | what it is |
|---|---|
| `gate-plumbline-recent.txt` | The shipping module against the three gates, current epochs. **All three pass.** |
| `gate-baseline-recent.txt` | The same corpus, correction layer compiled out. The comparison the headline claims rest on |
| `gate-baseline-40epochs.txt` | The unmodified baseline pooled over 40 epochs per intent — **fails** on `WEATHER_CHECK` and `WEATHER_FORECAST`. See FINDINGS §3c |
| `verify.txt` | Fork integrity, correction bounds, and Rust-vs-Go cross-check |
| `gate-vernier-recent.txt` | Regenerated under the current name; see docs/SUBMISSION.md |
| `gate-rejected-*.txt` | The four already-rejected registrations, replayed locally |

## Headline

```
plumbline.wasm   sha256 ce745cb06df214480cb8bfabf3f6987faac944519de8b90623c48435b63fa6b0
baseline.wasm    sha256 ebdcfd12cf54b8fe0a3f085834e0a0215d22d7addaae720761ab603b986d2885

corpus 1,468 rows · 20 intents · epochs 271-285

[1] STRUCTURAL SELF-MATCH   PASS   self 0.8605 vs cross 0.1740, 20/20 pairs correct
[2] SCORE DISPERSION        PASS   stdev 0.3056 against a 0.05 floor
[3] RANK AGREEMENT          PASS   mean +0.8450, weakest CHAT_COMPLETION +0.6065

                              BASELINE  PLUMBLINE
  mean rank agreement           0.8406     0.8450
  weakest intent                0.6065     0.6065
  intents failing the gate           0          0
```

The correction layer raises mean agreement and leaves the weakest intent untouched.

## The four rejected registrations, replayed

Each was rejected by the network on a gate; `tg-score` reaches the same gate for all
four, without a transaction.

| registration | network's `RejectionReason` | `tg-score` |
|---|---|---|
| #11 `WEATHER_CHECK` | `candidate scores collapsed: stdev=0.0000 <= threshold 0.0500` | **[2] FAIL** — same string, character for character |
| #8 `SPORTS_SCORE` | `structural validation failed: self-match (0.0000) did not beat unrelated cross-match (0.0000)` | **[1] FAIL** |
| #5 `CHAT_COMPLETION` | `rank agreement below threshold (0.60)` | **[3] FAIL** |
| #13 `SPORTS_SCORE` | `rank agreement below threshold (0.60)` | **[3] FAIL** |

All four also register a non-zero `EvalErrorCount`: they use a fixed bump allocator
that never grows linear memory, and the corpus contains a 1.9 MB row.

## Reproducing

```bash
make cli
tg-score pull -o data/corpus-full.json
tg-score subset -c data/corpus-full.json -o data/corpus-recent.json -per-intent 0 -since 271
tg-score subset -c data/corpus-full.json -o data/corpus-iter.json -per-intent 12
./scripts/fetch-rejected.sh
make rebuild
make check
./scripts/demo-rejected.sh
```

Scoring is the slow part — roughly three MiniLM inferences per row — so budget about
25 minutes per binary over the recent corpus on 13 cores. Results are cached by module
hash, so a re-run against an unchanged binary is instant.
