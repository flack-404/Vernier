package detect

// Config parameterises the correction layer.
//
// The total clamp is the load-bearing number. Telegraph's genesis parameters put
// two bounds on how far a challenger may sit from canonical:
//
//	delta_c       = 0.15  deviate further and a validator running the script is penalised
//	delta_promote = 0.10  disagree by at least this on an answer canonical rated >0.70
//	                      and the script is promoted to Canonical after T_promote epochs
//
// A correction must therefore land inside 0.10 < |c| < 0.15 to be simultaneously
// safe and promotable. 0.12 sits in that window with margin on both sides.
//
// The penalty weights are float32, not float64, and that is deliberate. The
// shipping module computes the correction in f32 and this package must model
// that arithmetic exactly, because `tg-score simulate` tunes against this
// implementation and `tg-score verify` compares the two bit for bit. Summing in
// f64 and narrowing once is NOT the same number: float32(-0.03) + float32(-0.06)
// differs in its last bits from float32(-0.03 + -0.06), and verify caught
// exactly that on a live TOKEN_HOLDER_COUNT row.
//
// Number VALUES stay f64 on both sides — answers carry figures like
// 3977664413963610716 wei, which f32 cannot hold to seven significant digits.
type Config struct {
	MaxCorrection float32 // total clamp, applied to the summed penalty
	NumericWeight float32 // maximum penalty from numeric infidelity alone
	IdentWeight   float32 // maximum penalty from identifier infidelity alone
	RefusalWeight float32 // penalty for refusing against a substantive ground truth

	// RelTolerance is the relative band within which two numbers count as equal.
	// Prices move between the instant ground truth is captured and the instant a
	// miner answers, so an exact-match rule would punish correct answers for the
	// clock rather than for being wrong.
	RelTolerance float64

	// IgnoreYears drops bare four-digit values in [1900, 2100] from the headline
	// set. Dates are handled by the identifier scanner; a year appearing loose in
	// prose is almost never the figure being asserted.
	IgnoreYears bool

	// MaxHeadline caps how many ground-truth figures an answer is held to.
	// A ground truth asserts one or two things; the rest of its numbers are
	// timestamps, list indices and incidental detail.
	MaxHeadline int

	// CrossTypeFallback lets a currency or percent headline figure be checked
	// against the answer's untyped numbers when the answer carries no figure of
	// the matching type. Most miners on this network answer in raw JSON
	// ("price_usd":496.37) with no currency symbol anywhere, so without this the
	// detector would fall silent on the majority of numeric answers.
	CrossTypeFallback bool

	// MaxGTNumbers is the number of figures beyond which a ground truth is treated
	// as a multi-fact TABLE rather than a claim, and the numeric detector declines
	// to act at all.
	//
	// The detector's whole model is "the ground truth asserts a headline figure;
	// does the answer contradict it". A 7-day forecast carrying 54 numbers asserts
	// no headline figure — it asserts fifty-four of them — and choosing two is
	// guessing. Measured on WEATHER_FORECAST, that guess is what turns the layer
	// from neutral into harmful.
	//
	// 0 disables the ceiling, which is the shipping default: measured on current
	// epochs a ceiling of 12 LOWERS mean agreement (0.8367 against 0.8450 with no
	// ceiling), because it silences the detector on exactly the dense numeric
	// answers where it was helping. It is kept configurable because it does help
	// marginally once stale epochs are pooled in, and that trade-off is worth
	// being able to re-measure rather than re-litigate.
	MaxGTNumbers int

	// CheckDates includes ISO dates in the identifier detector.
	//
	// Off by default. A date in a ground truth is usually echoed from the question
	// — the range the caller asked about — rather than a fact the answer must
	// reproduce, and a forecast legitimately stamps itself with a different
	// as_of date. Transaction hashes and CVE identifiers have no such excuse.
	CheckDates bool

	// FloodThreshold is the number of comparable figures beyond which a match
	// stops counting as evidence, in either direction.
	//
	// Matching uses a relative tolerance, so an answer spraying K numbers covers
	// roughly K*2*tol of the value space by chance alone: 900 consecutive
	// integers match any headline below 900 outright. One live STOCK_PRICE
	// answer carried 6,972 numeric tokens. Past this threshold the detector
	// treats the answer as asserting nothing and stays silent — neither
	// penalising it nor letting it buy a match — because a figure found among
	// thousands is not a claim. Padding is a separate problem with a separate
	// detector; this only stops padding from defeating THIS one.
	FloodThreshold int

	// IgnoreSmallInts drops non-currency integer values below SmallIntCutoff.
	// List indices, counts of bullet points and "top 5" style figures are
	// incidental to the claim and appear or vanish with formatting.
	IgnoreSmallInts bool
	SmallIntCutoff  float64

	// SalienceFloor is the number count below which no salience filtering is
	// applied at all. The filters above exist to suppress noise in number-dense
	// text; in a sparse ground truth every figure is the claim. A live
	// WALLET_BALANCE_CHECK ground truth asserts a balance of exactly "0 ETH",
	// and filtering small integers there would silence the detector on the
	// clearest catch in the corpus.
	SalienceFloor int

	// HexPrefixMatch treats two hex identifiers as equal when one is a prefix of
	// the other and both are at least HexPrefixMin digits long. Some ground
	// truths on this network carry malformed addresses — one live
	// WALLET_BALANCE_CHECK ground truth holds a 41-hex-digit address — and an
	// exact-match rule would penalise miners for normalising them.
	HexPrefixMatch bool
	HexPrefixMin   int
}

// DefaultConfig is the shipping configuration.
func DefaultConfig() Config {
	return Config{
		MaxCorrection:     0.12,
		NumericWeight:     0.08,
		IdentWeight:       0.06,
		RefusalWeight:     0.06,
		RelTolerance:      0.005,
		IgnoreYears:       true,
		IgnoreSmallInts:   true,
		SmallIntCutoff:    10,
		SalienceFloor:     3,
		MaxHeadline:       2,
		CrossTypeFallback: true,
		FloodThreshold:    48,
		MaxGTNumbers:      0,
		CheckDates:        false,
		HexPrefixMatch:    true,
		HexPrefixMin:      24,
	}
}

// Correction is the layer's verdict for one triple. All penalty fields are <= 0.
type Correction struct {
	Total   float32
	Numeric float32
	Ident   float32
	Refusal float32

	// GTRefused records that the ground truth was itself a refusal, in which case
	// the layer returns zero correction.
	GTRefused bool

	// NumericCoverage and IdentCoverage are the matched fractions, or -1 when the
	// detector had nothing to check.
	NumericCoverage float32
	IdentCoverage   float32
}

// Fired reports whether the layer changed the score at all.
func (c Correction) Fired() bool { return c.Total != 0 }

// Analyse computes the bounded correction for one (question, ground_truth,
// miner_answer) triple.
//
// The correction is only ever negative: Vernier can lower a score the baseline
// gave too generously, never raise one. Raising scores would mean asserting the
// baseline was too harsh, which requires knowledge the module does not have —
// and it would put the layer on the wrong side of the Catch-Rate condition,
// which is defined as scoring BELOW canonical.
func Analyse(question, groundTruth, answer string, cfg Config) Correction {
	c := Correction{NumericCoverage: -1, IdentCoverage: -1}

	// An empty answer already scores 0 under the baseline's own early return.
	// Correcting it further is meaningless and would push the composite negative.
	if isBlank(answer) {
		return c
	}

	// ── Detector 3, anti-gaming half ─────────────────────────────────────────
	// When the ground truth is itself a refusal it carries no content to check,
	// so every other detector would be comparing against noise. Measured on the
	// live corpus this is 1.3% of rows, and their published scores span the full
	// range: one FINANCIAL_DATA row where the ground truth reads "I don't have
	// the exact All-in Sustaining Costs" and the miner answered with its own
	// refusal scored a perfect 1.0000. Returning the baseline untouched here is
	// both the honest reading — we cannot tell who is right — and the safe one,
	// because a correction applied against a noisy reference can only erode rank
	// agreement.
	if isRefusal(groundTruth) {
		c.GTRefused = true
		return c
	}

	// ── Detector 3, quality half ─────────────────────────────────────────────
	// A refusal against a substantive ground truth is a non-answer. The baseline
	// scores it on topical similarity, and a fluent refusal is highly similar to
	// the question it declines.
	if isRefusal(answer) {
		c.Refusal = -cfg.RefusalWeight
	}

	gtIdents := ScanIdentifiers(groundTruth)
	maIdents := ScanIdentifiers(answer)

	// ── Detector 2, identifier fidelity ──────────────────────────────────────
	// Identifiers are exact or wrong; there is no "close" transaction hash.
	// Self-gating: with no identifier in the ground truth there is nothing to
	// check, which silences this detector on every prose intent.
	if len(gtIdents) > 0 {
		matched, total := identCoverage(gtIdents, maIdents, cfg)
		if total > 0 {
			cov := float32(matched) / float32(total)
			c.IdentCoverage = cov
			c.Ident = -cfg.IdentWeight * (1 - cov)
		}
	}

	// ── Detector 1, numeric fidelity ─────────────────────────────────────────
	// Self-gating in the same way, and this is what makes the whole design safe:
	// on the live corpus the four largest rankable intents — TASK_COMPLETION,
	// CHAT_COMPLETION, LANGUAGE_GENERATION and AGENT_TASK, together 70% of all
	// rankable rows — contain no digit at all in their ground truth. Not few:
	// none. This detector cannot fire there, so rank agreement on those intents
	// is unchanged exactly, not approximately.
	//
	// It penalises CONTRADICTION, never omission. An answer that does not mention
	// a figure is incomplete; an answer that states a different one is wrong, and
	// only the second is the blind spot this layer exists to close. The
	// distinction is not academic: measured against the earlier coverage-based
	// rule, a miner answering $496.37 to a ground truth of $496.35 was penalised
	// 0.064 for failing to echo the ground truth's timestamp, while a miner that
	// emitted 6,972 numbers matched everything by accident and escaped entirely.
	if cov, contradicted, checked := numericVerdict(groundTruth, answer, gtIdents, maIdents, cfg); checked > 0 {
		c.NumericCoverage = cov
		rate := float32(contradicted) / float32(checked)
		c.Numeric = -cfg.NumericWeight * rate
	}

	total := c.Numeric + c.Ident + c.Refusal
	if total < -cfg.MaxCorrection {
		total = -cfg.MaxCorrection
	}
	if total > 0 {
		total = 0
	}
	c.Total = total
	return c
}

// Apply returns the corrected score, clamped to [0,1] as the baseline's own
// composite is.
//
// The arithmetic runs in float32 to mirror the shipping module, which adds the
// correction to an f32 composite and clamps with the baseline's own clamp01.
func Apply(baseline float64, c Correction) float64 {
	v := float32(baseline) + c.Total
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return float64(v)
}

// numericVerdict compares the ground truth's headline figures against the
// answer's, returning the matched fraction plus the counts the penalty is built
// from.
//
// checked counts only headline figures the answer actually engaged with — ones
// for which it offered a comparable number. contradicted counts those it
// answered differently. A headline figure the answer is simply silent about is
// neither, so omission carries no penalty.
func numericVerdict(groundTruth, answer string, gtIdents, maIdents []Ident, cfg Config) (coverage float32, contradicted, checked int) {
	gtNums := ScanNumbers(groundTruth, gtIdents)
	if cfg.MaxGTNumbers > 0 && len(gtNums) > cfg.MaxGTNumbers {
		return -1, 0, 0 // a table, not a claim
	}
	heads := headline(gtNums, cfg)
	if len(heads) == 0 {
		return -1, 0, 0
	}
	maNums := ScanNumbers(answer, maIdents)
	if len(maNums) == 0 {
		return -1, 0, 0
	}

	matched := 0
	for _, h := range heads {
		peers := sameType(maNums, h)
		if len(peers) == 0 {
			if !cfg.CrossTypeFallback {
				continue
			}
			peers = maNums
		}
		if cfg.FloodThreshold > 0 && len(peers) > cfg.FloodThreshold {
			continue // too many candidates for a match to mean anything
		}
		checked++
		if hasMatch(h, peers, cfg.RelTolerance) {
			matched++
		} else {
			contradicted++
		}
	}
	if checked == 0 {
		return -1, 0, 0
	}
	return float32(matched) / float32(checked), contradicted, checked
}

// sameType returns the answer numbers written in the same notation as want.
//
// A price is compared against prices and a percentage against percentages. It
// keeps "up 4.78%" from being checked against a market cap, which is how a
// correct answer ends up looking wrong.
func sameType(nums []Number, want Number) []Number {
	out := nums[:0:0]
	for _, n := range nums {
		if n.Currency == want.Currency && n.Percent == want.Percent {
			out = append(out, n)
		}
	}
	return out
}

// headline picks the few figures a ground truth is actually asserting.
//
// Currency and percentage figures come first because their notation marks them
// as the claim. Otherwise the largest remaining magnitude wins, years and small
// integers having been dropped — those are the clock and the list indices.
// Filtering is skipped for sparse ground truths, and never allowed to empty the
// set: one live WALLET_BALANCE_CHECK ground truth asserts a balance of exactly
// "0 ETH", and dropping small integers there would silence the detector on the
// clearest catch in the corpus.
func headline(nums []Number, cfg Config) []Number {
	if len(nums) == 0 {
		return nil
	}
	pool := nums
	if len(nums) > cfg.SalienceFloor {
		filtered := nums[:0:0]
		for _, n := range nums {
			if cfg.IgnoreYears && !n.Currency && !n.Percent && isYear(n.Value) {
				continue
			}
			if cfg.IgnoreSmallInts && !n.Currency && !n.Percent &&
				n.Value >= 0 && n.Value < cfg.SmallIntCutoff && isInteger(n.Value) {
				continue
			}
			filtered = append(filtered, n)
		}
		if len(filtered) > 0 {
			pool = filtered
		}
	}

	typed := pool[:0:0]
	for _, n := range pool {
		if n.Currency || n.Percent {
			typed = append(typed, n)
		}
	}
	if len(typed) > 0 {
		return capHead(typed, cfg.MaxHeadline)
	}

	ranked := make([]Number, len(pool))
	copy(ranked, pool)
	sortByMagnitudeDesc(ranked)
	return capHead(ranked, cfg.MaxHeadline)
}

func capHead(ns []Number, max int) []Number {
	if max > 0 && len(ns) > max {
		return ns[:max]
	}
	return ns
}

// sortByMagnitudeDesc orders by absolute value, largest first, breaking ties by
// position so the result never depends on sort stability.
func sortByMagnitudeDesc(ns []Number) {
	for i := 1; i < len(ns); i++ {
		for j := i; j > 0; j-- {
			a, b := ns[j-1], ns[j]
			if absf(b.Value) > absf(a.Value) || (absf(b.Value) == absf(a.Value) && b.Start < a.Start) {
				ns[j-1], ns[j] = b, a
				continue
			}
			break
		}
	}
}

func isYear(v float64) bool {
	return isInteger(v) && v >= 1900 && v <= 2100
}

func isInteger(v float64) bool {
	return v == float64(int64(v))
}

// hasMatch reports whether any candidate number equals want within tol.
func hasMatch(want Number, candidates []Number, tol float64) bool {
	for _, c := range candidates {
		if numbersEqual(want.Value, c.Value, tol) {
			return true
		}
	}
	return false
}

// numbersEqual compares with a relative tolerance, falling back to an absolute
// comparison near zero where a relative band collapses.
func numbersEqual(a, b, tol float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	if d == 0 {
		return true
	}
	m := absf(a)
	if absf(b) > m {
		m = absf(b)
	}
	if m < 1e-9 {
		return d < 1e-9
	}
	return d <= tol*m
}

func absf(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// identCoverage counts how many ground-truth identifiers the answer reproduces.
func identCoverage(gt, ma []Ident, cfg Config) (matched, total int) {
	for _, g := range gt {
		if g.Kind == IdentDate && !cfg.CheckDates {
			continue
		}
		total++
		for _, m := range ma {
			if identEqual(g, m, cfg) {
				matched++
				break
			}
		}
	}
	return matched, total
}

func identEqual(a, b Ident, cfg Config) bool {
	if a.Kind != b.Kind {
		return false
	}
	if a.Text == b.Text {
		return true
	}
	if a.Kind == IdentHex && cfg.HexPrefixMatch {
		// Compare the body after "0x".
		x, y := a.Text, b.Text
		if len(x) > 2 && len(y) > 2 {
			x, y = x[2:], y[2:]
			n := len(x)
			if len(y) < n {
				n = len(y)
			}
			if n >= cfg.HexPrefixMin && x[:n] == y[:n] {
				return true
			}
		}
	}
	return false
}

// ── Refusal detection ────────────────────────────────────────────────────────

// refusalMarkers are matched case-insensitively against the opening of a text.
//
// Scoped to the opening deliberately. A substantive answer often contains a
// hedge somewhere in its body ("I cannot be certain that..."); a refusal
// announces itself immediately.
var refusalMarkers = []string{
	"i cannot", "i can't", "i can not",
	"cannot provide", "can't provide", "cannot directly",
	"sorry, i", "i'm sorry", "i am sorry",
	"i do not have", "i don't have",
	"unable to provide", "unable to determine", "not able to provide",
	"i'm not able", "i am not able",
	"no data available", "not explicitly provided",
	"insufficient data", "i'm unable", "i am unable",
}

// refusalWindow is how far into a text refusal markers are looked for.
const refusalWindow = 240

func isRefusal(s string) bool {
	n := len(s)
	if n > refusalWindow {
		n = refusalWindow
	}
	head := s[:n]
	for _, m := range refusalMarkers {
		if containsFold(head, m) {
			return true
		}
	}
	return false
}

// containsFold is an ASCII case-insensitive substring search.
//
// Hand-rolled rather than using strings.Contains on a lowercased copy so the
// Rust port needs no allocation and no case-conversion pass over the input.
func containsFold(hay, needle string) bool {
	if len(needle) == 0 || len(hay) < len(needle) {
		return false
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		ok := true
		for j := 0; j < len(needle); j++ {
			if lower(hay[i+j]) != needle[j] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func isBlank(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' && c != '\v' && c != '\f' {
			return false
		}
	}
	return true
}
