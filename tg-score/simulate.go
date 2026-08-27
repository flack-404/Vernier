package main

import (
	"context"
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/vernier/tg-score/internal/corpus"
	"github.com/vernier/tg-score/internal/detect"
	"github.com/vernier/tg-score/internal/eval"
	"github.com/vernier/tg-score/internal/gate"
)

// cmdSimulate applies the correction layer to a baseline module's cached scores
// and reports what it does to the gates.
//
// This is the tuning loop. Scoring a 1,574-row corpus through the WASM module
// costs about eight minutes, so tuning a correction by rebuilding the module for
// every parameter change would be unworkable. The correction is a pure function
// of (question, ground_truth, answer) and the baseline score, so it can be
// applied to cached baseline scores instead — reducing an eight-minute round
// trip to a few milliseconds. The shipping Rust module is then checked against
// this implementation row by row (see `tg-score verify`).
func cmdSimulate(args []string) error {
	fs := flag.NewFlagSet("simulate", flag.ExitOnError)
	in := fs.String("c", defaultCorpus, "corpus file")
	instances := fs.Int("j", 0, "parallel module instances (0 = auto)")
	minGroup := fs.Int("min-group", 4, "minimum rows for a group to be rankable")
	pool := fs.Bool("pool-epochs", false, "pool epochs into one group per intent")
	verbose := fs.Bool("v", false, "list every group")
	showFired := fs.Int("show", 12, "example rows to list where the correction fired")

	cfg := detect.DefaultConfig()
	float32Var(fs, &cfg.MaxCorrection, "max", cfg.MaxCorrection, "total correction clamp")
	float32Var(fs, &cfg.NumericWeight, "w-num", cfg.NumericWeight, "numeric detector weight")
	float32Var(fs, &cfg.IdentWeight, "w-id", cfg.IdentWeight, "identifier detector weight")
	float32Var(fs, &cfg.RefusalWeight, "w-ref", cfg.RefusalWeight, "refusal detector weight")
	fs.Float64Var(&cfg.RelTolerance, "tol", cfg.RelTolerance, "relative numeric tolerance")
	fs.IntVar(&cfg.MaxGTNumbers, "max-gt-nums", cfg.MaxGTNumbers, "ground-truth figure count above which the numeric detector declines (0 = no ceiling)")
	fs.BoolVar(&cfg.CheckDates, "check-dates", cfg.CheckDates, "include ISO dates in the identifier detector")
	brief := fs.Bool("brief", false, "one line of output, for parameter sweeps")
	fs.Parse(args)

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: tg-score simulate [flags] <baseline.wasm>")
	}
	basePath := fs.Arg(0)

	rows, err := corpus.Load(*in)
	if err != nil {
		return err
	}
	base, err := eval.ScoreCorpus(context.Background(), basePath, rows, eval.Options{
		Instances: *instances,
		Progress:  progressFn("scoring", 100),
	})
	clearProgress()
	if err != nil {
		return err
	}

	corrected := make(map[string]float64, len(rows))
	corrections := make(map[string]detect.Correction, len(rows))
	var fired, gtRefused, numFired, idFired, refFired int
	for _, r := range rows {
		b, ok := base.Scores[r.ID]
		if !ok {
			continue
		}
		c := detect.Analyse(r.Question, r.GT, r.MA, cfg)
		corrections[r.ID] = c
		corrected[r.ID] = detect.Apply(b, c)
		if c.GTRefused {
			gtRefused++
		}
		if c.Fired() {
			fired++
		}
		if c.Numeric < 0 {
			numFired++
		}
		if c.Ident < 0 {
			idFired++
		}
		if c.Refusal < 0 {
			refFired++
		}
	}

	mg := *minGroup
	var groups []corpus.Group
	if *pool {
		groups = corpus.GroupByIntent(rows, mg)
	} else {
		groups = corpus.GroupByIntentEpoch(rows, mg)
	}
	ref := eval.Reference(rows)

	baseG3 := gate.Gate3(groups, base.Scores, ref)
	corrG3 := gate.Gate3(groups, corrected, ref)

	flatBase := make([]float64, 0, len(rows))
	flatCorr := make([]float64, 0, len(rows))
	for _, r := range rows {
		if b, ok := base.Scores[r.ID]; ok {
			flatBase = append(flatBase, b)
			flatCorr = append(flatCorr, corrected[r.ID])
		}
	}
	baseG2 := gate.Gate2(flatBase)
	corrG2 := gate.Gate2(flatCorr)

	if *brief {
		printBrief(cfg, corrG3, gate.CatchRate(rows, corrected, base.Scores), fired, len(corrected))
		return nil
	}

	fmt.Printf("\n%s\n", strings.Repeat("=", 78))
	fmt.Printf("SIMULATED CORRECTION LAYER  —  baseline %s\n", pathBase(basePath))
	fmt.Printf("corpus %d rows · %d intents\n", len(rows), corpus.Summarise(rows).Intents)
	fmt.Printf("%s\n\n", strings.Repeat("=", 78))

	fmt.Printf("CONFIG\n")
	fmt.Printf("  clamp %.3f · numeric %.3f · identifier %.3f · refusal %.3f · tolerance %.4f\n",
		cfg.MaxCorrection, cfg.NumericWeight, cfg.IdentWeight, cfg.RefusalWeight, cfg.RelTolerance)
	printConfigWarnings(cfg)
	fmt.Println()

	fmt.Printf("FIRING RATE\n")
	fmt.Printf("  rows corrected            %d / %d (%.2f%%)\n", fired, len(corrected), pct(fired, len(corrected)))
	fmt.Printf("    numeric detector        %d (%.2f%%)\n", numFired, pct(numFired, len(corrected)))
	fmt.Printf("    identifier detector     %d (%.2f%%)\n", idFired, pct(idFired, len(corrected)))
	fmt.Printf("    refusal detector        %d (%.2f%%)\n", refFired, pct(refFired, len(corrected)))
	fmt.Printf("  bypassed (refusal GT)     %d (%.2f%%)\n\n", gtRefused, pct(gtRefused, len(corrected)))

	// Per-intent silence profile. This is the safety argument made visible: the
	// detectors are gated on the ground truth containing something checkable, so
	// on intents whose ground truth is pure prose they cannot fire at all, and
	// rank agreement there is unchanged exactly rather than approximately.
	fmt.Printf("SILENCE PROFILE (where the layer can and cannot fire)\n")
	type ip struct{ rows, fired, bypassed int }
	byIntent := map[string]*ip{}
	for _, r := range rows {
		c, ok := corrections[r.ID]
		if !ok {
			continue
		}
		e := byIntent[r.Intent]
		if e == nil {
			e = &ip{}
			byIntent[r.Intent] = e
		}
		e.rows++
		if c.Fired() {
			e.fired++
		}
		if c.GTRefused {
			e.bypassed++
		}
	}
	names := make([]string, 0, len(byIntent))
	for k := range byIntent {
		names = append(names, k)
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := byIntent[names[i]], byIntent[names[j]]
		ra, rb := float64(a.fired)/float64(a.rows), float64(b.fired)/float64(b.rows)
		if ra != rb {
			return ra < rb
		}
		return names[i] < names[j]
	})
	fmt.Printf("  %-28s %6s %8s %9s %9s\n", "INTENT", "ROWS", "FIRED", "RATE", "BYPASSED")
	for _, n := range names {
		e := byIntent[n]
		note := ""
		if e.fired == 0 {
			note = "  silent"
		}
		fmt.Printf("  %-28s %6d %8d %8.1f%% %9d%s\n", n, e.rows, e.fired, 100*float64(e.fired)/float64(e.rows), e.bypassed, note)
	}
	fmt.Println()

	fmt.Printf("GATE 2 — DISPERSION\n")
	fmt.Printf("  baseline stdev  %.4f   %s\n", baseG2.Stdev, baseG2.Status)
	fmt.Printf("  corrected stdev %.4f   %s\n\n", corrG2.Stdev, corrG2.Status)

	fmt.Printf("GATE 3 — RANK AGREEMENT (threshold %.2f)\n", gate.RankAgreementThreshold)
	fmt.Printf("  %-34s %10s %10s %9s\n", "GROUP", "BASELINE", "CORRECTED", "CHANGE")
	byKey := map[string]gate.IntentAgreement{}
	for _, g := range corrG3.Groups {
		byKey[groupKey(g)] = g
	}
	type cmp struct {
		key         string
		b, c, delta float64
		n           int
		status      gate.Status
	}
	var rowsOut []cmp
	for _, g := range baseG3.Groups {
		if g.Status == gate.Undefined {
			continue
		}
		cg, ok := byKey[groupKey(g)]
		if !ok || cg.Status == gate.Undefined {
			continue
		}
		rowsOut = append(rowsOut, cmp{groupKey(g), g.Rho, cg.Rho, cg.Rho - g.Rho, g.N, cg.Status})
	}
	sort.Slice(rowsOut, func(i, j int) bool { return rowsOut[i].c < rowsOut[j].c })

	shown := 0
	for _, r := range rowsOut {
		if !*verbose && shown >= 14 && r.delta == 0 {
			continue
		}
		mark := ""
		if r.status == gate.Fail {
			mark = "  FAIL"
		}
		fmt.Printf("  %-34s %10.4f %10.4f %+9.4f%s\n", r.key, r.b, r.c, r.delta, mark)
		shown++
	}
	if !*verbose && len(rowsOut) > shown {
		fmt.Printf("  ... %d more unchanged (use -v)\n", len(rowsOut)-shown)
	}

	fmt.Printf("\n  %-24s %10s %10s\n", "", "BASELINE", "CORRECTED")
	fmt.Printf("  %-24s %10.4f %10.4f\n", "mean rho", baseG3.MeanRho, corrG3.MeanRho)
	fmt.Printf("  %-24s %10.4f %10.4f\n", "min rho", baseG3.MinRho, corrG3.MinRho)
	fmt.Printf("  %-24s %10d %10d\n", "groups failing", baseG3.FailingN, corrG3.FailingN)
	fmt.Printf("  %-24s %10.4f %10.4f\n", "headroom over 0.60", baseG3.Headroom(), corrG3.Headroom())

	// Catch-Rate against the baseline as canonical — the comparison the network
	// actually makes for promotion.
	catches := gate.CatchRate(rows, corrected, base.Scores)
	fmt.Printf("\nCATCH-RATE (candidate vs baseline-as-canonical)\n")
	fmt.Printf("  eligible rows (baseline > %.2f)  %d\n", gate.CatchCanonicalFloor, catches.Eligible)
	fmt.Printf("  catches (delta >= %.2f)          %d\n", gate.DeltaPromote, len(catches.Catches))
	if len(catches.Catches) > 0 {
		fmt.Printf("  promotion condition SATISFIED on %d intent(s)\n", len(catches.ByIntent))
	} else {
		fmt.Printf("  promotion condition NOT satisfied — no row disagrees by %.2f\n", gate.DeltaPromote)
	}

	if *showFired > 0 {
		fmt.Printf("\nLARGEST CORRECTIONS\n")
		type fr struct {
			row corpus.Row
			c   detect.Correction
			b   float64
		}
		var frs []fr
		for _, r := range rows {
			c := corrections[r.ID]
			if c.Fired() {
				frs = append(frs, fr{r, c, base.Scores[r.ID]})
			}
		}
		sort.Slice(frs, func(i, j int) bool { return frs[i].c.Total < frs[j].c.Total })
		fmt.Printf("  %-24s %-20s %7s %7s %7s  %s\n", "INTENT", "MINER", "BASE", "CORR", "PUB", "DETECTORS")
		for i, f := range frs {
			if i >= *showFired {
				break
			}
			var which []string
			if f.c.Numeric < 0 {
				which = append(which, fmt.Sprintf("num(%.0f%%)", 100*f.c.NumericCoverage))
			}
			if f.c.Ident < 0 {
				which = append(which, fmt.Sprintf("id(%.0f%%)", 100*f.c.IdentCoverage))
			}
			if f.c.Refusal < 0 {
				which = append(which, "refusal")
			}
			fmt.Printf("  %-24s %-20s %7.4f %7.4f %7.4f  %s\n",
				truncate(f.row.Intent, 24), truncate(f.row.Miner, 20),
				f.b, f.b+float64(f.c.Total), f.row.Score, strings.Join(which, " "))
		}
	}

	fmt.Printf("\n%s\n", strings.Repeat("-", 78))
	if corrG3.Status == gate.Fail {
		fmt.Printf("GATE 3 WOULD FAIL — %d group(s) below %.2f, weakest %s at %.4f\n",
			corrG3.FailingN, gate.RankAgreementThreshold, corrG3.MinIntent, corrG3.MinRho)
	} else {
		fmt.Printf("GATE 3 PASSES — weakest group %s at %.4f (%.4f of headroom)\n",
			corrG3.MinIntent, corrG3.MinRho, corrG3.Headroom())
	}
	fmt.Printf("%s\n", strings.Repeat("-", 78))
	return nil
}

// printConfigWarnings flags a simulated configuration that no longer describes the
// shipping module, or that breaches a protocol bound.
//
// The Rust module's weights are compile-time constants. Tuning them here explores
// what a DIFFERENT module would do — which is the point of the command — but a
// reader glancing at the output would reasonably assume it describes vernier.wasm,
// so say plainly when it does not.
// printBrief emits one line per configuration, for sweeping parameters.
func printBrief(cfg detect.Config, g3 gate.Gate3Result, catches gate.CatchResult, fired, total int) {
	worst := "-"
	if g3.DefinedN > 0 {
		worst = fmt.Sprintf("%s %.4f", truncate(g3.MinIntent, 20), g3.MinRho)
	}
	fmt.Printf("max-gt=%-3d dates=%-5v w-num=%.3f w-id=%.3f  mean=%.4f  worst=%-28s fail=%d  fired=%4.1f%%  catch=%d\n",
		cfg.MaxGTNumbers, cfg.CheckDates, cfg.NumericWeight, cfg.IdentWeight,
		g3.MeanRho, worst, g3.FailingN, 100*float64(fired)/float64(total), len(catches.Catches))
}

func printConfigWarnings(cfg detect.Config) {
	def := detect.DefaultConfig()
	if cfg != def {
		fmt.Printf("  NOTE: this is not the shipping configuration. These results describe a\n")
		fmt.Printf("        hypothetical module; rebuild vernier/src/corrections.rs to ship them.\n")
	}
	if float64(cfg.MaxCorrection) >= gate.DeltaC {
		fmt.Printf("  WARNING: clamp %.3f >= delta_c %.2f — a validator running this script would\n",
			cfg.MaxCorrection, gate.DeltaC)
		fmt.Printf("           be penalised for consensus deviation.\n")
	}
	if float64(cfg.MaxCorrection) <= gate.DeltaPromote {
		fmt.Printf("  WARNING: clamp %.3f <= delta_promote %.2f — no correction can ever satisfy\n",
			cfg.MaxCorrection, gate.DeltaPromote)
		fmt.Printf("           the Catch-Rate condition, so the script could never be promoted.\n")
	}
}

func groupKey(g gate.IntentAgreement) string {
	if g.Epoch < 0 {
		return g.Intent
	}
	return fmt.Sprintf("%s@%d", g.Intent, g.Epoch)
}
