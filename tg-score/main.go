// Command tg-score reproduces Telegraph's WASM activation gate on your laptop.
//
// Five scoring scripts have been submitted to Telegraph. All five were rejected,
// and every one of their authors had to spend an on-chain registration to find
// out why. There is no official way to test a scoring module before submitting
// it. This is that way.
//
//	tg-score pull                      snapshot /scores into a local corpus
//	tg-score gate ./candidate.wasm     the three activation gates, by name
//	tg-score catch ./candidate.wasm    the Catch-Rate promotion table
//	tg-score compare a.wasm b.wasm     where two scorers disagree, and by how much
//	tg-score registry                  live activation status of every submission
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vernier/tg-score/internal/corpus"
	"github.com/vernier/tg-score/internal/eval"
	"github.com/vernier/tg-score/internal/gate"
)

const defaultCorpus = "corpus.json"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "pull":
		err = cmdPull(os.Args[2:])
	case "gate":
		err = cmdGate(os.Args[2:])
	case "catch":
		err = cmdCatch(os.Args[2:])
	case "compare":
		err = cmdCompare(os.Args[2:])
	case "registry":
		err = cmdRegistry(os.Args[2:])
	case "corpus":
		err = cmdCorpus(os.Args[2:])
	case "subset":
		err = cmdSubset(os.Args[2:])
	case "simulate":
		err = cmdSimulate(os.Args[2:])
	case "explain":
		err = cmdExplain(os.Args[2:])
	case "verify":
		err = cmdVerify(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "tg-score: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "tg-score: %v\n", err)
		os.Exit(1)
	}
}

// float32Var binds a float32 config field to a command-line flag.
//
// The standard flag package has no float32 form, and the correction weights are
// float32 because the shipping module computes in f32 — see detect.Config.
func float32Var(fs *flag.FlagSet, p *float32, name string, def float32, usage string) {
	tmp := new(float64)
	*tmp = float64(def)
	fs.Func(name, usage, func(s string) error {
		v, err := strconv.ParseFloat(s, 32)
		if err != nil {
			return err
		}
		*p = float32(v)
		return nil
	})
	_ = tmp
}

func usage() {
	fmt.Fprint(os.Stderr, `tg-score — reproduce Telegraph's WASM activation gate locally

USAGE
  tg-score <command> [flags]

COMMANDS
  pull       Snapshot the public /scores endpoint into a local replay corpus
  corpus     Summarise a local corpus: rows, intents, epochs, group sizes
  subset     Select whole rankable groups into a smaller iteration corpus
  gate       Run the three activation gates against a candidate module
  catch      Catch-Rate table: rows where a candidate would trigger promotion
  simulate   Apply the correction layer to cached baseline scores and re-gate
  explain    Show what the correction layer saw on a row and why it fired
  compare    Diff two scoring modules row by row
  verify     Prove the fork is intact and the Rust layer matches its Go reference
  registry   Live activation status and rejection reasons from the network

Run "tg-score <command> -h" for the flags of a single command.
`)
}

// ── pull ─────────────────────────────────────────────────────────────────────

func cmdPull(args []string) error {
	fs := flag.NewFlagSet("pull", flag.ExitOnError)
	host := fs.String("host", corpus.DefaultHost, "Telegraph node base URL")
	out := fs.String("o", defaultCorpus, "output corpus file")
	max := fs.Int("max", 0, "stop after N rows (0 = every row the endpoint has)")
	intent := fs.String("intent", "", "restrict to a single intent")
	fs.Parse(args)

	fmt.Fprintf(os.Stderr, "pulling from %s ...\n", *host)
	rows, err := corpus.Pull(corpus.PullOptions{
		Host: *host, Max: *max, Intent: *intent,
		Progress: func(fetched, total int) {
			fmt.Fprintf(os.Stderr, "\r  %d / %d rows", fetched, total)
		},
	})
	fmt.Fprintln(os.Stderr)
	if err != nil && len(rows) == 0 {
		return err
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}
	if err := corpus.Save(*out, rows); err != nil {
		return err
	}
	printCorpusSummary(rows)
	fmt.Fprintf(os.Stderr, "\nwrote %s\n", *out)
	return nil
}

// ── corpus ───────────────────────────────────────────────────────────────────

func cmdCorpus(args []string) error {
	fs := flag.NewFlagSet("corpus", flag.ExitOnError)
	in := fs.String("c", defaultCorpus, "corpus file")
	minGroup := fs.Int("min-group", 4, "minimum rows for a group to be rankable")
	fs.Parse(args)

	rows, err := corpus.Load(*in)
	if err != nil {
		return err
	}
	printCorpusSummary(rows)

	fmt.Printf("\nRANKABLE GROUPS (>= %d rows)\n", *minGroup)
	groups := corpus.GroupByIntentEpoch(rows, *minGroup)
	fmt.Printf("  per intent-epoch: %d groups\n", len(groups))
	pooled := corpus.GroupByIntent(rows, *minGroup)
	fmt.Printf("  per intent      : %d groups\n\n", len(pooled))

	fmt.Printf("  %-28s %6s %8s %8s\n", "INTENT", "ROWS", "EPOCHS", "NONZERO")
	for _, g := range pooled {
		epochs := map[int]bool{}
		nonzero := 0
		for _, r := range g.Rows {
			epochs[r.Epoch] = true
			if r.Score > 0 {
				nonzero++
			}
		}
		fmt.Printf("  %-28s %6d %8d %8d\n", g.Intent, len(g.Rows), len(epochs), nonzero)
	}
	return nil
}

func cmdSubset(args []string) error {
	fs := flag.NewFlagSet("subset", flag.ExitOnError)
	in := fs.String("c", defaultCorpus, "input corpus file")
	out := fs.String("o", "corpus-subset.json", "output corpus file")
	minGroup := fs.Int("min-group", 4, "minimum rows for a group to be rankable")
	per := fs.Int("per-intent", 12, "groups to keep per intent (0 = all)")
	all := fs.Bool("all", false, "keep groups whose reference scores are all equal")
	since := fs.Int("since", 0, "drop groups from epochs before this (0 = all epochs)")
	fs.Parse(args)

	rows, err := corpus.Load(*in)
	if err != nil {
		return err
	}
	sub := corpus.Subset(rows, corpus.SubsetOptions{
		MinGroup:        *minGroup,
		GroupsPerIntent: *per,
		RankableOnly:    !*all,
		SinceEpoch:      *since,
	})
	if len(sub) == 0 {
		return fmt.Errorf("subset selected no rows; try -min-group lower or -all")
	}
	if err := corpus.Save(*out, sub); err != nil {
		return err
	}
	fmt.Printf("selected %d of %d rows\n", len(sub), len(rows))
	printCorpusSummary(sub)
	fmt.Fprintf(os.Stderr, "\nwrote %s\n", *out)
	return nil
}

func printCorpusSummary(rows []corpus.Row) {
	s := corpus.Summarise(rows)
	fmt.Printf("\nCORPUS\n")
	fmt.Printf("  rows              %d\n", s.Rows)
	fmt.Printf("  intents           %d\n", s.Intents)
	fmt.Printf("  epochs            %v\n", s.Epochs)
	fmt.Printf("  empty answers     %d (%.1f%%)\n", s.EmptyAnswers, pct(s.EmptyAnswers, s.Rows))
	fmt.Printf("  published score 0 %d (%.1f%%)\n", s.ZeroPublished, pct(s.ZeroPublished, s.Rows))
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return 100 * float64(a) / float64(b)
}

// ── gate ─────────────────────────────────────────────────────────────────────

func cmdGate(args []string) error {
	fs := flag.NewFlagSet("gate", flag.ExitOnError)
	in := fs.String("c", defaultCorpus, "corpus file")
	instances := fs.Int("j", 0, "parallel module instances (0 = auto)")
	minGroup := fs.Int("min-group", 4, "minimum rows for a group to be rankable")
	pool := fs.Bool("pool-epochs", false, "pool epochs into one group per intent")
	noCache := fs.Bool("no-cache", false, "ignore cached scores")
	verbose := fs.Bool("v", false, "list every group, not just failures and the weakest")
	fs.Parse(args)

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: tg-score gate [flags] <candidate.wasm>")
	}
	wasmPath := fs.Arg(0)

	rows, err := corpus.Load(*in)
	if err != nil {
		return err
	}

	rep, err := runGate(wasmPath, rows, *instances, *minGroup, *pool, *noCache)
	if err != nil {
		return err
	}
	printGateReport(rep, *verbose)

	if !rep.Verdict.Activates() {
		os.Exit(1)
	}
	return nil
}

func runGate(wasmPath string, rows []corpus.Row, instances, minGroup int, pool, noCache bool) (*eval.Report, error) {
	start := time.Now()
	opts := eval.Options{
		Instances:  instances,
		NoCache:    noCache,
		MinGroup:   minGroup,
		PoolEpochs: pool,
		Progress:   progressFn("scoring", 100),
	}
	rep, err := eval.Run(context.Background(), wasmPath, rows, opts)
	clearProgress()
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "scored in %s\n", time.Since(start).Round(time.Millisecond))
	return rep, nil
}

func pathBase(p string) string {
	if i := strings.LastIndexAny(p, "/\\"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func printGateReport(rep *eval.Report, verbose bool) {
	v := rep.Verdict
	fmt.Printf("\n%s\n", strings.Repeat("=", 74))
	fmt.Printf("TELEGRAPH ACTIVATION GATE  —  %s\n", pathBase(rep.WasmPath))
	fmt.Printf("sha256 %s\n", rep.WasmHash)
	fmt.Printf("corpus %d rows · %d intents · epochs %v\n", rep.Corpus.Rows, rep.Corpus.Intents, rep.Corpus.Epochs)
	if rep.Errors > 0 {
		// The registry exposes EvalErrorCount alongside every submission, so the
		// network counts these too rather than treating them as fatal.
		fmt.Printf("EvalErrorCount %d — rows this module could not score\n", rep.Errors)
		fmt.Printf("  e.g. %s\n", truncate(rep.FirstErr, 100))
	}
	fmt.Printf("%s\n\n", strings.Repeat("=", 74))

	// Gate 1
	g1 := v.Gate1
	fmt.Printf("[1] STRUCTURAL SELF-MATCH                                        %s\n", g1.Status)
	fmt.Printf("    self-match %.4f  must beat  cross-match %.4f      margin %+.4f\n",
		g1.MeanSelf, g1.MeanCross, g1.MeanSelf-g1.MeanCross)
	fmt.Printf("    %d/%d intent pairs individually correct (%.0f%%, need %.0f%%)\n",
		g1.PassedN, len(g1.Pairs), 100*g1.PassRate, 100*gate.MinPairPassRate)
	if g1.Status == gate.Fail {
		fmt.Printf("    -> %s\n", rep.Verdict.RejectionReason())
	}
	fmt.Println()

	// Gate 2
	g2 := v.Gate2
	fmt.Printf("[2] SCORE DISPERSION                                             %s\n", g2.Status)
	fmt.Printf("    stdev %.4f  must exceed  %.4f                     margin %+.4f\n",
		g2.Stdev, gate.StdevThreshold, g2.Stdev-gate.StdevThreshold)
	fmt.Printf("    range %.4f .. %.4f across %d rows\n", g2.Min, g2.Max, g2.N)
	if g2.Status == gate.Fail {
		fmt.Printf("    -> candidate scores collapsed: stdev=%.4f <= threshold %.4f\n", g2.Stdev, gate.StdevThreshold)
	}
	fmt.Println()

	// Gate 3
	g3 := v.Gate3
	fmt.Printf("[3] RANK AGREEMENT (per intent, vs published canonical)          %s\n", g3.Status)
	fmt.Printf("    threshold %.2f · mean rho %+.4f · %d groups measured, %d unrankable\n",
		gate.RankAgreementThreshold, g3.MeanRho, g3.DefinedN, g3.UndefinedN)
	if g3.DefinedN > 0 {
		fmt.Printf("    weakest %s at %+.4f — headroom %+.4f\n", g3.MinIntent, g3.MinRho, g3.Headroom())
	}
	if g3.ConstantCandidateN > 0 {
		fmt.Printf("    WARNING: %d group(s) scored identically for every miner — the module is not discriminating\n",
			g3.ConstantCandidateN)
	}
	fmt.Println()

	shown := 0
	fmt.Printf("    %-30s %5s %9s  %s\n", "GROUP", "N", "RHO", "")
	for _, grp := range g3.Groups {
		if !verbose && grp.Status == gate.Pass && shown >= 8 {
			continue
		}
		name := grp.Intent
		if grp.Epoch >= 0 {
			name = fmt.Sprintf("%s@%d", grp.Intent, grp.Epoch)
		}
		if grp.Status == gate.Undefined {
			fmt.Printf("    %-30s %5d %9s  %s\n", name, grp.N, "-", grp.Why)
			continue
		}
		fmt.Printf("    %-30s %5d %+9.4f  %s\n", name, grp.N, grp.Rho, grp.Status)
		shown++
	}
	if !verbose && len(g3.Groups) > shown {
		fmt.Printf("    ... %d more (use -v)\n", len(g3.Groups)-shown)
	}

	fmt.Printf("\n%s\n", strings.Repeat("-", 74))
	if v.Activates() {
		fmt.Printf("VERDICT: all three gates pass — this module would activate.\n")
	} else {
		fmt.Printf("VERDICT: rejected. The network would report:\n\n  %s\n", v.RejectionReason())
	}
	fmt.Printf("%s\n", strings.Repeat("-", 74))
}

// ── catch ────────────────────────────────────────────────────────────────────

func cmdCatch(args []string) error {
	fs := flag.NewFlagSet("catch", flag.ExitOnError)
	in := fs.String("c", defaultCorpus, "corpus file")
	canonical := fs.String("canonical", "", "canonical module to compare against (required)")
	instances := fs.Int("j", 0, "parallel module instances (0 = auto)")
	noCache := fs.Bool("no-cache", false, "ignore cached scores")
	limit := fs.Int("n", 20, "rows to list")
	fs.Parse(args)

	if fs.NArg() < 1 {
		return fmt.Errorf("usage: tg-score catch [flags] -canonical <canonical.wasm> <candidate.wasm>")
	}
	candPath := fs.Arg(0)
	if *canonical == "" {
		return fmt.Errorf("-canonical is required.\n\n" +
			"Catch-Rate compares a challenger script against the CANONICAL SCRIPT, not against\n" +
			"the published scores. The deployed scorer is not the published baseline in absolute\n" +
			"terms (mean |err| 0.383), so measuring catches against published scores reports\n" +
			"calibration mismatch rather than genuine disagreement.\n\n" +
			"Pass the baseline build as -canonical to isolate what your correction layer changes.")
	}

	rows, err := corpus.Load(*in)
	if err != nil {
		return err
	}
	opts := eval.Options{Instances: *instances, NoCache: *noCache,
		Progress: func(done, total int) {
			if done%50 == 0 || done == total {
				fmt.Fprintf(os.Stderr, "\r  scoring %d / %d", done, total)
			}
		}}

	fmt.Fprintf(os.Stderr, "scoring canonical %s\n", pathBase(*canonical))
	canonScores, err := eval.ScoreCorpus(context.Background(), *canonical, rows, opts)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\r%-40s\rscoring candidate %s\n", "", pathBase(candPath))
	candScores, err := eval.ScoreCorpus(context.Background(), candPath, rows, opts)
	if err != nil {
		return err
	}
	clearProgress()

	res := gate.CatchRate(rows, candScores.Scores, canonScores.Scores)

	fmt.Printf("\n%s\n", strings.Repeat("=", 74))
	fmt.Printf("CATCH-RATE  —  %s  vs canonical  %s\n", pathBase(candPath), pathBase(*canonical))
	fmt.Printf("%s\n\n", strings.Repeat("=", 74))
	fmt.Printf("Promotion condition (whitepaper §4.3, §12): a challenger scoring at least\n")
	fmt.Printf("delta_promote=%.2f below canonical, on an answer canonical rated above %.2f,\n", gate.DeltaPromote, gate.CatchCanonicalFloor)
	fmt.Printf("is automatically promoted to Canonical after T_promote=3 epochs.\n\n")

	fmt.Printf("  eligible rows (canonical > %.2f)   %d\n", gate.CatchCanonicalFloor, res.Eligible)
	fmt.Printf("  catches (delta >= %.2f)            %d\n", gate.DeltaPromote, len(res.Catches))
	fmt.Printf("  catch rate                         %.2f%%\n\n", 100*res.Rate())

	if len(res.Catches) == 0 {
		fmt.Printf("  No catches. This candidate never disagrees with canonical by enough to\n")
		fmt.Printf("  trigger promotion — it would activate, then sit as a permanent challenger.\n")
		return nil
	}

	fmt.Printf("  %-24s %-22s %10s %10s %8s\n", "INTENT", "MINER", "CANONICAL", "CANDIDATE", "DELTA")
	for i, c := range res.Catches {
		if i >= *limit {
			fmt.Printf("  ... %d more\n", len(res.Catches)-*limit)
			break
		}
		fmt.Printf("  %-24s %-22s %10.4f %10.4f %8.4f\n", c.Intent, truncate(c.Miner, 22), c.Canonical, c.Candidate, c.Delta)
	}

	fmt.Printf("\n  BY INTENT\n")
	keys := make([]string, 0, len(res.ByIntent))
	for k := range res.ByIntent {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return res.ByIntent[keys[i]] > res.ByIntent[keys[j]] })
	for _, k := range keys {
		fmt.Printf("    %-28s %d\n", k, res.ByIntent[k])
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// ── compare ──────────────────────────────────────────────────────────────────

func cmdCompare(args []string) error {
	fs := flag.NewFlagSet("compare", flag.ExitOnError)
	in := fs.String("c", defaultCorpus, "corpus file")
	instances := fs.Int("j", 0, "parallel module instances (0 = auto)")
	noCache := fs.Bool("no-cache", false, "ignore cached scores")
	limit := fs.Int("n", 20, "largest divergences to list")
	fs.Parse(args)

	if fs.NArg() < 2 {
		return fmt.Errorf("usage: tg-score compare [flags] <a.wasm> <b.wasm>")
	}
	aPath, bPath := fs.Arg(0), fs.Arg(1)

	rows, err := corpus.Load(*in)
	if err != nil {
		return err
	}
	opts := eval.Options{Instances: *instances, NoCache: *noCache,
		Progress: func(done, total int) {
			if done%50 == 0 || done == total {
				fmt.Fprintf(os.Stderr, "\r  scoring %d / %d", done, total)
			}
		}}

	fmt.Fprintf(os.Stderr, "scoring %s\n", pathBase(aPath))
	a, err := eval.ScoreCorpus(context.Background(), aPath, rows, opts)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\r%-40s\rscoring %s\n", "", pathBase(bPath))
	b, err := eval.ScoreCorpus(context.Background(), bPath, rows, opts)
	if err != nil {
		return err
	}
	clearProgress()

	type diff struct {
		row     corpus.Row
		a, b, d float64
	}
	var diffs []diff
	var identical, changed int
	var maxAbs float64
	for _, r := range rows {
		av, ok1 := a.Scores[r.ID]
		bv, ok2 := b.Scores[r.ID]
		if !ok1 || !ok2 {
			continue
		}
		d := bv - av
		if d == 0 {
			identical++
		} else {
			changed++
			if abs(d) > maxAbs {
				maxAbs = abs(d)
			}
		}
		diffs = append(diffs, diff{r, av, bv, d})
	}
	sort.Slice(diffs, func(i, j int) bool { return abs(diffs[i].d) > abs(diffs[j].d) })

	fmt.Printf("\n%s\n", strings.Repeat("=", 74))
	fmt.Printf("COMPARE  —  %s  ->  %s\n", pathBase(aPath), pathBase(bPath))
	fmt.Printf("%s\n\n", strings.Repeat("=", 74))
	fmt.Printf("  rows compared      %d\n", len(diffs))
	fmt.Printf("  bit-identical      %d (%.2f%%)\n", identical, pct(identical, len(diffs)))
	fmt.Printf("  changed            %d (%.2f%%)\n", changed, pct(changed, len(diffs)))
	fmt.Printf("  largest change     %.4f\n", maxAbs)
	if changed == 0 {
		fmt.Printf("\n  The two modules are behaviourally identical on this corpus.\n")
		return nil
	}

	var deltas []float64
	for _, d := range diffs {
		if d.d != 0 {
			deltas = append(deltas, abs(d.d))
		}
	}
	fmt.Printf("  mean |change|      %.4f  (over changed rows)\n\n", gate.Mean(deltas))

	fmt.Printf("  %-22s %-20s %8s %8s %8s\n", "INTENT", "MINER", "A", "B", "DELTA")
	for i, d := range diffs {
		if i >= *limit || d.d == 0 {
			break
		}
		fmt.Printf("  %-22s %-20s %8.4f %8.4f %+8.4f\n",
			truncate(d.row.Intent, 22), truncate(d.row.Miner, 20), d.a, d.b, d.d)
	}
	return nil
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// ── registry ─────────────────────────────────────────────────────────────────

func cmdRegistry(args []string) error {
	fs := flag.NewFlagSet("registry", flag.ExitOnError)
	host := fs.String("host", corpus.DefaultHost, "Telegraph node base URL")
	intent := fs.String("intent", "CHAT_COMPLETION", "intent to query (the endpoint returns all registrations regardless)")
	fs.Parse(args)

	regs, err := corpus.PullRegistry(*host, *intent, 30*time.Second)
	if err != nil {
		return err
	}
	fmt.Printf("\n%s\n", strings.Repeat("=", 74))
	fmt.Printf("TELEGRAPH SCRIPT REGISTRY  —  %d registrations network-wide\n", len(regs))
	fmt.Printf("%s\n", strings.Repeat("=", 74))

	active := 0
	for _, r := range regs {
		if r.ActivationStatus == "active" {
			active++
		}
		fmt.Printf("\n  #%d  %s  [%s]\n", r.RegistrationID, r.IntentID, strings.ToUpper(r.ActivationStatus))
		fmt.Printf("      author %s\n", r.AuthorAddress)
		fmt.Printf("      sha256 %s\n", r.WasmHash)
		fmt.Printf("      url    %s\n", r.WasmURL)
		fmt.Printf("      bond   %.0f · registered %s\n", r.BondAmount, r.RegisteredAt)
		if r.RejectionReason != "" {
			fmt.Printf("      REASON %s\n", r.RejectionReason)
		}
	}
	fmt.Printf("\n%s\n", strings.Repeat("-", 74))
	fmt.Printf("  active scripts on the network: %d\n", active)
	fmt.Printf("%s\n", strings.Repeat("-", 74))
	return nil
}
