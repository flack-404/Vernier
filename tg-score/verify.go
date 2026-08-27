package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/vernier/tg-score/internal/corpus"
	"github.com/vernier/tg-score/internal/detect"
	"github.com/vernier/tg-score/internal/eval"
	"github.com/vernier/tg-score/internal/wasmrt"
)

// cmdVerify proves three things about a candidate module, against real rows.
//
//	[A] FORK INTEGRITY     the corrections-off build still scores bit-identically
//	                       to the upstream baseline it forked, so the fork did not
//	                       disturb the thing that earns rank agreement
//	[B] CORRECTION BOUNDS  every difference between the shipping module and that
//	                       baseline is negative and within the clamp
//	[C] CROSS-CHECK        the Rust correction layer and its Go reference
//	                       implementation agree row by row
//
// [C] is what makes `tg-score simulate` trustworthy. Simulation tunes the layer
// against cached baseline scores in milliseconds instead of minutes, which is
// only legitimate if the Go implementation it uses is the same function the
// shipping Rust actually computes.
func cmdVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	in := fs.String("c", defaultCorpus, "corpus file")
	baseline := fs.String("baseline", "", "corrections-off build to compare against (required)")
	upstream := fs.String("upstream", "", "upstream telegraph-wasm-baseline binary for the fork-integrity check")
	instances := fs.Int("j", 0, "parallel module instances (0 = auto)")
	limit := fs.Int("n", 0, "check only the first N rows (0 = all)")
	show := fs.Int("show", 10, "mismatches to list")
	fs.Parse(args)

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: tg-score verify [flags] -baseline <baseline.wasm> <vernier.wasm>")
	}
	candPath := fs.Arg(0)

	rows, err := corpus.Load(*in)
	if err != nil {
		return err
	}
	if *limit > 0 && *limit < len(rows) {
		rows = rows[:*limit]
	}

	fmt.Printf("\n%s\n", strings.Repeat("=", 78))
	fmt.Printf("VERIFY  —  %s\n", pathBase(candPath))
	fmt.Printf("corpus %d rows\n", len(rows))
	fmt.Printf("%s\n", strings.Repeat("=", 78))

	failed := false

	// ── [A] Fork integrity ───────────────────────────────────────────────────
	if *upstream != "" {
		fmt.Printf("\n[A] FORK INTEGRITY  —  %s vs %s\n", pathBase(*baseline), pathBase(*upstream))
		diffs, n, err := compareModules(*baseline, *upstream, rows, *instances)
		if err != nil {
			return err
		}
		if len(diffs) == 0 {
			fmt.Printf("    PASS  %d/%d rows scored bit-identically\n", n, n)
			fmt.Printf("    The correction layer is the only change this fork makes.\n")
		} else {
			failed = true
			fmt.Printf("    FAIL  %d of %d rows differ from upstream\n", len(diffs), n)
			printDiffs(diffs, *show)
		}
	} else {
		fmt.Printf("\n[A] FORK INTEGRITY  —  skipped (pass -upstream <telegraph_scoring.wasm>)\n")
	}

	ctx := context.Background()

	// ── [B] Correction bounds ────────────────────────────────────────────────
	//
	// Skipped without -baseline. [C] needs no embedding at all — correction_answer
	// runs only the byte scanners — so leaving -baseline off makes it practical to
	// cross-check every row of the full corpus rather than a sample.
	if *baseline == "" {
		fmt.Printf("\n[B] CORRECTION BOUNDS  —  skipped (pass -baseline <baseline.wasm>)\n")
	} else if err := checkBounds(ctx, candPath, *baseline, rows, *instances, &failed); err != nil {
		return err
	}

	// ── [C] Rust vs Go ───────────────────────────────────────────────────────
	fmt.Printf("\n[C] CROSS-CHECK  —  Rust correction layer vs Go reference\n")
	pool, err := wasmrt.Open(ctx, candPath, *instances)
	if err != nil {
		return err
	}
	defer pool.Close()
	if !pool.HasCorrection() {
		fmt.Printf("    SKIP  %s does not export correction_answer\n", pathBase(candPath))
	} else {
		cfg := detect.DefaultConfig()
		type mism struct {
			row       corpus.Row
			rust, go_ detect.Correction
		}
		var mismatches []mism
		checked := 0
		tick := progressFn("checking", 500)
		for i, r := range rows {
			if tick != nil {
				tick(i, len(rows))
			}
			rc, err := pool.Correction(r.Question, r.GT, r.MA)
			if err != nil {
				return fmt.Errorf("correction_answer on row %s: %w", r.ID, err)
			}
			gc := detect.Analyse(r.Question, r.GT, r.MA, cfg)
			checked++

			// Both sides compute in f32, so this is an exact bit comparison, not
			// a tolerance. Anything less would let the two implementations drift.
			if gc.Total != rc.Total ||
				gc.Numeric != rc.Numeric ||
				gc.Ident != rc.Ident ||
				gc.Refusal != rc.Refusal ||
				gc.GTRefused != rc.GTRefused {
				mismatches = append(mismatches, mism{r, detect.Correction{
					Total: rc.Total, Numeric: rc.Numeric,
					Ident: rc.Ident, Refusal: rc.Refusal,
					GTRefused: rc.GTRefused,
				}, gc})
			}
		}
		clearProgress()
		if len(mismatches) == 0 {
			fmt.Printf("    PASS  %d/%d rows agree exactly\n", checked, checked)
			fmt.Printf("    Simulation against the Go implementation is a valid stand-in for this module.\n")
		} else {
			failed = true
			fmt.Printf("    FAIL  %d of %d rows disagree\n", len(mismatches), checked)
			sort.Slice(mismatches, func(i, j int) bool {
				return math.Abs(float64(mismatches[i].rust.Total-mismatches[i].go_.Total)) >
					math.Abs(float64(mismatches[j].rust.Total-mismatches[j].go_.Total))
			})
			for i, m := range mismatches {
				if i >= *show {
					fmt.Printf("    ... %d more\n", len(mismatches)-*show)
					break
				}
				fmt.Printf("    %s · %s · row %s\n", m.row.Intent, m.row.Miner, m.row.ID)
				// Report every field, marking the ones that actually differ, so a
				// mismatch names the detector responsible instead of leaving the
				// reader to guess from a total that may well agree.
				fmt.Printf("        %-18s %12s %12s\n", "FIELD", "RUST", "GO")
				diffField("total", float64(m.rust.Total), float64(m.go_.Total))
				diffField("numeric", float64(m.rust.Numeric), float64(m.go_.Numeric))
				diffField("identifier", float64(m.rust.Ident), float64(m.go_.Ident))
				diffField("refusal", float64(m.rust.Refusal), float64(m.go_.Refusal))
				rb, gb := 0.0, 0.0
				if m.rust.GTRefused {
					rb = 1
				}
				if m.go_.GTRefused {
					gb = 1
				}
				diffField("gt_refused", rb, gb)
			}
		}
	}

	fmt.Printf("\n%s\n", strings.Repeat("-", 78))
	if failed {
		fmt.Printf("VERIFY FAILED\n")
		fmt.Printf("%s\n", strings.Repeat("-", 78))
		os.Exit(1)
	}
	fmt.Printf("VERIFY PASSED\n")
	fmt.Printf("%s\n", strings.Repeat("-", 78))
	return nil
}

// diffField prints one field of a cross-check comparison, marking disagreement.
func diffField(name string, rust, goRef float64) {
	mark := ""
	if float32(rust) != float32(goRef) {
		mark = "   <-- differs"
	}
	fmt.Printf("        %-18s %12.6f %12.6f%s\n", name, rust, goRef, mark)
}

// checkBounds is verify's section [B]: every difference between the shipping
// module and the corrections-off build must be a lowering, and within the clamp.
func checkBounds(ctx context.Context, candPath, baseline string, rows []corpus.Row, instances int, failed *bool) error {
	fmt.Printf("\n[B] CORRECTION BOUNDS  —  %s vs %s\n", pathBase(candPath), pathBase(baseline))

	opts := eval.Options{Instances: instances, Progress: func(done, total int) {
		if done%50 == 0 || done == total {
			fmt.Fprintf(os.Stderr, "\r    scoring %d / %d", done, total)
		}
	}}
	baseScores, err := eval.ScoreCorpus(ctx, baseline, rows, opts)
	if err != nil {
		return err
	}
	candScores, err := eval.ScoreCorpus(ctx, candPath, rows, opts)
	if err != nil {
		return err
	}
	clearProgress()

	clamp := float64(detect.DefaultConfig().MaxCorrection)
	var changed, raised, overClamp int
	var maxDelta float64
	for _, r := range rows {
		b, ok1 := baseScores.Scores[r.ID]
		c, ok2 := candScores.Scores[r.ID]
		if !ok1 || !ok2 {
			continue
		}
		d := c - b
		if d == 0 {
			continue
		}
		changed++
		if d > 0 {
			raised++
		}
		if -d > clamp+1e-6 {
			overClamp++
		}
		if math.Abs(d) > maxDelta {
			maxDelta = math.Abs(d)
		}
	}
	fmt.Printf("    rows changed        %d / %d (%.2f%%)\n", changed, len(rows), pct(changed, len(rows)))
	fmt.Printf("    largest change      %.6f   (clamp %.4f)\n", maxDelta, clamp)
	if raised > 0 {
		*failed = true
		fmt.Printf("    FAIL  %d rows had their score RAISED — the layer must only ever lower\n", raised)
	} else {
		fmt.Printf("    PASS  no row had its score raised\n")
	}
	if overClamp > 0 {
		*failed = true
		fmt.Printf("    FAIL  %d rows exceeded the clamp\n", overClamp)
	} else {
		fmt.Printf("    PASS  no row exceeded the %.2f clamp\n", clamp)
	}
	return nil
}

type rowDiff struct {
	row  corpus.Row
	a, b float64
}

// compareModules scores every row through two modules and returns the rows where
// they differ, comparing raw float32 bits rather than with a tolerance — the
// claim under test is bit-identity, not approximate agreement.
func compareModules(aPath, bPath string, rows []corpus.Row, instances int) ([]rowDiff, int, error) {
	ctx := context.Background()
	opts := eval.Options{Instances: instances, Progress: func(done, total int) {
		if done%50 == 0 || done == total {
			fmt.Fprintf(os.Stderr, "\r    scoring %d / %d", done, total)
		}
	}}
	a, err := eval.ScoreCorpus(ctx, aPath, rows, opts)
	if err != nil {
		return nil, 0, err
	}
	b, err := eval.ScoreCorpus(ctx, bPath, rows, opts)
	if err != nil {
		return nil, 0, err
	}
	clearProgress()

	var diffs []rowDiff
	n := 0
	for _, r := range rows {
		av, ok1 := a.Scores[r.ID]
		bv, ok2 := b.Scores[r.ID]
		if !ok1 || !ok2 {
			continue
		}
		n++
		if math.Float32bits(float32(av)) != math.Float32bits(float32(bv)) {
			diffs = append(diffs, rowDiff{r, av, bv})
		}
	}
	return diffs, n, nil
}

func printDiffs(diffs []rowDiff, show int) {
	fmt.Printf("    %-22s %-18s %10s %10s\n", "INTENT", "MINER", "A", "B")
	for i, d := range diffs {
		if i >= show {
			fmt.Printf("    ... %d more\n", len(diffs)-show)
			break
		}
		fmt.Printf("    %-22s %-18s %10.6f %10.6f\n",
			truncate(d.row.Intent, 22), truncate(d.row.Miner, 18), d.a, d.b)
	}
}
