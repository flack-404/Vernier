// Package eval orchestrates scoring a corpus through a candidate module and
// turning the result into gate verdicts.
//
// Scoring is the expensive step — each row costs three MiniLM transformer
// inferences at roughly 0.13 s each — so results are cached on disk keyed by the
// module's SHA-256. Re-running a gate against an unchanged binary is then
// instant, which is what makes tuning a correction layer against gate 3 a
// practical loop rather than an overnight job.
package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/vernier/tg-score/internal/corpus"
	"github.com/vernier/tg-score/internal/gate"
	"github.com/vernier/tg-score/internal/wasmrt"
)

// Scored holds a module's score for every corpus row it could score, keyed by
// row ID, plus an account of the rows it could not.
type Scored struct {
	WasmPath string             `json:"wasm_path"`
	WasmHash string             `json:"wasm_hash"`
	Scores   map[string]float64 `json:"scores"`

	// Errors records rows where the module trapped or its allocator failed.
	// These are not fatal: the public registry exposes an EvalErrorCount field
	// alongside each submission, so the network evidently tolerates and counts
	// them too. A module that fails on some rows is still gradeable on the rest,
	// and refusing to report anything would hide the far more useful finding —
	// which rows it cannot handle, and why.
	Errors map[string]string `json:"errors,omitempty"`
}

// ErrorCount returns the number of rows the module failed to score.
func (s *Scored) ErrorCount() int { return len(s.Errors) }

// FirstError returns one representative failure, for a report header.
func (s *Scored) FirstError() string {
	for id, e := range s.Errors {
		return id + ": " + e
	}
	return ""
}

// checkpointEvery is how many rows are scored between cache writes.
//
// Large enough that the write cost is negligible against the scoring cost, small
// enough that an interrupted hour-long run loses seconds of work rather than all
// of it.
const checkpointEvery = 500

// CacheDir returns the directory used for cached score sets.
func CacheDir(base string) string {
	if base == "" {
		base = ".tg-score-cache"
	}
	return base
}

// cachePath is keyed by the module's hash ALONE, deliberately not by the corpus.
//
// One binary's score for a given row never depends on which other rows were in
// the file, so a corpus-keyed cache would rescore the same rows every time the
// evaluation set changed — and the workflow here is exactly that: iterate on a
// small stratified subset, then confirm against the full corpus. Sharing one
// merged file per binary means the confirmation run only pays for the rows the
// subset did not already cover.
func cachePath(dir, wasmHash string) string {
	return filepath.Join(dir, fmt.Sprintf("scores-%s.json", wasmHash[:16]))
}

// ScoreCorpus runs every row through the module, reusing any cached score this
// exact binary has already produced for a row.
func ScoreCorpus(ctx context.Context, wasmPath string, rows []corpus.Row, opts Options) (*Scored, error) {
	pool, err := wasmrt.Open(ctx, wasmPath, opts.Instances)
	if err != nil {
		return nil, err
	}
	defer pool.Close()

	dir := CacheDir(opts.CacheDir)
	cp := cachePath(dir, pool.Hash)

	cached := &Scored{Scores: map[string]float64{}, Errors: map[string]string{}}
	if !opts.NoCache {
		if b, err := os.ReadFile(cp); err == nil {
			if err := json.Unmarshal(b, cached); err != nil || cached.WasmHash != pool.Hash {
				cached = &Scored{Scores: map[string]float64{}, Errors: map[string]string{}}
			}
		}
	}
	if cached.Scores == nil {
		cached.Scores = map[string]float64{}
	}
	if cached.Errors == nil {
		cached.Errors = map[string]string{}
	}

	// Only score rows the cache is missing.
	var jobs []wasmrt.Job
	var jobRows []corpus.Row
	for _, r := range rows {
		if r.ID != "" {
			if _, ok := cached.Scores[r.ID]; ok {
				continue
			}
			if _, ok := cached.Errors[r.ID]; ok {
				continue
			}
		}
		jobs = append(jobs, wasmrt.Job{Index: len(jobs), Question: r.Question, GT: r.GT, MA: r.MA})
		jobRows = append(jobRows, r)
	}

	cached.WasmHash = pool.Hash
	cached.WasmPath = wasmPath

	if len(jobs) > 0 {
		if opts.Progress != nil {
			opts.Progress(0, len(jobs))
		}
		// Scored in batches so the cache is checkpointed as the run proceeds.
		// A full-corpus pass is roughly three MiniLM inferences per row and takes
		// hours; writing only at the end means an interrupted run — or a machine
		// that sleeps — throws all of it away.
		done := 0
		for start := 0; start < len(jobs); start += checkpointEvery {
			end := start + checkpointEvery
			if end > len(jobs) {
				end = len(jobs)
			}
			batch := make([]wasmrt.Job, end-start)
			copy(batch, jobs[start:end])
			for i := range batch {
				batch[i].Index = i
			}

			results := pool.ScoreAll(batch, func(d, total int) {
				if opts.Progress != nil {
					opts.Progress(done+d, len(jobs))
				}
			})
			for i, res := range results {
				row := jobRows[start+i]
				if res.Err != nil {
					cached.Errors[row.ID] = res.Err.Error()
					continue
				}
				cached.Scores[row.ID] = float64(res.Score)
			}
			done = end
			if !opts.NoCache {
				writeCache(dir, cp, cached)
			}
		}
	}

	if !opts.NoCache {
		writeCache(dir, cp, cached)
	}
	return cached, nil
}

// writeCache merges the in-memory score set over whatever is already on disk and
// writes the result atomically.
//
// Re-reading before writing matters because two evaluations of the same binary
// against different corpora can overlap — scoring a subset while a full-corpus
// run is in flight is the normal workflow here, and a blind overwrite would
// discard whichever finished first.
func writeCache(dir, path string, sc *Scored) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	merged := map[string]float64{}
	mergedErrs := map[string]string{}
	if b, err := os.ReadFile(path); err == nil {
		var prev Scored
		if json.Unmarshal(b, &prev) == nil && prev.WasmHash == sc.WasmHash {
			for k, v := range prev.Scores {
				merged[k] = v
			}
			for k, v := range prev.Errors {
				mergedErrs[k] = v
			}
		}
	}
	for k, v := range sc.Scores {
		merged[k] = v
	}
	for k, v := range sc.Errors {
		mergedErrs[k] = v
	}
	out := Scored{WasmPath: sc.WasmPath, WasmHash: sc.WasmHash, Scores: merged, Errors: mergedErrs}
	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, path)
	}
}

// Options configures an evaluation run.
type Options struct {
	Instances  int
	CacheDir   string
	NoCache    bool
	Progress   func(done, total int)
	MinGroup   int
	PoolEpochs bool
}

// StructuralPairs builds gate 1's self-match / cross-match comparisons from the
// corpus.
//
// For each intent it takes one representative (question, ground_truth) and asks
// two things of the module: score the ground truth as though a miner had
// returned it verbatim (self-match, which should be near the module's ceiling),
// and score an unrelated intent's ground truth against the same question
// (cross-match, which should be near its floor). Pairing intent i with intent
// i+1 in sorted order keeps the choice deterministic, so the gate gives the same
// answer on every run.
func StructuralPairs(ctx context.Context, wasmPath string, rows []corpus.Row, instances int) ([]gate.Pair, error) {
	type rep struct {
		intent string
		q, gt  string
	}
	seen := map[string]bool{}
	var reps []rep
	for _, r := range rows {
		// A representative needs real text on both sides; an empty ground truth
		// would make self-match trivially zero and misreport the gate.
		if seen[r.Intent] || len(r.GT) < 32 || len(r.Question) < 8 {
			continue
		}
		seen[r.Intent] = true
		reps = append(reps, rep{r.Intent, r.Question, r.GT})
	}
	sort.Slice(reps, func(i, j int) bool { return reps[i].intent < reps[j].intent })
	if len(reps) < 2 {
		return nil, fmt.Errorf("need at least 2 intents with usable ground truth for structural validation, got %d", len(reps))
	}

	pool, err := wasmrt.Open(ctx, wasmPath, instances)
	if err != nil {
		return nil, err
	}
	defer pool.Close()

	var jobs []wasmrt.Job
	for i, r := range reps {
		other := reps[(i+1)%len(reps)]
		jobs = append(jobs,
			wasmrt.Job{Index: len(jobs), Question: r.q, GT: r.gt, MA: r.gt},         // self
			wasmrt.Job{Index: len(jobs) + 1, Question: r.q, GT: r.gt, MA: other.gt}, // cross
		)
	}
	results := pool.ScoreAll(jobs, nil)

	var pairs []gate.Pair
	for i, r := range reps {
		self, cross := results[i*2], results[i*2+1]
		// A module that traps here scores 0, which is precisely how the network
		// reports this failure: registration #8 was rejected with "self-match
		// (0.0000) did not beat unrelated cross-match (0.0000)". Surfacing the
		// trap as an error instead would hide the fact that the gate's own
		// message is reproducible.
		sv, cv := 0.0, 0.0
		if self.Err == nil {
			sv = float64(self.Score)
		}
		if cross.Err == nil {
			cv = float64(cross.Score)
		}
		pairs = append(pairs, gate.Pair{
			Intent:      r.intent,
			CrossIntent: reps[(i+1)%len(reps)].intent,
			Self:        sv,
			Cross:       cv,
		})
	}
	return pairs, nil
}

// Reference builds the reference ranking from the corpus's published scores —
// what the deployed canonical scorer actually produced on the network.
func Reference(rows []corpus.Row) map[string]float64 {
	out := make(map[string]float64, len(rows))
	for _, r := range rows {
		out[r.ID] = r.Score
	}
	return out
}

// Run scores a candidate and evaluates all three gates against the corpus.
func Run(ctx context.Context, wasmPath string, rows []corpus.Row, opts Options) (*Report, error) {
	scored, err := ScoreCorpus(ctx, wasmPath, rows, opts)
	if err != nil {
		return nil, err
	}
	pairs, err := StructuralPairs(ctx, wasmPath, rows, opts.Instances)
	if err != nil {
		return nil, err
	}

	// Gate 2 is measured over the scores in corpus order so the reported min/max
	// correspond to real rows.
	flat := make([]float64, 0, len(rows))
	for _, r := range rows {
		if s, ok := scored.Scores[r.ID]; ok {
			flat = append(flat, s)
		}
	}

	minGroup := opts.MinGroup
	if minGroup <= 0 {
		minGroup = 4
	}
	var groups []corpus.Group
	if opts.PoolEpochs {
		groups = corpus.GroupByIntent(rows, minGroup)
	} else {
		groups = corpus.GroupByIntentEpoch(rows, minGroup)
	}

	ref := Reference(rows)
	rep := &Report{
		WasmPath: wasmPath,
		WasmHash: scored.WasmHash,
		Corpus:   corpus.Summarise(rows),
		Scored:   scored,
		Errors:   scored.ErrorCount(),
		FirstErr: scored.FirstError(),
		Verdict: gate.Verdict{
			Gate1: gate.Gate1(pairs),
			Gate2: gate.Gate2(flat),
			Gate3: gate.Gate3(groups, scored.Scores, ref),
		},
	}
	return rep, nil
}

// Report is a full gate evaluation of one candidate module.
type Report struct {
	WasmPath string
	WasmHash string
	Corpus   corpus.Stats
	Scored   *Scored
	Errors   int
	FirstErr string
	Verdict  gate.Verdict
}
