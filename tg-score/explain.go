package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/vernier/tg-score/internal/corpus"
	"github.com/vernier/tg-score/internal/detect"
)

// cmdExplain shows exactly what the correction layer saw on selected rows: the
// numbers and identifiers it recovered from each side, which matched, and the
// penalty that resulted.
//
// Scoring modules are otherwise opaque — a single f32 comes back with no account
// of how it was reached — and "why did my scorer disagree here" is the question
// a script author actually needs answered.
func cmdExplain(args []string) error {
	fs := flag.NewFlagSet("explain", flag.ExitOnError)
	in := fs.String("c", defaultCorpus, "corpus file")
	intent := fs.String("intent", "", "restrict to one intent")
	miner := fs.String("miner", "", "restrict to one miner")
	epoch := fs.Int("epoch", 0, "restrict to one epoch (0 = any)")
	rowID := fs.String("row", "", "explain a single row by id")
	limit := fs.Int("n", 5, "rows to explain")
	firedOnly := fs.Bool("fired", false, "only rows where the correction fired")
	text := fs.Int("text", 300, "characters of question/GT/answer to show (0 = none)")

	cfg := detect.DefaultConfig()
	float32Var(fs, &cfg.MaxCorrection, "max", cfg.MaxCorrection, "total correction clamp")
	float32Var(fs, &cfg.NumericWeight, "w-num", cfg.NumericWeight, "numeric detector weight")
	float32Var(fs, &cfg.IdentWeight, "w-id", cfg.IdentWeight, "identifier detector weight")
	float32Var(fs, &cfg.RefusalWeight, "w-ref", cfg.RefusalWeight, "refusal detector weight")
	fs.Float64Var(&cfg.RelTolerance, "tol", cfg.RelTolerance, "relative numeric tolerance")
	fs.Parse(args)

	rows, err := corpus.Load(*in)
	if err != nil {
		return err
	}

	var sel []corpus.Row
	for _, r := range rows {
		if *rowID != "" && r.ID != *rowID {
			continue
		}
		if *intent != "" && !strings.EqualFold(r.Intent, *intent) {
			continue
		}
		if *miner != "" && !strings.Contains(r.Miner, *miner) {
			continue
		}
		if *epoch != 0 && r.Epoch != *epoch {
			continue
		}
		if *firedOnly && !detect.Analyse(r.Question, r.GT, r.MA, cfg).Fired() {
			continue
		}
		sel = append(sel, r)
	}
	if len(sel) == 0 {
		return fmt.Errorf("no rows matched")
	}
	sort.Slice(sel, func(i, j int) bool {
		if sel[i].Epoch != sel[j].Epoch {
			return sel[i].Epoch > sel[j].Epoch
		}
		return sel[i].Score > sel[j].Score
	})

	for i, r := range sel {
		if i >= *limit {
			fmt.Fprintf(os.Stderr, "\n(%d more rows matched)\n", len(sel)-*limit)
			break
		}
		explainRow(r, cfg, *text)
	}
	return nil
}

func explainRow(r corpus.Row, cfg detect.Config, textN int) {
	fmt.Printf("\n%s\n", strings.Repeat("=", 78))
	fmt.Printf("%s  ·  %s  ·  epoch %d\n", r.Intent, r.Miner, r.Epoch)
	fmt.Printf("published score %.4f    row %s\n", r.Score, r.ID)
	fmt.Printf("%s\n", strings.Repeat("=", 78))

	if textN > 0 {
		fmt.Printf("\nQUESTION      %s\n", clip(r.Question, textN))
		fmt.Printf("GROUND TRUTH  %s\n", clip(r.GT, textN))
		fmt.Printf("MINER ANSWER  %s\n", clip(r.MA, textN))
	}

	gtIds := detect.ScanIdentifiers(r.GT)
	maIds := detect.ScanIdentifiers(r.MA)
	gtNums := detect.ScanNumbers(r.GT, gtIds)
	maNums := detect.ScanNumbers(r.MA, maIds)

	fmt.Printf("\nSCANNED\n")
	fmt.Printf("  ground truth  %d numbers, %d identifiers\n", len(gtNums), len(gtIds))
	fmt.Printf("  miner answer  %d numbers, %d identifiers\n", len(maNums), len(maIds))
	if len(gtNums) > 0 {
		fmt.Printf("  GT numbers    %s\n", fmtNums(gtNums, 12))
	}
	if len(maNums) > 0 {
		fmt.Printf("  MA numbers    %s\n", fmtNums(maNums, 12))
	}
	if len(gtIds) > 0 {
		fmt.Printf("  GT ids        %s\n", fmtIds(gtIds, 4))
	}
	if len(maIds) > 0 {
		fmt.Printf("  MA ids        %s\n", fmtIds(maIds, 4))
	}

	c := detect.Analyse(r.Question, r.GT, r.MA, cfg)
	fmt.Printf("\nCORRECTION\n")
	if c.GTRefused {
		fmt.Printf("  ground truth is itself a refusal — baseline returned untouched\n")
		return
	}
	fmt.Printf("  numeric     %+.4f", c.Numeric)
	if c.NumericCoverage >= 0 {
		fmt.Printf("   (%.0f%% of salient ground-truth figures reproduced)", 100*c.NumericCoverage)
	} else {
		fmt.Printf("   (no numeric ground truth — detector silent)")
	}
	fmt.Println()
	fmt.Printf("  identifier  %+.4f", c.Ident)
	if c.IdentCoverage >= 0 {
		fmt.Printf("   (%.0f%% of identifiers reproduced)", 100*c.IdentCoverage)
	} else {
		fmt.Printf("   (no identifiers in ground truth — detector silent)")
	}
	fmt.Println()
	fmt.Printf("  refusal     %+.4f\n", c.Refusal)
	fmt.Printf("  TOTAL       %+.4f  (clamp %.3f)\n", c.Total, cfg.MaxCorrection)
}

func fmtNums(ns []detect.Number, max int) string {
	var parts []string
	for i, n := range ns {
		if i >= max {
			parts = append(parts, fmt.Sprintf("... +%d more", len(ns)-max))
			break
		}
		s := trimFloat(n.Value)
		if n.Percent {
			s += "%"
		}
		if n.Currency {
			s = "$" + s
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "  ")
}

func fmtIds(ids []detect.Ident, max int) string {
	var parts []string
	for i, d := range ids {
		if i >= max {
			parts = append(parts, fmt.Sprintf("... +%d more", len(ids)-max))
			break
		}
		parts = append(parts, fmt.Sprintf("%s:%s", d.Kind, truncate(d.Text, 24)))
	}
	return strings.Join(parts, "  ")
}

func trimFloat(v float64) string {
	s := fmt.Sprintf("%.6f", v)
	s = strings.TrimRight(s, "0")
	s = strings.TrimSuffix(s, ".")
	if s == "" || s == "-" {
		s = "0"
	}
	return s
}

func clip(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
