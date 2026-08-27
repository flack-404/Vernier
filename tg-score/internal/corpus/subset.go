package corpus

import "sort"

// SubsetOptions controls stratified corpus selection.
type SubsetOptions struct {
	MinGroup        int  // smallest group worth ranking
	GroupsPerIntent int  // cap on groups kept per intent; 0 means keep all
	RankableOnly    bool // drop groups whose reference scores are all equal

	// SinceEpoch drops every group from an earlier epoch. 0 keeps all.
	//
	// This matters more than it looks. Agreement between the published baseline
	// and the deployed scorer is strongly time-dependent — on WEATHER_CHECK it
	// runs +0.358 for epochs up to 250, +0.528 for 251-270 and +0.883 for
	// 271-285. Pooling old epochs in drags an intent below the 0.60 gate that
	// comfortably passes on current data, and the network replays current data.
	SinceEpoch int
}

// Subset selects whole (intent, epoch) groups, stratified across intents.
//
// It selects GROUPS, never individual rows. Sampling rows out of a group would
// silently change the ranking that group represents, and rank agreement measured
// on a partial group is measuring a competition that never happened.
//
// RankableOnly drops groups where every published score is identical. Those
// groups have no reference ordering, so Spearman is undefined for them and they
// can neither pass nor fail a candidate — scoring them is pure cost. On the live
// corpus this removes about 300 rows outright and, together with the group-size
// floor, cuts roughly a quarter of the corpus that gate 3 could never have used.
//
// Groups are taken from the most recent epochs first, on the assumption that
// recent rounds better reflect the miner population a submission will be
// evaluated against.
func Subset(rows []Row, opts SubsetOptions) []Row {
	if opts.MinGroup <= 0 {
		opts.MinGroup = 4
	}
	groups := GroupByIntentEpoch(rows, opts.MinGroup)

	byIntent := map[string][]Group{}
	for _, g := range groups {
		if opts.RankableOnly && !g.rankable() {
			continue
		}
		if opts.SinceEpoch > 0 && g.Epoch < opts.SinceEpoch {
			continue
		}
		byIntent[g.Intent] = append(byIntent[g.Intent], g)
	}

	var out []Row
	intents := make([]string, 0, len(byIntent))
	for k := range byIntent {
		intents = append(intents, k)
	}
	sort.Strings(intents)

	for _, intent := range intents {
		gs := byIntent[intent]
		sort.Slice(gs, func(i, j int) bool { return gs[i].Epoch > gs[j].Epoch }) // newest first
		if opts.GroupsPerIntent > 0 && len(gs) > opts.GroupsPerIntent {
			gs = gs[:opts.GroupsPerIntent]
		}
		for _, g := range gs {
			out = append(out, g.Rows...)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Intent != out[j].Intent {
			return out[i].Intent < out[j].Intent
		}
		if out[i].Epoch != out[j].Epoch {
			return out[i].Epoch < out[j].Epoch
		}
		return out[i].Miner < out[j].Miner
	})
	return out
}

// rankable reports whether the group's published scores carry any ordering.
func (g Group) rankable() bool {
	if len(g.Rows) < 2 {
		return false
	}
	first := g.Rows[0].Score
	for _, r := range g.Rows[1:] {
		if r.Score != first {
			return true
		}
	}
	return false
}
