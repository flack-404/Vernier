//! BM25 single-document lexical scorer.
//!
//! Standard BM25 requires a corpus to compute IDF. For our use case —
//! scoring one miner answer against one ground-truth string — we use a
//! simplified single-document variant where IDF is treated as constant
//! (every query term is assumed to be relevant). This reduces the formula to
//! a TF-saturation model that rewards:
//!
//!   - Exact keyword overlap with the ground truth
//!   - Longer, more complete answers (up to a natural saturation point)
//!   - Without over-rewarding repetition (k1 saturation)
//!
//! Parameters: k1 = 1.5, b = 0.75 (standard TREC values).
//! Output is normalised to [0, 1] so it can be combined linearly with
//! cosine similarity scores in `rank_answer`.

extern crate alloc;

use alloc::{string::String, vec::Vec};

const K1: f32 = 1.5;
const B: f32 = 0.75;

/// Score `doc` against `query`.
///
/// Both strings are lowercased and split on non-alphanumeric characters.
/// Returns a value in [0, 1].
pub fn score(query: &str, doc: &str) -> f32 {
    let q_terms = tokenise(query);
    let d_terms = tokenise(doc);

    if q_terms.is_empty() || d_terms.is_empty() {
        return 0.0;
    }

    // Term frequency map for the doc
    // Using a Vec of (term, count) pairs — no_std compatible, small input size
    // means linear scan is fine (< 200 terms in practice).
    let mut tf: Vec<(&str, f32)> = Vec::new();
    for term in &d_terms {
        if let Some(entry) = tf.iter_mut().find(|(t, _)| *t == term.as_str()) {
            entry.1 += 1.0;
        } else {
            tf.push((term.as_str(), 1.0));
        }
    }

    let doc_len = d_terms.len() as f32;
    // Use average of query and doc length as proxy for avgdl.
    // This keeps length normalisation meaningful for single-pair scoring.
    let avg_dl = ((q_terms.len() + d_terms.len()) as f32) / 2.0;

    let mut raw = 0.0f32;
    let mut max_raw = 0.0f32;

    for term in &q_terms {
        let tf_val = tf
            .iter()
            .find(|(t, _)| *t == term.as_str())
            .map(|(_, c)| *c)
            .unwrap_or(0.0);

        // BM25 TF component (IDF = 1.0 constant — single document)
        let tf_norm = (tf_val * (K1 + 1.0)) / (tf_val + K1 * (1.0 - B + B * doc_len / avg_dl));

        raw += tf_norm;
        max_raw += K1 + 1.0; // upper bound when TF → ∞
    }

    if max_raw == 0.0 {
        return 0.0;
    }

    crate::math::clamp01(raw / max_raw)
}

/// Tokenise `text` into lowercase alphanumeric words, minimum length 2.
fn tokenise(text: &str) -> Vec<String> {
    text.split(|c: char| !c.is_alphanumeric())
        .filter(|s| s.len() >= 2)
        .map(|s| {
            s.chars()
                .map(|c| {
                    if c.is_ascii_uppercase() {
                        (c as u8 + 32) as char
                    } else {
                        c
                    }
                })
                .collect()
        })
        .collect()
}

// ── Tests ──────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    // NOTE: this test's expectation was corrected from `> 0.85`, which is
    // unreachable. The upstream baseline ships it asserting 0.85, but its
    // `#[panic_handler]` was unconditional, so `cargo test` could never build for
    // the host target and the assertion had never been executed. Gating that
    // handler behind `cfg(test)` — the upstream README's own suggested fix —
    // makes the suite runnable and this failure immediately visible.
    //
    // The IMPLEMENTATION is correct and is deliberately left untouched: it is what
    // the network deploys, and matching it is what earns rank agreement.
    //
    // The arithmetic: `score` normalises by `max_raw`, the ceiling each term would
    // reach as its term frequency tends to infinity (`K1 + 1` = 2.5). A term that
    // appears exactly once contributes
    //
    //     tf_norm = (1 * (K1+1)) / (1 + K1*(1 - B + B*doc_len/avg_dl))
    //             = 2.5 / (1 + 1.5*(0.25 + 0.75))
    //             = 1.0
    //
    // when doc_len == avg_dl, which holds whenever query and document are equal.
    // So an exact match over N distinct terms scores exactly N/(N*2.5) = 0.4,
    // whatever N is. Any expectation above 0.4 is unsatisfiable for an exact match
    // of distinct terms.
    #[test]
    fn exact_match_scores_high() {
        let s = score(
            "the capital of france is paris",
            "the capital of france is paris",
        );
        assert!(
            (s - 0.4).abs() < 1e-6,
            "exact match of 6 distinct terms should be exactly 0.4, got {s:.4}"
        );
    }

    /// Repetition must raise the score toward the ceiling without reaching it —
    /// this is what makes 0.4 the exact-match value rather than the maximum, and
    /// it is also why the baseline rewards padding, which detector 4 addresses.
    #[test]
    fn repetition_scores_above_exact_match() {
        let exact = score("paris france", "paris france");
        let repeated = score("paris france", "paris france paris france paris france");
        assert!(
            repeated > exact,
            "repetition should score above an exact match ({repeated:.4} vs {exact:.4})"
        );
        assert!(repeated < 1.0, "score must stay below the tf-infinity ceiling");
    }

    #[test]
    fn zero_overlap_scores_zero() {
        let s = score("france paris capital", "banana mango tropical fruit");
        assert!(s < 0.05, "no overlap should be < 0.05, got {s:.4}");
    }

    #[test]
    fn partial_overlap_in_range() {
        let s = score(
            "capital of france",
            "france is a country with paris as its main city",
        );
        assert!(
            s > 0.1 && s < 0.9,
            "partial overlap should be mid-range, got {s:.4}"
        );
    }

    #[test]
    fn empty_query_returns_zero() {
        assert_eq!(score("", "some document text"), 0.0);
    }

    #[test]
    fn empty_doc_returns_zero() {
        assert_eq!(score("some query", ""), 0.0);
    }
}
