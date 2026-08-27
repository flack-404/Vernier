//! Byte scanners for numbers and identifiers.
//!
//! Everything here works directly on `&[u8]`. `no_std` rules out the regex crate,
//! and the WASM sandbox rules out anything that would want a host — but the
//! bigger reason is determinism. Validators take a stake-weighted median of
//! scores and penalise deviation beyond `delta_c`, so every operation in this
//! module is integer or exact-IEEE float arithmetic with no transcendental calls
//! and no locale-dependent parsing.
//!
//! These scanners are the mirror of `tg-score/internal/detect/scan.go`. The two
//! are checked against each other row by row over the live corpus by
//! `tg-score verify`; if they ever diverge, that command fails.

extern crate alloc;

use alloc::vec::Vec;

/// A numeric literal recovered from text, normalised to its magnitude.
///
/// `value` is `f64` rather than `f32` deliberately. Answers on this network carry
/// figures like `3977664413963610716` wei, and `f32` carries only ~7 significant
/// digits — comparing those at `f32` would turn rounding into false mismatches.
/// Only the final correction is narrowed to `f32`, to match the composite it
/// adjusts.
#[derive(Clone, Copy, PartialEq, Debug)]
pub struct Number {
    pub value: f64,
    pub percent: bool,
    pub currency: bool,
    pub start: usize,
    pub end: usize,
}

/// The identifier classes worth checking exactly.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum IdentKind {
    /// `0x`-prefixed: contract addresses, transaction hashes.
    Hex,
    /// `CVE-YYYY-NNNN` security advisory identifiers.
    Cve,
    /// ISO-8601 calendar dates.
    Date,
}

/// A high-signal identifier: exactly right or wrong, with no notion of "close".
#[derive(Clone, PartialEq, Debug)]
pub struct Ident {
    pub kind: IdentKind,
    /// Case-normalised text: lowercase for hex, uppercase for CVE.
    pub text: Vec<u8>,
    pub start: usize,
    pub end: usize,
}

#[inline]
fn is_digit(c: u8) -> bool {
    c.is_ascii_digit()
}

#[inline]
fn is_hex_digit(c: u8) -> bool {
    c.is_ascii_hexdigit()
}

#[inline]
pub fn lower(c: u8) -> u8 {
    if c.is_ascii_uppercase() {
        c + 32
    } else {
        c
    }
}

#[inline]
fn upper(c: u8) -> u8 {
    if c.is_ascii_lowercase() {
        c - 32
    } else {
        c
    }
}

#[inline]
fn is_alpha(c: u8) -> bool {
    c.is_ascii_alphabetic()
}

/// Extract hex strings, CVE identifiers and ISO dates.
///
/// Run before [`scan_numbers`], whose `skip` argument takes the spans found here.
/// Without that, an address like `0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48`
/// decomposes into a dozen meaningless integers and drowns out the real numeric
/// signal on exactly the intents where identifiers matter most.
pub fn scan_identifiers(s: &[u8]) -> Vec<Ident> {
    let mut out: Vec<Ident> = Vec::new();
    let n = s.len();
    let mut i = 0usize;

    while i < n {
        // 0x-prefixed hex
        if s[i] == b'0' && i + 1 < n && lower(s[i + 1]) == b'x' {
            let mut j = i + 2;
            while j < n && is_hex_digit(s[j]) {
                j += 1;
            }
            // Six hex digits is the shortest span worth calling an identifier;
            // below that it is far more likely a colour or a short literal.
            if j - i - 2 >= 6 {
                let mut text = Vec::with_capacity(j - i);
                for k in i..j {
                    text.push(lower(s[k]));
                }
                out.push(Ident { kind: IdentKind::Hex, text, start: i, end: j });
                i = j;
                continue;
            }
        }

        // CVE-YYYY-NNNN...
        if (s[i] == b'C' || s[i] == b'c')
            && i + 3 < n
            && lower(s[i + 1]) == b'v'
            && lower(s[i + 2]) == b'e'
            && s[i + 3] == b'-'
        {
            let mut j = i + 4;
            let ds = j;
            while j < n && is_digit(s[j]) {
                j += 1;
            }
            if j - ds == 4 && j < n && s[j] == b'-' {
                j += 1;
                let ns = j;
                while j < n && is_digit(s[j]) {
                    j += 1;
                }
                if j - ns >= 4 {
                    let mut text = Vec::with_capacity(j - i);
                    for k in i..j {
                        text.push(upper(s[k]));
                    }
                    out.push(Ident { kind: IdentKind::Cve, text, start: i, end: j });
                    i = j;
                    continue;
                }
            }
        }

        // ISO date YYYY-MM-DD
        if is_digit(s[i]) && i + 9 < n {
            if is_digit(s[i + 1])
                && is_digit(s[i + 2])
                && is_digit(s[i + 3])
                && s[i + 4] == b'-'
                && is_digit(s[i + 5])
                && is_digit(s[i + 6])
                && s[i + 7] == b'-'
                && is_digit(s[i + 8])
                && is_digit(s[i + 9])
            {
                // Reject a date glued to a longer digit run on either side.
                let clean_left = i == 0 || !is_digit(s[i - 1]);
                let clean_right = i + 10 >= n || !is_digit(s[i + 10]);
                if clean_left && clean_right {
                    let text = s[i..i + 10].to_vec();
                    out.push(Ident { kind: IdentKind::Date, text, start: i, end: i + 10 });
                    i += 10;
                    continue;
                }
            }
        }

        i += 1;
    }
    out
}

/// Multiplier for a scale word starting at `i`, and the index just past it.
///
/// Returns `(1.0, i)` when there is no scale word, leaving the cursor untouched.
fn magnitude(s: &[u8], i: usize) -> (f64, usize) {
    let mut j = i;
    if j < s.len() && s[j] == b' ' {
        j += 1;
    }
    if j >= s.len() || !is_alpha(s[j]) {
        return (1.0, i);
    }
    let start = j;
    while j < s.len() && is_alpha(s[j]) {
        j += 1;
    }
    let w = &s[start..j];
    let mul = match w.len() {
        1 => match lower(w[0]) {
            b'k' => 1e3,
            b'm' => 1e6,
            b'b' => 1e9,
            b't' => 1e12,
            _ => 1.0,
        },
        2 => {
            let a = lower(w[0]);
            let b = lower(w[1]);
            match (a, b) {
                (b'm', b'm') | (b'm', b'n') => 1e6,
                (b'b', b'n') => 1e9,
                (b't', b'n') => 1e12,
                _ => 1.0,
            }
        }
        _ => {
            if eq_fold(w, b"thousand") {
                1e3
            } else if eq_fold(w, b"million") {
                1e6
            } else if eq_fold(w, b"billion") {
                1e9
            } else if eq_fold(w, b"trillion") {
                1e12
            } else {
                1.0
            }
        }
    };
    if mul == 1.0 {
        (1.0, i)
    } else {
        (mul, j)
    }
}

fn eq_fold(a: &[u8], b: &[u8]) -> bool {
    if a.len() != b.len() {
        return false;
    }
    for k in 0..a.len() {
        if lower(a[k]) != b[k] {
            return false;
        }
    }
    true
}

/// Extract numeric literals, skipping spans already claimed by an identifier.
///
/// `skip` must be sorted by `start`; pass the output of [`scan_identifiers`] on
/// the same input.
pub fn scan_numbers(s: &[u8], skip: &[Ident]) -> Vec<Number> {
    let mut out: Vec<Number> = Vec::new();
    let n = s.len();
    let mut i = 0usize;
    let mut si = 0usize;

    while i < n {
        while si < skip.len() && skip[si].end <= i {
            si += 1;
        }
        if si < skip.len() && i >= skip[si].start && i < skip[si].end {
            i = skip[si].end;
            continue;
        }
        if !is_digit(s[i]) {
            i += 1;
            continue;
        }

        let mut start = i;
        let mut neg = false;
        let mut currency = false;

        // Walk back over a sign and a currency marker.
        if i > 0 && s[i - 1] == b'-' {
            // A genuine minus, not a hyphen joining two words.
            if i - 1 == 0 || !is_alpha(s[i - 2]) {
                neg = true;
                start = i - 1;
            }
        }
        {
            let mut k = start;
            while k > 0 && s[k - 1] == b' ' {
                k -= 1;
            }
            if k > 0 && s[k - 1] == b'$' {
                currency = true;
                start = k - 1;
            }
        }

        // Integer part, allowing comma or underscore grouping.
        let mut int_digits: Vec<u8> = Vec::new();
        let mut j = i;
        while j < n {
            if is_digit(s[j]) {
                int_digits.push(s[j]);
                j += 1;
                continue;
            }
            if (s[j] == b',' || s[j] == b'_') && j + 1 < n && is_digit(s[j + 1]) {
                j += 1;
                continue;
            }
            break;
        }

        // Fractional part.
        let mut frac_digits: Vec<u8> = Vec::new();
        if j < n && s[j] == b'.' && j + 1 < n && is_digit(s[j + 1]) {
            j += 1;
            while j < n && is_digit(s[j]) {
                frac_digits.push(s[j]);
                j += 1;
            }
        }

        let mut val = digits_to_f64(&int_digits, &frac_digits);

        // Scientific notation.
        if j < n && (s[j] == b'e' || s[j] == b'E') {
            let mut k = j + 1;
            let mut esign = 1.0f64;
            if k < n && (s[k] == b'+' || s[k] == b'-') {
                if s[k] == b'-' {
                    esign = -1.0;
                }
                k += 1;
            }
            let es = k;
            while k < n && is_digit(s[k]) {
                k += 1;
            }
            if k > es && k - es <= 4 {
                let mut exp = 0f64;
                for p in es..k {
                    exp = exp * 10.0 + (s[p] - b'0') as f64;
                }
                val *= pow10(esign * exp);
                j = k;
            }
        }

        let mut percent = false;
        if j < n && s[j] == b'%' {
            percent = true;
            j += 1;
        } else {
            let (mul, nj) = magnitude(s, j);
            if mul != 1.0 {
                val *= mul;
                j = nj;
            }
        }
        if neg {
            val = -val;
        }

        out.push(Number { value: val, percent, currency, start, end: j });
        i = j;
    }
    out
}

/// Assemble a value from its digit bytes.
///
/// Built up digit by digit rather than via a string parse: `no_std` has no float
/// parser, and doing it this way means the Rust and Go implementations round
/// identically.
fn digits_to_f64(int_part: &[u8], frac_part: &[u8]) -> f64 {
    let mut v = 0f64;
    for &c in int_part {
        v = v * 10.0 + (c - b'0') as f64;
    }
    if !frac_part.is_empty() {
        let mut f = 0f64;
        let mut scale = 1f64;
        for &c in frac_part {
            f = f * 10.0 + (c - b'0') as f64;
            scale *= 10.0;
        }
        v += f / scale;
    }
    v
}

/// Integer power of ten by repeated multiplication.
///
/// Not `libm::pow`: this is exact for the small exponents that appear in
/// scientific notation, and repeated multiplication of a constant is bit-identical
/// everywhere, which `pow` is not obliged to be.
fn pow10(e: f64) -> f64 {
    let mut v = 1f64;
    let n = if e < 0.0 { -e } else { e } as i32;
    if e >= 0.0 {
        for _ in 0..n {
            v *= 10.0;
        }
    } else {
        for _ in 0..n {
            v /= 10.0;
        }
    }
    v
}

// ── Tests ────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    fn nums(s: &str) -> Vec<Number> {
        let b = s.as_bytes();
        scan_numbers(b, &scan_identifiers(b))
    }

    fn values(s: &str) -> Vec<f64> {
        nums(s).iter().map(|n| n.value).collect()
    }

    #[test]
    fn parses_plain_and_grouped_integers() {
        assert_eq!(values("USDC has 8,976,173 holders"), vec![8_976_173.0]);
        assert_eq!(values("block 25843057"), vec![25_843_057.0]);
    }

    #[test]
    fn parses_decimals_and_currency() {
        let n = &nums("SOL is $94.06 USD")[0];
        assert!((n.value - 94.06).abs() < 1e-9);
        assert!(n.currency, "leading $ must mark the figure as currency");
    }

    #[test]
    fn parses_percentages() {
        let n = &nums("up 4.78% today")[0];
        assert!((n.value - 4.78).abs() < 1e-9);
        assert!(n.percent);
    }

    #[test]
    fn applies_scale_words() {
        assert_eq!(values("market cap $59 billion"), vec![59e9]);
        assert_eq!(values("about 12k users"), vec![12e3]);
        // A word merely starting with a scale letter must not be read as a scale.
        assert_eq!(values("7 minutes"), vec![7.0]);
    }

    #[test]
    fn parses_scientific_notation() {
        assert_eq!(values("supply 1.5e9 tokens"), vec![1.5e9]);
        let v = values("value 2E-3 units");
        assert!((v[0] - 2e-3).abs() < 1e-12);
    }

    #[test]
    fn parses_negatives_but_not_hyphenated_words() {
        assert_eq!(values("temperature -3.5 degrees"), vec![-3.5]);
        // A hyphen joining words is not a minus sign.
        assert_eq!(values("a 24-hour window"), vec![24.0]);
    }

    /// The blind spot the whole module exists to close: these two differ by three
    /// orders of magnitude and are near-identical to an embedding model.
    #[test]
    fn separates_the_dollar_blind_spot() {
        assert_eq!(values("trading at $94.06"), vec![94.06]);
        assert_eq!(values("trading at $94,060"), vec![94_060.0]);
    }

    #[test]
    fn recognises_identifier_kinds() {
        let ids = scan_identifiers(b"CVE-2021-44228 on 2021-12-09 at 0xdeadbeefcafe1234");
        assert_eq!(ids.len(), 3);
        assert_eq!(ids[0].kind, IdentKind::Cve);
        assert_eq!(ids[0].text, b"CVE-2021-44228");
        assert_eq!(ids[1].kind, IdentKind::Date);
        assert_eq!(ids[2].kind, IdentKind::Hex);
    }

    #[test]
    fn hex_is_case_normalised() {
        let ids = scan_identifiers(b"0xA0b86991C6218B36c1d19D4a2e9Eb0cE3606eB48");
        assert_eq!(ids.len(), 1);
        assert_eq!(ids[0].text, b"0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48");
    }

    /// Without this, one address decomposes into a dozen meaningless integers and
    /// drowns out the real numeric signal.
    #[test]
    fn identifiers_are_withheld_from_the_number_scanner() {
        let s = "holders of 0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48 total 8,976,173";
        assert_eq!(values(s), vec![8_976_173.0]);
    }

    #[test]
    fn short_hex_is_not_an_identifier() {
        // Too short to be an address; must not be swallowed as one.
        assert!(scan_identifiers(b"colour 0xfff").is_empty());
    }

    #[test]
    fn a_date_glued_to_more_digits_is_not_a_date() {
        assert!(scan_identifiers(b"123456-12-09").is_empty());
    }

    #[test]
    fn empty_input_yields_nothing() {
        assert!(nums("").is_empty());
        assert!(scan_identifiers(b"").is_empty());
        assert!(nums("no digits at all here").is_empty());
    }
}
