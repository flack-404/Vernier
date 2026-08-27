// Package corpus loads and groups the replay corpus: real
// (question, ground_truth, miner_answer, score) rows pulled from Telegraph's
// public /scores endpoint.
//
// This is the evidence base every gate is measured against. The published
// `score` field is the reference ranking — what the deployed canonical scorer
// actually produced on the network — and gate 3 measures a candidate against it.
package corpus

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// Row is one scored (miner, intent, epoch) round, as returned by GET /scores.
type Row struct {
	ID       string  `json:"id"`
	Epoch    int     `json:"epoch_id"`
	Intent   string  `json:"intent_id"`
	Miner    string  `json:"miner_slug"`
	Rank     int     `json:"rank"`
	Score    float64 `json:"score"`
	Question string  `json:"question"`
	GT       string  `json:"ground_truth"`
	MA       string  `json:"miner_answer"`
	CA       string  `json:"converted_answer"`
	Failure  string  `json:"failure_reason"`
	ScoredAt string  `json:"scored_at"`
}

// Payload is the envelope /scores returns.
type Payload struct {
	Scores []Row `json:"scores"`
}

// Load reads a corpus file written by `tg-score pull` (or any /scores dump).
func Load(path string) ([]Row, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read corpus %s: %w", path, err)
	}
	var p Payload
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("parse corpus %s: %w", path, err)
	}
	if len(p.Scores) == 0 {
		return nil, fmt.Errorf("corpus %s contains no rows", path)
	}
	return p.Scores, nil
}

// Save writes rows back out in the same envelope shape /scores uses, so a saved
// corpus is interchangeable with a raw endpoint dump.
func Save(path string, rows []Row) error {
	b, err := json.MarshalIndent(Payload{Scores: rows}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// Group is a set of rows that compete against each other and are therefore
// ranked against each other.
//
// Telegraph scores every registered miner for an intent against the same
// (question, ground_truth) pair each epoch, so an intent-epoch pair is the unit
// within which a ranking is meaningful. Comparing a FRAUD_DETECTION row against
// a WEATHER_CHECK row is not a ranking, it is a category error.
type Group struct {
	Intent string
	Epoch  int
	Rows   []Row
}

// Key returns the group's stable identifier, e.g. "FRAUD_DETECTION@284".
func (g Group) Key() string { return fmt.Sprintf("%s@%d", g.Intent, g.Epoch) }

// GroupByIntentEpoch partitions rows into (intent, epoch) groups, dropping any
// group smaller than minSize.
//
// minSize matters: Spearman over three points is close to meaningless, and a
// two-point group is either +1 or -1 with nothing in between. Passing a small
// group through the gate produces confident-looking noise.
func GroupByIntentEpoch(rows []Row, minSize int) []Group {
	byKey := map[string][]Row{}
	for _, r := range rows {
		byKey[fmt.Sprintf("%s\x00%d", r.Intent, r.Epoch)] = append(byKey[fmt.Sprintf("%s\x00%d", r.Intent, r.Epoch)], r)
	}

	var out []Group
	for _, rs := range byKey {
		if len(rs) < minSize {
			continue
		}
		out = append(out, Group{Intent: rs[0].Intent, Epoch: rs[0].Epoch, Rows: rs})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Intent != out[j].Intent {
			return out[i].Intent < out[j].Intent
		}
		return out[i].Epoch < out[j].Epoch
	})
	return out
}

// GroupByIntent pools every epoch for an intent into one group.
//
// The network's rejection strings report one agreement figure per intent, not
// per intent-epoch, so this is the grouping that most likely matches how the
// gate is actually evaluated. Pooling across epochs mixes rows scored against
// different ground truths, which is why `tg-score gate` reports both this and
// the per-epoch split rather than picking one and hiding the choice.
func GroupByIntent(rows []Row, minSize int) []Group {
	byIntent := map[string][]Row{}
	for _, r := range rows {
		byIntent[r.Intent] = append(byIntent[r.Intent], r)
	}
	var out []Group
	for intent, rs := range byIntent {
		if len(rs) < minSize {
			continue
		}
		out = append(out, Group{Intent: intent, Epoch: -1, Rows: rs})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Intent < out[j].Intent })
	return out
}

// Intents returns the sorted distinct intent names present in rows.
func Intents(rows []Row) []string {
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.Intent] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Stats summarises a corpus for the header line of a gate report.
type Stats struct {
	Rows          int
	Intents       int
	Epochs        []int
	EmptyAnswers  int
	ZeroPublished int
}

// Summarise computes corpus-level counts.
//
// EmptyAnswers and ZeroPublished are reported because they bound what the corpus
// can prove: a row whose miner answer is empty scores 0 under every scorer that
// implements the baseline's empty check, so it carries no discriminating
// information for gate 3.
func Summarise(rows []Row) Stats {
	s := Stats{Rows: len(rows)}
	epochSeen := map[int]bool{}
	intentSeen := map[string]bool{}
	for _, r := range rows {
		epochSeen[r.Epoch] = true
		intentSeen[r.Intent] = true
		if len(trimSpace(r.MA)) == 0 {
			s.EmptyAnswers++
		}
		if r.Score == 0 {
			s.ZeroPublished++
		}
	}
	s.Intents = len(intentSeen)
	for e := range epochSeen {
		s.Epochs = append(s.Epochs, e)
	}
	sort.Ints(s.Epochs)
	return s
}

// trimSpace mirrors Rust's str::trim for the whitespace this corpus actually
// contains, so the empty-answer count here agrees with the WASM module's own
// `miner_answer.trim().is_empty()` early return.
func trimSpace(s string) string {
	start := 0
	for start < len(s) && isSpace(s[start]) {
		start++
	}
	end := len(s)
	for end > start && isSpace(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}
