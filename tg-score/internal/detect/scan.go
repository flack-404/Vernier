// Package detect implements Vernier's correction layer.
//
// This Go implementation is the tuning prototype and the reference for the Rust
// module that actually ships. Every scanner here is written the way the WASM
// module must write it — byte-oriented, no regular expressions, no allocation
// beyond a result slice — so that porting is mechanical and the two can be
// checked against each other row by row.
//
// The layer exists because Telegraph's baseline scorer compares meaning, not
// content. It embeds text with MiniLM and measures cosine similarity, so
// "$94.06" and "$94,060" point in nearly the same direction. Measured on the
// live corpus, a miner reporting a wallet balance of 3.977 ETH on the wrong
// chain scored 0.9901 against a ground truth of 0 ETH on Arbitrum.
package detect

// Number is a numeric literal recovered from text, normalised to its magnitude.
type Number struct {
	Value    float64
	Percent  bool // written with a trailing %
	Currency bool // written with a leading currency symbol
	Start    int
	End      int
}

// Ident is a high-signal identifier: something that is either exactly right or
// wrong, with no meaningful notion of "close".
type Ident struct {
	Kind  IdentKind
	Text  string // normalised: lowercased for hex, uppercased for CVE
	Start int
	End   int
}

// IdentKind distinguishes the identifier classes the scanner recognises.
type IdentKind uint8

const (
	IdentHex  IdentKind = iota // 0x-prefixed: addresses, transaction hashes
	IdentCVE                   // CVE-YYYY-NNNN advisory identifiers
	IdentDate                  // ISO-8601 calendar dates
)

func (k IdentKind) String() string {
	switch k {
	case IdentHex:
		return "hex"
	case IdentCVE:
		return "cve"
	default:
		return "date"
	}
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func isHexDigit(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}
func lower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 32
	}
	return c
}
func upper(c byte) byte {
	if c >= 'a' && c <= 'z' {
		return c - 32
	}
	return c
}

// isAlpha reports ASCII letters only. Non-ASCII bytes are deliberately excluded:
// the scanner works on raw bytes so it can be reproduced exactly in no_std Rust,
// and every pattern it recognises is ASCII by definition.
func isAlpha(c byte) bool {
	c = lower(c)
	return c >= 'a' && c <= 'z'
}

// ScanIdentifiers extracts hex strings, CVE identifiers and ISO dates.
//
// These are scanned before numbers, and the spans they consume are withheld from
// the number scanner. Without that, the address
// 0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48 would decompose into a dozen
// meaningless integers and swamp the numeric signal on exactly the intents where
// identifiers matter most.
func ScanIdentifiers(s string) []Ident {
	var out []Ident
	n := len(s)
	for i := 0; i < n; {
		// 0x-prefixed hex
		if s[i] == '0' && i+1 < n && lower(s[i+1]) == 'x' {
			j := i + 2
			for j < n && isHexDigit(s[j]) {
				j++
			}
			// 6 hex digits is the shortest span worth treating as an identifier;
			// below that it is far more likely to be a colour or a short literal.
			if j-i-2 >= 6 {
				out = append(out, Ident{Kind: IdentHex, Text: lowerASCII(s[i:j]), Start: i, End: j})
				i = j
				continue
			}
		}
		// CVE-YYYY-NNNN...
		if (s[i] == 'C' || s[i] == 'c') && i+3 < n &&
			lower(s[i+1]) == 'v' && lower(s[i+2]) == 'e' && s[i+3] == '-' {
			j := i + 4
			ds := j
			for j < n && isDigit(s[j]) {
				j++
			}
			if j-ds == 4 && j < n && s[j] == '-' {
				j++
				ns := j
				for j < n && isDigit(s[j]) {
					j++
				}
				if j-ns >= 4 {
					out = append(out, Ident{Kind: IdentCVE, Text: upperASCII(s[i:j]), Start: i, End: j})
					i = j
					continue
				}
			}
		}
		// ISO date YYYY-MM-DD
		if isDigit(s[i]) && i+9 < n {
			if isDigit(s[i+1]) && isDigit(s[i+2]) && isDigit(s[i+3]) &&
				s[i+4] == '-' && isDigit(s[i+5]) && isDigit(s[i+6]) &&
				s[i+7] == '-' && isDigit(s[i+8]) && isDigit(s[i+9]) {
				// Reject a date glued to a longer digit run on either side.
				if (i == 0 || !isDigit(s[i-1])) && (i+10 >= n || !isDigit(s[i+10])) {
					out = append(out, Ident{Kind: IdentDate, Text: s[i : i+10], Start: i, End: i + 10})
					i += 10
					continue
				}
			}
		}
		i++
	}
	return out
}

func lowerASCII(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		b[i] = lower(s[i])
	}
	return string(b)
}

func upperASCII(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		b[i] = upper(s[i])
	}
	return string(b)
}

// magnitude returns the multiplier for a scale word starting at i, and the index
// just past it. Recognises k/m/b/bn/t and the spelled-out forms.
func magnitude(s string, i int) (float64, int) {
	// Skip a single space between the number and its scale word.
	j := i
	if j < len(s) && s[j] == ' ' {
		j++
	}
	if j >= len(s) || !isAlpha(s[j]) {
		return 1, i
	}
	start := j
	for j < len(s) && isAlpha(s[j]) {
		j++
	}
	word := lowerASCII(s[start:j])

	// A scale word must not be followed by more letters that would make it part
	// of a longer word ("mint" must not read as "m").
	switch word {
	case "k", "thousand":
		return 1e3, j
	case "m", "mm", "million", "mn":
		return 1e6, j
	case "b", "bn", "billion":
		return 1e9, j
	case "t", "tn", "trillion":
		return 1e12, j
	}
	return 1, i
}

// ScanNumbers extracts numeric literals, skipping any span already claimed by an
// identifier.
//
// skip must be sorted by Start; pass the result of ScanIdentifiers on the same
// string.
func ScanNumbers(s string, skip []Ident) []Number {
	var out []Number
	n := len(s)
	si := 0
	for i := 0; i < n; {
		// Advance past identifier spans.
		for si < len(skip) && skip[si].End <= i {
			si++
		}
		if si < len(skip) && i >= skip[si].Start && i < skip[si].End {
			i = skip[si].End
			continue
		}
		if !isDigit(s[i]) {
			i++
			continue
		}
		// Walk back over a sign and a currency marker.
		start := i
		neg := false
		currency := false
		k := i - 1
		if k >= 0 && s[k] == '-' {
			// Only a genuine minus, not a hyphen joining two words or digits.
			if k == 0 || !isAlpha(s[k-1]) {
				neg = true
				start = k
				k--
			}
		}
		for k >= 0 && s[k] == ' ' {
			k--
		}
		if k >= 0 && (s[k] == '$' || s[k] == '#') {
			currency = s[k] == '$'
			if currency {
				start = k
			}
		}

		// Consume the integer part, allowing comma or underscore grouping.
		var intDigits []byte
		j := i
		for j < n {
			if isDigit(s[j]) {
				intDigits = append(intDigits, s[j])
				j++
				continue
			}
			// A separator only continues the number if a digit follows it.
			if (s[j] == ',' || s[j] == '_') && j+1 < n && isDigit(s[j+1]) {
				j++
				continue
			}
			break
		}
		// Fractional part.
		var fracDigits []byte
		if j < n && s[j] == '.' && j+1 < n && isDigit(s[j+1]) {
			j++
			for j < n && isDigit(s[j]) {
				fracDigits = append(fracDigits, s[j])
				j++
			}
		}

		val := digitsToFloat(intDigits, fracDigits)

		// Scientific notation: 1.5e9 / 2E-3.
		if j < n && (s[j] == 'e' || s[j] == 'E') {
			k := j + 1
			esign := 1.0
			if k < n && (s[k] == '+' || s[k] == '-') {
				if s[k] == '-' {
					esign = -1
				}
				k++
			}
			es := k
			for k < n && isDigit(s[k]) {
				k++
			}
			if k > es && k-es <= 4 {
				exp := 0.0
				for p := es; p < k; p++ {
					exp = exp*10 + float64(s[p]-'0')
				}
				val *= pow10(int(esign * exp))
				j = k
			}
		}

		percent := false
		if j < n && s[j] == '%' {
			percent = true
			j++
		} else {
			if mul, nj := magnitude(s, j); mul != 1 {
				val *= mul
				j = nj
			}
		}
		if neg {
			val = -val
		}
		out = append(out, Number{Value: val, Percent: percent, Currency: currency, Start: start, End: j})
		i = j
	}
	return out
}

// digitsToFloat assembles a value from its digit bytes.
//
// Built up digit by digit rather than via a string parse so the Rust port needs
// no float parser in no_std, and so both implementations round identically.
func digitsToFloat(intPart, fracPart []byte) float64 {
	var v float64
	for _, c := range intPart {
		v = v*10 + float64(c-'0')
	}
	if len(fracPart) > 0 {
		var f float64
		var scale float64 = 1
		for _, c := range fracPart {
			f = f*10 + float64(c-'0')
			scale *= 10
		}
		v += f / scale
	}
	return v
}

func pow10(e int) float64 {
	v := 1.0
	if e >= 0 {
		for i := 0; i < e; i++ {
			v *= 10
		}
		return v
	}
	for i := 0; i < -e; i++ {
		v /= 10
	}
	return v
}
