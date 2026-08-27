package detect

import (
	"math"
	"testing"
)

func nums(t *testing.T, s string) []Number {
	t.Helper()
	return ScanNumbers(s, ScanIdentifiers(s))
}

func TestScanNumbersBasicForms(t *testing.T) {
	cases := []struct {
		in   string
		want []float64
	}{
		{"SOL is $94.06 USD", []float64{94.06}},
		{"SOL is $94,060 USD", []float64{94060}},
		{"a 24-hour increase of about +4.78%", []float64{24, 4.78}},
		{"market cap stands at around $59 billion", []float64{59e9}},
		{"USDC has 8,976,173 holders", []float64{8976173}},
		{"balance of 0.009135", []float64{0.009135}},
		{"temperature -3.5 degrees", []float64{-3.5}},
		{"value 1.5e9 tokens", []float64{1.5e9}},
		{"no numbers here at all", nil},
	}
	for _, c := range cases {
		got := nums(t, c.in)
		if len(got) != len(c.want) {
			t.Errorf("%q: got %d numbers %v, want %d %v", c.in, len(got), values(got), len(c.want), c.want)
			continue
		}
		for i := range c.want {
			if math.Abs(got[i].Value-c.want[i]) > 1e-6*math.Max(1, math.Abs(c.want[i])) {
				t.Errorf("%q: number %d = %v, want %v", c.in, i, got[i].Value, c.want[i])
			}
		}
	}
}

func values(ns []Number) []float64 {
	out := make([]float64, len(ns))
	for i, n := range ns {
		out[i] = n.Value
	}
	return out
}

// The blind spot this whole project exists to close. To the baseline these two
// sentences are near-identical; to the scanner they are three orders of
// magnitude apart.
func TestScannerSeparatesTheDollarBlindSpot(t *testing.T) {
	a := nums(t, "Solana is trading at $94.06")
	b := nums(t, "Solana is trading at $94,060")
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("expected one number each, got %v and %v", values(a), values(b))
	}
	if numbersEqual(a[0].Value, b[0].Value, 0.005) {
		t.Errorf("$94.06 and $94,060 must not compare equal")
	}
}

// An address must not decompose into a pile of meaningless integers.
func TestIdentifiersAreWithheldFromTheNumberScanner(t *testing.T) {
	s := "holders of 0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48 total 8,976,173"
	ids := ScanIdentifiers(s)
	if len(ids) != 1 || ids[0].Kind != IdentHex {
		t.Fatalf("expected one hex identifier, got %+v", ids)
	}
	got := ScanNumbers(s, ids)
	if len(got) != 1 || got[0].Value != 8976173 {
		t.Errorf("expected only the holder count to survive, got %v", values(got))
	}
}

func TestScanIdentifierKinds(t *testing.T) {
	s := "CVE-2021-44228 disclosed 2021-12-09 affecting 0xdeadbeefcafe1234"
	ids := ScanIdentifiers(s)
	if len(ids) != 3 {
		t.Fatalf("expected 3 identifiers, got %d: %+v", len(ids), ids)
	}
	if ids[0].Kind != IdentCVE || ids[0].Text != "CVE-2021-44228" {
		t.Errorf("bad CVE: %+v", ids[0])
	}
	if ids[1].Kind != IdentDate || ids[1].Text != "2021-12-09" {
		t.Errorf("bad date: %+v", ids[1])
	}
	if ids[2].Kind != IdentHex {
		t.Errorf("bad hex: %+v", ids[2])
	}
}

// The safety property the whole design rests on: a ground truth with no digits
// cannot produce a numeric correction, so the prose intents that dominate gate 3
// are untouched.
func TestNoCorrectionWhenGroundTruthHasNoNumbers(t *testing.T) {
	cfg := DefaultConfig()
	gt := "The user asked for a haiku about autumn, and the assistant produced three lines in the traditional form."
	ma := "Crimson leaves descend, whispering across the stones, autumn breathes and rests."
	c := Analyse("Write a haiku about autumn", gt, ma, cfg)
	if c.Fired() {
		t.Errorf("correction fired on a digit-free ground truth: %+v", c)
	}
}

// Drawn from a live WALLET_BALANCE_CHECK row: ground truth says 0 ETH on
// Arbitrum, the miner reported 3.977 ETH on Ethereum, and the network scored it
// 0.9901.
func TestNumericInfidelityIsCaught(t *testing.T) {
	cfg := DefaultConfig()
	gt := "The address `0x1234567890abcdef1234567890abcdef123456789` currently has a native-coin balance of **0 ETH** on Arbitrum."
	ma := `{"balance_eth":3.977664413963611,"chain":"ethereum","signal":"The native-coin balance is 3.977664413963611 ETH."}`
	c := Analyse("What is the balance?", gt, ma, cfg)
	if c.Numeric >= 0 {
		t.Errorf("expected a numeric penalty, got %+v", c)
	}
	if c.Total < -cfg.MaxCorrection {
		t.Errorf("total %.4f exceeded the clamp %.4f", c.Total, cfg.MaxCorrection)
	}
}

func TestCorrectAnswerIsNotPenalised(t *testing.T) {
	cfg := DefaultConfig()
	gt := "The current price of Solana (SOL) is approximately $101.03 USD, up 4.78% over 24 hours."
	ma := "SOL is trading at $101.03 USD, a 24-hour change of +4.78%."
	c := Analyse("Price of SOL?", gt, ma, cfg)
	if c.Fired() {
		t.Errorf("a faithful answer was penalised: %+v", c)
	}
}

// Prices move between ground-truth capture and the miner's reply, so a small
// relative drift must not be treated as an error.
func TestSmallPriceDriftIsToleratedButLargeErrorIsNot(t *testing.T) {
	cfg := DefaultConfig()
	gt := "Solana is trading at $101.03 USD."

	close := Analyse("Price?", gt, "SOL is $101.05 USD.", cfg)
	if close.Fired() {
		t.Errorf("0.02%% drift should be tolerated, got %+v", close)
	}
	wrong := Analyse("Price?", gt, "SOL is $94,060 USD.", cfg)
	if wrong.Numeric >= 0 {
		t.Errorf("a three-order-of-magnitude error must be caught, got %+v", wrong)
	}
}

// The anti-gaming half, from a live FINANCIAL_DATA row that scored 1.0000: the
// ground truth was itself a refusal and the miner echoed one back.
func TestRefusalGroundTruthReturnsBaselineUntouched(t *testing.T) {
	cfg := DefaultConfig()
	gt := "I don't have the exact All-in Sustaining Costs (AISC) for Freeport-McMoRan (FCX) as of August 2026, nor do I have industry benchmarks."
	ma := `{"signal":"I don't have verified data on Freeport-McMoRan's All-in Sustaining Costs as of August 2026."}`
	c := Analyse("What is FCX's AISC?", gt, ma, cfg)
	if !c.GTRefused {
		t.Fatal("expected the ground truth to be recognised as a refusal")
	}
	if c.Fired() {
		t.Errorf("no correction may be applied against a refusal ground truth, got %+v", c)
	}
}

func TestRefusalAgainstSubstantiveGroundTruthIsPenalised(t *testing.T) {
	cfg := DefaultConfig()
	gt := "Paris is the capital of France and has a population of about 2.1 million."
	ma := "I'm sorry, I cannot provide that information."
	c := Analyse("What is the capital of France?", gt, ma, cfg)
	if c.Refusal >= 0 {
		t.Errorf("expected a refusal penalty, got %+v", c)
	}
}

// The clamp is the contract with delta_c: no input may push the correction past
// it, however many detectors fire at once.
func TestTotalCorrectionNeverExceedsTheClamp(t *testing.T) {
	cfg := DefaultConfig()
	gt := "Transaction 0xabc123def456789012345678901234567890abcd moved 1,234,567.89 USDC on 2026-08-26, CVE-2021-44228 referenced."
	ma := "I cannot provide that. Perhaps 42 or 0xffffffffffffffffffffffffffffffffffffffff on 1999-01-01."
	c := Analyse("Details?", gt, ma, cfg)
	if c.Total < -cfg.MaxCorrection {
		t.Errorf("total %.6f breached the clamp -%.4f", c.Total, cfg.MaxCorrection)
	}
	if c.Total >= 0 {
		t.Errorf("expected a penalty on a wholly wrong answer, got %+v", c)
	}
	if got := Apply(0.80, c); got > 0.80 {
		t.Errorf("Apply raised the score: %.4f", got)
	}
}

// Empty answers already score 0 from the baseline's own early return.
func TestEmptyAnswerIsNotCorrected(t *testing.T) {
	cfg := DefaultConfig()
	for _, ma := range []string{"", "   ", "\n\t"} {
		c := Analyse("q", "The price is $94.06 and the count is 1,234.", ma, cfg)
		if c.Fired() {
			t.Errorf("empty answer %q produced a correction %+v", ma, c)
		}
	}
}

func TestApplyClampsToUnitRange(t *testing.T) {
	c := Correction{Total: -0.12}
	if got := Apply(0.05, c); got != 0 {
		t.Errorf("Apply(0.05, -0.12) = %v, want 0", got)
	}
	if got := Apply(1.0, Correction{}); got != 1.0 {
		t.Errorf("Apply(1.0, zero) = %v, want 1.0", got)
	}
}

func TestCorrectionIsNeverPositive(t *testing.T) {
	cfg := DefaultConfig()
	inputs := [][3]string{
		{"q", "gt with $1.00", "answer with $1.00"},
		{"q", "gt no numbers", "answer with $5,000"},
		{"q", "CVE-2021-44228", "CVE-2021-44228"},
	}
	for _, in := range inputs {
		c := Analyse(in[0], in[1], in[2], cfg)
		if c.Total > 0 {
			t.Errorf("correction was positive for %v: %+v", in, c)
		}
	}
}

// ── Regressions from live rows ───────────────────────────────────────────────

// STOCK_PRICE epoch 285, miner txlens. The answer is right to four decimal
// places; only the ground truth's capture timestamp is missing from it. An
// earlier coverage-based rule scored this -0.0640 and inverted the intent's
// ranking, because it counted "26, 2026, 12, 59, 59" as figures the answer had
// failed to reproduce.
func TestCorrectPriceIsNotPenalisedForOmittingTheGroundTruthTimestamp(t *testing.T) {
	cfg := DefaultConfig()
	gt := "The current share price of Microsoft (MSFT) is $496.35 as of August 26, 2026, at 12:59:59 PM Pacific Time."
	ma := `{"as_of":"2026-08-26T13:30:00.000Z","canonical":"ticker:MSFT:496.37","confidence":1,"currency":"USD","price_usd":496.37,"summary":"MSFT is $496.37"}`
	c := Analyse("Share price of MSFT?", gt, ma, cfg)
	if c.Fired() {
		t.Errorf("a correct price was penalised: %+v", c)
	}
}

// The same ground truth, answered with a genuinely wrong price, must still be
// caught — otherwise the fix above has simply disabled the detector.
func TestWrongPriceIsStillCaughtAlongsideATimestamp(t *testing.T) {
	cfg := DefaultConfig()
	gt := "The current share price of Microsoft (MSFT) is $496.35 as of August 26, 2026, at 12:59:59 PM Pacific Time."
	ma := `{"as_of":"2026-08-26T13:30:00.000Z","price_usd":312.10,"summary":"MSFT is $312.10"}`
	c := Analyse("Share price of MSFT?", gt, ma, cfg)
	if c.Numeric >= 0 {
		t.Errorf("a wrong price escaped the detector: %+v", c)
	}
}

// Omission is not contradiction. An answer that never mentions the figure is
// incomplete, and the baseline's own signals already handle incompleteness.
func TestOmittingTheFigureEntirelyIsNotPenalised(t *testing.T) {
	cfg := DefaultConfig()
	gt := "The current share price of Microsoft (MSFT) is $496.35."
	ma := "Microsoft trades on the NASDAQ under the ticker MSFT and reports quarterly."
	c := Analyse("Share price of MSFT?", gt, ma, cfg)
	if c.Numeric < 0 {
		t.Errorf("omission must not be penalised as contradiction: %+v", c)
	}
}

// A percentage must be checked against percentages, not against a market cap
// that happens to sit nearby.
func TestFiguresAreComparedWithinTheirOwnNotation(t *testing.T) {
	cfg := DefaultConfig()
	gt := "Solana is up 4.78% over 24 hours, with a market cap of $59 billion."
	ma := "SOL has risen 4.78% in the last day; market capitalisation is $59 billion."
	if c := Analyse("SOL?", gt, ma, cfg); c.Fired() {
		t.Errorf("matching typed figures were penalised: %+v", c)
	}
	wrong := "SOL has risen 19.4% in the last day; market capitalisation is $59 billion."
	if c := Analyse("SOL?", gt, wrong, cfg); c.Numeric >= 0 {
		t.Errorf("a contradicted percentage escaped: %+v", c)
	}
}

// Anti-gaming: an answer padded with thousands of numbers must not be able to
// match a headline figure it never actually asserts. STOCK_PRICE epoch 285,
// miner kriterion-pramagraph, emitted 6,972 numeric tokens.
func TestNumberFloodDoesNotManufactureAMatch(t *testing.T) {
	cfg := DefaultConfig()
	gt := "The current share price of Microsoft (MSFT) is $496.35."

	flood := "Findings: "
	for i := 0; i < 900; i++ {
		flood += itoa(i) + " "
	}
	c := Analyse("Share price of MSFT?", gt, flood, cfg)
	// 900 consecutive integers span the tolerance band around any headline below
	// 900, so a match here would be an artefact of spraying rather than a claim.
	// The detector must decline to credit it — and equally must not treat the
	// flood as a contradiction, since it asserts nothing either way.
	if c.NumericCoverage >= 0 {
		t.Errorf("a number flood was allowed to buy a match: coverage %.2f, %+v", c.NumericCoverage, c)
	}
	if c.Numeric != 0 {
		t.Errorf("a flood asserts nothing and must draw no numeric penalty: %+v", c)
	}
}

// The flood guard must not disable the detector on ordinary answers, which is
// the obvious way to "fix" the exploit by accident.
func TestFloodGuardLeavesOrdinaryAnswersChecked(t *testing.T) {
	cfg := DefaultConfig()
	gt := "The current share price of Microsoft (MSFT) is $496.35."
	ma := `{"price_usd":312.10,"volume":18234511,"open":310.4,"high":313.1,"low":309.7}`
	c := Analyse("Share price of MSFT?", gt, ma, cfg)
	if c.Numeric >= 0 {
		t.Errorf("an ordinary wrong answer must still be caught: %+v", c)
	}
}

// The detector's contract, stated as a test: it fires only when NO figure
// anywhere in the answer is close to the ground truth's headline.
//
// This is deliberately conservative. Identifying which number in a JSON blob is
// "the answer" is not something a byte scanner can do reliably, so rather than
// guess, the detector treats the presence of the right value anywhere as enough
// to withhold judgement. False positives are what destroy rank agreement, and
// rank agreement is the gate that rejected all five prior submissions.
func TestNearbyCorrectFigureWithholdsJudgement(t *testing.T) {
	cfg := DefaultConfig()
	gt := "The current share price of Microsoft (MSFT) is $496.35."
	// The headline claim is wrong, but the answer also reports an opening price
	// within half a percent of the ground truth, so it is not a clean assertion
	// of a different value.
	ma := `{"price_usd":312.10,"open":494.2,"high":499.1}`
	if c := Analyse("Share price of MSFT?", gt, ma, cfg); c.Numeric < 0 {
		t.Errorf("a nearby correct figure must withhold the penalty: %+v", c)
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}

// A date in a ground truth is the range the caller asked about, not a fact the
// answer must echo. From a live WEATHER_FORECAST round: the ground truth carried
// the requested forecast range 2026-09-01..2026-09-07 while the miner stamped its
// own as_of of 2026-08-26, and counting that as a 50% identifier miss cost the
// answer 0.03 for nothing.
func TestISODatesAreNotTreatedAsIdentifiers(t *testing.T) {
	cfg := DefaultConfig()
	c := Analyse(
		"7-day forecast for Tokyo starting 2026-09-01?",
		"Here is the 7-day forecast for Tokyo starting from 2026-09-01 through 2026-09-07.",
		`{"as_of":"2026-08-26","summary":"Tokyo: high 34C low 26C partly cloudy."}`,
		cfg,
	)
	if c.Ident != 0 {
		t.Errorf("a date mismatch must not draw an identifier penalty: %+v", c)
	}
}

// The Go reference and the shipping Rust module must agree on their defaults, or
// `tg-score simulate` stops describing the module it is meant to stand in for.
// The authoritative check is `tg-score verify` against the real binary; this
// guards the constants themselves so a stray edit is caught without a WASM host.
func TestDefaultConfigMatchesShippedConstants(t *testing.T) {
	c := DefaultConfig()
	// Compared at float32, the precision the weights are actually declared and
	// computed in on both sides. Widening to float64 first would compare
	// float64(float32(0.06)) against the decimal literal 0.06 and fail on the
	// representation rather than on the value.
	for _, want := range []struct {
		name     string
		got, exp float32
	}{
		{"MaxCorrection", c.MaxCorrection, 0.12},
		{"NumericWeight", c.NumericWeight, 0.08},
		{"IdentWeight", c.IdentWeight, 0.06},
		{"RefusalWeight", c.RefusalWeight, 0.06},
	} {
		if want.got != want.exp {
			t.Errorf("%s = %v, shipped Rust uses %v", want.name, want.got, want.exp)
		}
	}
	for _, want := range []struct {
		name     string
		got, exp float64
	}{
		{"RelTolerance", c.RelTolerance, 0.005},
		{"SmallIntCutoff", c.SmallIntCutoff, 10},
		{"MaxHeadline", float64(c.MaxHeadline), 2},
		{"SalienceFloor", float64(c.SalienceFloor), 3},
		{"FloodThreshold", float64(c.FloodThreshold), 48},
		{"HexPrefixMin", float64(c.HexPrefixMin), 24},
		{"MaxGTNumbers", float64(c.MaxGTNumbers), 0},
	} {
		if want.got != want.exp {
			t.Errorf("%s = %v, shipped Rust uses %v", want.name, want.got, want.exp)
		}
	}
	if c.CheckDates {
		t.Error("CheckDates must default false; the shipped module excludes ISO dates")
	}
	// The clamp must sit strictly inside the promotion window, or the whole
	// derivation in docs/DESIGN.md stops holding.
	if c.MaxCorrection <= 0.10 || c.MaxCorrection >= 0.15 {
		t.Errorf("clamp %.3f is outside the delta_promote..delta_c window (0.10, 0.15)", c.MaxCorrection)
	}
}
