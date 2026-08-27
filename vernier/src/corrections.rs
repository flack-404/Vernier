//! Vernier's bounded correction layer.
//!
//! # Why this exists
//!
//! The baseline scorer this crate forks compares *meaning*. It embeds the ground
//! truth and the miner's answer with MiniLM-L6-v2 and measures cosine similarity,
//! so two sentences about the same subject score alike whatever they assert.
//! `"$94.06"` and `"$94,060"` point in almost exactly the same direction.
//!
//! That is not hypothetical. On a live `WALLET_BALANCE_CHECK` round the ground
//! truth was a balance of **0 ETH on Arbitrum**; one miner reported 0.009135 and
//! another reported 3.977 ETH **on Ethereum**, a different chain entirely. They
//! scored 0.9920 and 0.9901.
//!
//! # Why it is bounded
//!
//! Telegraph will not activate a scoring script whose ranking of miners
//! correlates below 0.60 with the incumbent's, per intent, measured on intents
//! the author does not choose. Five scripts have been submitted; all five were
//! rejected, three of them on exactly this check. A stricter scorer disagrees
//! everywhere, correlation collapses, and the gate closes before anyone reads the
//! logic.
//!
//! So the correction is capped. The protocol's own genesis parameters fix the
//! window:
//!
//! ```text
//! delta_c       = 0.15   deviate further from consensus and a validator running
//!                        this script is penalised (Category C)
//! delta_promote = 0.10   score at least this far BELOW canonical on one answer
//!                        canonical rated above 0.70, and the script is promoted
//!                        to Canonical after T_promote = 3 epochs
//!
//!         0.10  <  |correction|  <  0.15      ->      0.12
//! ```
//!
//! # Why it is safe
//!
//! Every detector is gated on the ground truth containing something checkable.
//! Measured over 14,258 live rows in rankable intent groups, the four largest —
//! `TASK_COMPLETION`, `CHAT_COMPLETION`, `LANGUAGE_GENERATION` and `AGENT_TASK`,
//! together 70% of the corpus — contain **no digit at all** in their ground
//! truth. Not few: none. The detectors cannot fire there, so rank agreement on
//! those intents is unchanged exactly rather than approximately.
//!
//! That property is what replaces the "shrink the correction on low-agreement
//! intents" idea from the design notes, which cannot work: `rank_answer` receives
//! no intent parameter, so the module never learns which intent it is scoring.
//! Gating on the shape of the ground truth achieves the same end without needing
//! to know.

extern crate alloc;

use alloc::vec::Vec;

use crate::scan::{lower, scan_identifiers, scan_numbers, Ident, IdentKind, Number};

// ── Tuning constants ─────────────────────────────────────────────────────────
//
// Every value here was chosen by measurement against the live corpus, not by
// argument. `tg-score simulate` reproduces the effect of changing any of them.

/// Total clamp on the correction, in score units.
///
/// Inside `delta_c` (0.15) so a validator running this script is never penalised
/// for deviation, and above `delta_promote` (0.10) so a genuine catch can satisfy
/// the automatic promotion condition.
pub const MAX_CORRECTION: f32 = 0.12;

/// Ceiling on the numeric detector's contribution.
const W_NUMERIC: f32 = 0.08;

/// Ceiling on the identifier detector's contribution.
const W_IDENT: f32 = 0.06;

/// Penalty for refusing to answer against a substantive ground truth.
const W_REFUSAL: f32 = 0.06;

/// Relative band within which two figures count as equal.
///
/// Ground truth is captured at one instant and the miner answers at another, and
/// prices move in between. An exact-match rule would punish correct answers for
/// the clock rather than for being wrong.
const REL_TOLERANCE: f64 = 0.005;

/// How many ground-truth figures an answer is held to.
///
/// A ground truth asserts one or two things; its remaining numbers are
/// timestamps, list indices and incidental detail.
const MAX_HEADLINE: usize = 2;

/// Number count below which no salience filtering happens at all.
const SALIENCE_FLOOR: usize = 3;

/// Non-currency integers below this are dropped from the headline set when the
/// ground truth is number-dense — list indices and "top 5" counts.
const SMALL_INT_CUTOFF: f64 = 10.0;

/// Comparable figures beyond which a match stops counting as evidence.
///
/// Matching uses a relative tolerance, so an answer spraying K numbers covers
/// roughly `K * 2 * tolerance` of the value space by chance: 900 consecutive
/// integers match any headline below 900 outright. One live `STOCK_PRICE` answer
/// carried 6,972 numeric tokens. Past this threshold the detector treats the
/// answer as asserting nothing and stays silent — neither penalising it nor
/// letting it buy a match.
const FLOOD_THRESHOLD: usize = 48;

/// How far into a text refusal markers are looked for.
const REFUSAL_WINDOW: usize = 240;

/// The correction layer's verdict for one triple.
#[derive(Clone, Copy, Default)]
pub struct Correction {
    /// Total adjustment, always `<= 0` and never beyond [`MAX_CORRECTION`].
    pub total: f32,
    pub numeric: f32,
    pub ident: f32,
    pub refusal: f32,
    /// The ground truth was itself a refusal, so no correction was applied.
    pub gt_refused: bool,
    /// Matched fraction of headline figures, or `-1.0` when nothing was checked.
    pub numeric_coverage: f32,
    /// Matched fraction of identifiers, or `-1.0` when nothing was checked.
    pub ident_coverage: f32,
}

/// Compute the bounded correction for one triple.
///
/// The correction is only ever negative. Vernier can lower a score the baseline
/// gave too generously; it never raises one. Raising would assert the baseline
/// was too harsh — a judgement this module has no evidence for — and would put
/// the layer on the wrong side of the Catch-Rate condition, which is defined as
/// scoring *below* canonical.
pub fn analyse(_question: &str, ground_truth: &str, answer: &str) -> Correction {
    let mut c = Correction {
        numeric_coverage: -1.0,
        ident_coverage: -1.0,
        ..Default::default()
    };

    let gt = ground_truth.as_bytes();
    let ma = answer.as_bytes();

    // An empty answer already scores 0 from the baseline's own early return.
    // Correcting it further is meaningless and would drive the composite negative.
    if is_blank(ma) {
        return c;
    }

    // ── Detector 3, anti-gaming half ─────────────────────────────────────────
    //
    // When the ground truth is itself a refusal it carries nothing to check, so
    // every other detector would be comparing against noise. This is 1.3% of live
    // rows, and their published scores span the entire range: on one
    // `FINANCIAL_DATA` round the ground truth read "I don't have the exact
    // All-in Sustaining Costs" and a miner answered with its own refusal — and
    // scored a perfect 1.0000.
    //
    // Returning the baseline untouched is both the honest reading (we cannot tell
    // who is right when the reference asserts nothing) and the safe one (a
    // correction applied against a noisy reference can only erode rank agreement).
    if is_refusal(gt) {
        c.gt_refused = true;
        return c;
    }

    // ── Detector 3, quality half ─────────────────────────────────────────────
    //
    // A refusal against a substantive ground truth is a non-answer. The baseline
    // scores it on topical similarity, and a fluent refusal is highly similar to
    // the question it declines to answer.
    if is_refusal(ma) {
        c.refusal = -W_REFUSAL;
    }

    let gt_ids = scan_identifiers(gt);
    let ma_ids = scan_identifiers(ma);

    // ── Detector 2, identifier fidelity ──────────────────────────────────────
    //
    // There is no such thing as a nearly-correct transaction hash. Self-gating:
    // with no identifier in the ground truth there is nothing to check, which
    // silences this detector on every prose intent.
    if !gt_ids.is_empty() {
        let (matched, total) = ident_coverage(&gt_ids, &ma_ids);
        if total > 0 {
            let cov = matched as f32 / total as f32;
            c.ident_coverage = cov;
            c.ident = -W_IDENT * (1.0 - cov);
        }
    }

    // ── Detector 1, numeric fidelity ─────────────────────────────────────────
    //
    // Penalises CONTRADICTION, never omission. An answer that does not mention a
    // figure is incomplete; an answer that states a different one is wrong, and
    // only the second is the blind spot this layer exists to close.
    //
    // The distinction is not academic. Under an earlier coverage-based rule, a
    // miner answering $496.37 against a ground truth of $496.35 was penalised
    // 0.064 for failing to echo the ground truth's capture timestamp, while a
    // miner that emitted 6,972 numbers matched everything by accident and escaped.
    let (cov, contradicted, checked) = numeric_verdict(gt, ma, &gt_ids, &ma_ids);
    if checked > 0 {
        c.numeric_coverage = cov;
        c.numeric = -W_NUMERIC * (contradicted as f32 / checked as f32);
    }

    let mut total = c.numeric + c.ident + c.refusal;
    if total < -MAX_CORRECTION {
        total = -MAX_CORRECTION;
    }
    if total > 0.0 {
        total = 0.0;
    }
    c.total = total;
    c
}

/// Compare the ground truth's headline figures against the answer's.
///
/// `checked` counts only headline figures the answer engaged with — ones for
/// which it offered a comparable number. `contradicted` counts those it answered
/// differently. A figure the answer is silent about is neither, so omission
/// carries no penalty.
fn numeric_verdict(gt: &[u8], ma: &[u8], gt_ids: &[Ident], ma_ids: &[Ident]) -> (f32, usize, usize) {
    let gt_nums = scan_numbers(gt, gt_ids);
    let heads = headline(&gt_nums);
    if heads.is_empty() {
        return (-1.0, 0, 0);
    }
    let ma_nums = scan_numbers(ma, ma_ids);
    if ma_nums.is_empty() {
        return (-1.0, 0, 0);
    }

    let mut matched = 0usize;
    let mut contradicted = 0usize;
    let mut checked = 0usize;

    for h in heads.iter() {
        // A price is compared against prices and a percentage against
        // percentages. Notation is the only type information available, and it
        // keeps "up 4.78%" from being checked against a market cap.
        //
        // Counted rather than collected: an answer can carry thousands of numeric
        // tokens (6,972 on one live row), and materialising that set once per
        // headline figure would allocate heavily inside the sandbox for no reason.
        let same_type = |n: &Number| n.currency == h.currency && n.percent == h.percent;
        let typed_count = ma_nums.iter().filter(|n| same_type(n)).count();

        // Most miners answer in raw JSON (`"price_usd":496.37`) with no currency
        // symbol anywhere, so without this fallback the detector would fall silent
        // on the majority of numeric answers.
        let fallback = typed_count == 0;
        let peer_count = if fallback { ma_nums.len() } else { typed_count };

        if peer_count > FLOOD_THRESHOLD {
            continue;
        }

        checked += 1;
        let found = ma_nums
            .iter()
            .any(|p| (fallback || same_type(p)) && numbers_equal(h.value, p.value));
        if found {
            matched += 1;
        } else {
            contradicted += 1;
        }
    }

    if checked == 0 {
        return (-1.0, 0, 0);
    }
    (matched as f32 / checked as f32, contradicted, checked)
}

/// Pick the few figures a ground truth is actually asserting.
///
/// Currency and percentage figures come first, because their notation marks them
/// as the claim. Otherwise the largest remaining magnitude wins, once years and
/// small integers have been dropped — those are the clock and the list indices.
///
/// Filtering is skipped for sparse ground truths and is never allowed to empty
/// the set. One live `WALLET_BALANCE_CHECK` ground truth asserts a balance of
/// exactly "0 ETH"; dropping small integers there would silence the detector on
/// the clearest catch in the corpus.
fn headline(nums: &[Number]) -> Vec<Number> {
    if nums.is_empty() {
        return Vec::new();
    }

    let mut pool: Vec<Number> = nums.to_vec();
    if nums.len() > SALIENCE_FLOOR {
        let filtered: Vec<Number> = nums
            .iter()
            .copied()
            .filter(|n| {
                if n.currency || n.percent {
                    return true;
                }
                if is_year(n.value) {
                    return false;
                }
                if n.value >= 0.0 && n.value < SMALL_INT_CUTOFF && is_integer(n.value) {
                    return false;
                }
                true
            })
            .collect();
        if !filtered.is_empty() {
            pool = filtered;
        }
    }

    let typed: Vec<Number> = pool.iter().copied().filter(|n| n.currency || n.percent).collect();
    if !typed.is_empty() {
        return cap_head(typed);
    }

    let mut ranked = pool;
    sort_by_magnitude_desc(&mut ranked);
    cap_head(ranked)
}

fn cap_head(mut ns: Vec<Number>) -> Vec<Number> {
    if ns.len() > MAX_HEADLINE {
        ns.truncate(MAX_HEADLINE);
    }
    ns
}

/// Insertion sort by absolute value, largest first, ties broken by position.
///
/// Ties are broken explicitly so the result never depends on sort stability, and
/// so this ordering is identical to the Go reference implementation's.
fn sort_by_magnitude_desc(ns: &mut [Number]) {
    for i in 1..ns.len() {
        let mut j = i;
        while j > 0 {
            let a = ns[j - 1];
            let b = ns[j];
            let (aa, ab) = (abs_f64(a.value), abs_f64(b.value));
            if ab > aa || (ab == aa && b.start < a.start) {
                ns.swap(j - 1, j);
                j -= 1;
            } else {
                break;
            }
        }
    }
}

/// Compare with a relative tolerance, falling back to an absolute comparison near
/// zero where a relative band collapses to nothing.
fn numbers_equal(a: f64, b: f64) -> bool {
    let d = abs_f64(a - b);
    if d == 0.0 {
        return true;
    }
    let m = if abs_f64(a) > abs_f64(b) { abs_f64(a) } else { abs_f64(b) };
    if m < 1e-9 {
        return d < 1e-9;
    }
    d <= REL_TOLERANCE * m
}

#[inline]
fn abs_f64(v: f64) -> f64 {
    if v < 0.0 {
        -v
    } else {
        v
    }
}

fn is_integer(v: f64) -> bool {
    v == (v as i64) as f64
}

fn is_year(v: f64) -> bool {
    is_integer(v) && (1900.0..=2100.0).contains(&v)
}

/// Count how many ground-truth identifiers the answer reproduces.
///
/// ISO dates are deliberately excluded. A date in a ground truth is almost always
/// echoed from the question — the range the caller asked about — rather than a
/// fact the answer must reproduce, and an answer legitimately stamps itself with a
/// different `as_of`. On a live WEATHER_FORECAST round the ground truth carried the
/// requested forecast range 2026-09-01..2026-09-07 while the miner stamped
/// 2026-08-26, and counting that as a 50% identifier miss cost the answer 0.03 for
/// nothing. Transaction hashes and CVE identifiers have no such excuse: they are
/// exactly right or invented.
fn ident_coverage(gt: &[Ident], ma: &[Ident]) -> (usize, usize) {
    let mut matched = 0usize;
    let mut total = 0usize;
    for g in gt.iter() {
        if g.kind == IdentKind::Date {
            continue;
        }
        total += 1;
        if ma.iter().any(|m| ident_equal(g, m)) {
            matched += 1;
        }
    }
    (matched, total)
}

/// Shortest hex body for a prefix match to be credible.
const HEX_PREFIX_MIN: usize = 24;

fn ident_equal(a: &Ident, b: &Ident) -> bool {
    if a.kind != b.kind {
        return false;
    }
    if a.text == b.text {
        return true;
    }
    if a.kind == IdentKind::Hex && a.text.len() > 2 && b.text.len() > 2 {
        // Some ground truths on this network carry malformed addresses — one live
        // WALLET_BALANCE_CHECK ground truth holds a 41-hex-digit address, one
        // digit too long — and an exact-match rule would penalise miners for
        // normalising them. Compare the bodies after "0x".
        let x = &a.text[2..];
        let y = &b.text[2..];
        let n = if x.len() < y.len() { x.len() } else { y.len() };
        if n >= HEX_PREFIX_MIN && x[..n] == y[..n] {
            return true;
        }
    }
    false
}

// ── Refusal detection ────────────────────────────────────────────────────────

/// Markers matched case-insensitively against the opening of a text.
///
/// Scoped to the opening deliberately: a substantive answer often contains a
/// hedge somewhere in its body ("I cannot be certain that…"), whereas a refusal
/// announces itself immediately.
const REFUSAL_MARKERS: [&[u8]; 20] = [
    b"i cannot",
    b"i can't",
    b"i can not",
    b"cannot provide",
    b"can't provide",
    b"cannot directly",
    b"sorry, i",
    b"i'm sorry",
    b"i am sorry",
    b"i do not have",
    b"i don't have",
    b"unable to provide",
    b"unable to determine",
    b"not able to provide",
    b"i'm not able",
    b"i am not able",
    b"no data available",
    b"not explicitly provided",
    b"insufficient data",
    b"i'm unable",
];

fn is_refusal(s: &[u8]) -> bool {
    let n = if s.len() > REFUSAL_WINDOW { REFUSAL_WINDOW } else { s.len() };
    let head = &s[..n];
    REFUSAL_MARKERS.iter().any(|m| contains_fold(head, m))
}

/// ASCII case-insensitive substring search.
///
/// Hand-rolled rather than lowercasing a copy first: that would allocate a buffer
/// the size of the input on every call, for every marker.
fn contains_fold(hay: &[u8], needle: &[u8]) -> bool {
    if needle.is_empty() || hay.len() < needle.len() {
        return false;
    }
    let last = hay.len() - needle.len();
    for i in 0..=last {
        let mut ok = true;
        for j in 0..needle.len() {
            if lower(hay[i + j]) != needle[j] {
                ok = false;
                break;
            }
        }
        if ok {
            return true;
        }
    }
    false
}

fn is_blank(s: &[u8]) -> bool {
    s.iter()
        .all(|&c| c == b' ' || c == b'\t' || c == b'\n' || c == b'\r' || c == 0x0b || c == 0x0c)
}

// ── Tests ────────────────────────────────────────────────────────────────────
//
// These cover the layer's invariants and its behaviour on rows drawn from live
// network data. Semantic coverage is broader on the Go side
// (tg-score/internal/detect), and `tg-score verify` proves the two implementations
// agree on every row of the 19,526-row corpus — so these exist to catch a break
// here without needing the corpus or a WASM host.

#[cfg(test)]
mod tests {
    use super::*;

    /// The property that makes the design safe: a ground truth with no digits
    /// cannot produce a numeric correction, so the four largest rankable intents —
    /// 70% of the live corpus, none of whose ground truths contain a digit — are
    /// untouched exactly rather than approximately.
    #[test]
    fn digit_free_ground_truth_is_untouched() {
        let c = analyse(
            "Write a haiku about autumn",
            "The assistant produced three lines in the traditional form, evoking falling leaves.",
            "Crimson leaves descend, whispering across the stones, autumn breathes and rests.",
        );
        assert_eq!(c.total, 0.0, "layer fired on a digit-free ground truth");
    }

    /// From a live WALLET_BALANCE_CHECK round: ground truth 0 ETH on Arbitrum, the
    /// miner reported 3.977 ETH on Ethereum, and the network scored it 0.9901.
    #[test]
    fn numeric_contradiction_is_caught() {
        let c = analyse(
            "What is the balance?",
            "The address currently has a native-coin balance of **0 ETH** on Arbitrum.",
            r#"{"balance_eth":3.977664413963611,"chain":"ethereum"}"#,
        );
        assert!(c.numeric < 0.0, "expected a numeric penalty, got {:?}", c.numeric);
        assert!(c.total >= -MAX_CORRECTION);
    }

    #[test]
    fn faithful_answer_is_not_penalised() {
        let c = analyse(
            "Price of SOL?",
            "The current price of Solana (SOL) is approximately $101.03 USD.",
            "SOL is trading at $101.03 USD.",
        );
        assert_eq!(c.total, 0.0, "a faithful answer was penalised");
    }

    /// Ground truth is captured at one instant and the miner answers at another.
    /// Small drift is the clock, not an error.
    #[test]
    fn small_drift_tolerated_large_error_caught() {
        let gt = "Solana is trading at $101.03 USD.";
        assert_eq!(analyse("q", gt, "SOL is $101.05 USD.").total, 0.0);
        assert!(analyse("q", gt, "SOL is $94,060 USD.").numeric < 0.0);
    }

    /// A correct price must not be penalised for omitting the ground truth's
    /// capture timestamp. Live STOCK_PRICE row, miner txlens.
    #[test]
    fn timestamp_omission_is_not_contradiction() {
        let c = analyse(
            "Share price of MSFT?",
            "The current share price of Microsoft (MSFT) is $496.35 as of August 26, 2026, at 12:59:59 PM Pacific Time.",
            r#"{"price_usd":496.37,"summary":"MSFT is $496.37"}"#,
        );
        assert_eq!(c.total, 0.0, "penalised for not echoing the clock: {:?}", c.total);
    }

    /// Omission is not contradiction; the baseline's own signals handle
    /// incompleteness.
    #[test]
    fn silence_about_a_figure_is_not_penalised() {
        let c = analyse(
            "Share price of MSFT?",
            "The current share price of Microsoft (MSFT) is $496.35.",
            "Microsoft trades on the NASDAQ under the ticker MSFT.",
        );
        assert_eq!(c.numeric, 0.0);
    }

    /// Anti-gaming: matching uses a relative tolerance, so an answer spraying
    /// hundreds of numbers would otherwise cover the whole value space by chance.
    #[test]
    fn number_flood_neither_matches_nor_is_penalised() {
        let mut flood = String::from("Findings: ");
        for i in 0..900 {
            flood.push_str(&alloc::format!("{} ", i));
        }
        let c = analyse(
            "Share price of MSFT?",
            "The current share price of Microsoft (MSFT) is $496.35.",
            &flood,
        );
        assert_eq!(c.numeric, 0.0, "a flood must assert nothing either way");
        assert!(c.numeric_coverage < 0.0, "a flood must not buy a match");
    }

    /// The anti-gaming half of detector 3, from a live FINANCIAL_DATA row that
    /// scored 1.0000: the ground truth was itself a refusal and the miner echoed
    /// one back.
    #[test]
    fn refusal_ground_truth_returns_baseline_untouched() {
        let c = analyse(
            "What is FCX's AISC?",
            "I don't have the exact All-in Sustaining Costs (AISC) for Freeport-McMoRan as of August 2026.",
            r#"{"signal":"I don't have verified data on Freeport-McMoRan's costs."}"#,
        );
        assert!(c.gt_refused);
        assert_eq!(c.total, 0.0);
    }

    #[test]
    fn refusal_against_substantive_ground_truth_is_penalised() {
        let c = analyse(
            "Capital of France?",
            "Paris is the capital of France, with a population of about 2.1 million.",
            "I'm sorry, I cannot provide that information.",
        );
        assert!(c.refusal < 0.0);
    }

    /// A date in a ground truth is the range the caller asked about, not a fact the
    /// answer must echo. From a live WEATHER_FORECAST round: the ground truth
    /// carried the requested forecast range while the miner stamped its own as_of.
    #[test]
    fn iso_dates_are_not_treated_as_identifiers() {
        let c = analyse(
            "7-day forecast for Tokyo starting 2026-09-01?",
            "Here is the 7-day forecast for Tokyo starting from 2026-09-01 through 2026-09-07.",
            r#"{"as_of":"2026-08-26","summary":"Tokyo: high 34C low 26C partly cloudy."}"#,
        );
        assert_eq!(c.ident, 0.0, "a date mismatch must not draw an identifier penalty");
    }

    #[test]
    fn identifier_mismatch_is_caught_and_exact_match_is_not() {
        let gt = "Transaction 0xabc123def4567890123456789012345678901234 settled.";
        assert!(analyse("q", gt, "see 0xffffffffffffffffffffffffffffffffffffffff").ident < 0.0);
        assert_eq!(
            analyse("q", gt, "see 0xABC123DEF4567890123456789012345678901234").ident,
            0.0,
            "case must not affect identifier equality"
        );
    }

    /// The contract with delta_c: no input may push the correction past the clamp,
    /// however many detectors fire at once.
    #[test]
    fn total_never_exceeds_the_clamp() {
        let c = analyse(
            "Details?",
            "Transaction 0xabc123def4567890123456789012345678901234 moved $1,234,567.89 on 2026-08-26, CVE-2021-44228 referenced.",
            "I cannot provide that. Perhaps $42 or 0xffffffffffffffffffffffffffffffffffffffff on 1999-01-01.",
        );
        assert!(c.total >= -MAX_CORRECTION, "clamp breached: {}", c.total);
        assert!(c.total < 0.0, "expected a penalty on a wholly wrong answer");
    }

    /// The layer may only ever lower a score. Raising one would assert the
    /// baseline was too harsh, and would put the layer on the wrong side of the
    /// Catch-Rate condition.
    #[test]
    fn correction_is_never_positive() {
        for (gt, ma) in [
            ("gt with $1.00", "answer with $1.00"),
            ("gt with no figures", "answer with $5,000"),
            ("CVE-2021-44228 disclosed", "CVE-2021-44228 disclosed"),
            ("balance is 0 ETH", ""),
        ] {
            assert!(analyse("q", gt, ma).total <= 0.0, "positive correction for {gt:?}/{ma:?}");
        }
    }

    /// An empty answer already scores 0 from the baseline's own early return.
    #[test]
    fn empty_answer_is_not_corrected() {
        for ma in ["", "   ", "\n\t "] {
            assert_eq!(analyse("q", "The price is $94.06 and the count is 1,234.", ma).total, 0.0);
        }
    }
}
