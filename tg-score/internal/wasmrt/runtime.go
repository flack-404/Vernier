// Package wasmrt hosts a Telegraph scoring module under wazero — the same
// pure-Go WASM runtime the validator node uses — and implements the memory ABI
// the module expects.
//
// The ABI is the part that silently kills submissions. Of the five scripts on
// the public registry, one was rejected with
//
//	structural validation failed: self-match (0.0000) did not beat unrelated cross-match (0.0000)
//
// Two zeros, not two close numbers: the module never returned a meaningful value
// at all, which is what a mismatched memory contract looks like from the outside.
// Getting this file right is the whole reason a candidate can be tested locally.
package wasmrt

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"runtime"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// EmbedDim is MiniLM-L6-v2's output width. The module writes exactly this many
// float32s into its static embed buffer.
const EmbedDim = 384

// breakdownDim is the width of the module's breakdown buffer:
// [relevance, correctness, lexical, length, composite].
const breakdownDim = 5

// correctionDim is the width of Vernier's correction buffer:
// [total, numeric, identifier, refusal, gt_refused, numeric_coverage].
const correctionDim = 6

// Correction is the correction layer's account of one score, read back from a
// module exporting correction_answer. Modules without that export — the
// baseline, and any other author's scorer — simply do not support it.
type Correction struct {
	Total           float32
	Numeric         float32
	Ident           float32
	Refusal         float32
	GTRefused       bool
	NumericCoverage float32
}

// Breakdown is the module's per-signal decomposition of one score.
type Breakdown struct {
	Relevance   float32
	Correctness float32
	Lexical     float32
	Length      float32
	Composite   float32
}

// Instance is a single instantiated module. It owns its own linear memory and is
// NOT safe for concurrent use — callers go through Pool.
type Instance struct {
	mod           api.Module
	mem           api.Memory
	alloc         api.Function
	dealloc       api.Function
	rank          api.Function
	breakdown     api.Function
	embed         api.Function
	correction    api.Function
	hasBreakdown  bool
	hasCorrection bool
}

// Pool holds one compiled module and a set of instances, so scoring can run
// across all cores. Compilation is shared; only linear memory is per-instance.
type Pool struct {
	rt       wazero.Runtime
	compiled wazero.CompiledModule
	free     chan *Instance
	all      []*Instance
	ctx      context.Context
	Hash     string // sha256 of the .wasm bytes, the same digest registerWasm takes
	Path     string
	size     int
}

// Open compiles the module at path and instantiates n copies.
//
// n <= 0 selects a default from the core count, capped at 8: each instance
// carries its own copy of the module's 22 MB of quantised weights in linear
// memory, so instance count trades RAM for wall-clock directly.
func Open(ctx context.Context, path string, n int) (*Pool, error) {
	wasmBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read wasm %s: %w", path, err)
	}
	sum := sha256.Sum256(wasmBytes)

	if n <= 0 {
		n = runtime.NumCPU()
		if n > 8 {
			n = 8
		}
	}

	rt := wazero.NewRuntime(ctx)
	compiled, err := rt.CompileModule(ctx, wasmBytes)
	if err != nil {
		rt.Close(ctx)
		return nil, fmt.Errorf("compile %s: %w", path, err)
	}

	p := &Pool{
		rt:       rt,
		compiled: compiled,
		free:     make(chan *Instance, n),
		ctx:      ctx,
		Hash:     hex.EncodeToString(sum[:]),
		Path:     path,
		size:     n,
	}

	for i := 0; i < n; i++ {
		inst, err := p.instantiate(ctx)
		if err != nil {
			p.Close()
			return nil, err
		}
		p.all = append(p.all, inst)
		p.free <- inst
	}
	return p, nil
}

func (p *Pool) instantiate(ctx context.Context) (*Instance, error) {
	// An empty module name lets many instances of one compiled module coexist;
	// wazero rejects a duplicate non-empty name in the same namespace.
	mod, err := p.rt.InstantiateModule(ctx, p.compiled,
		wazero.NewModuleConfig().WithName("").WithStartFunctions())
	if err != nil {
		return nil, fmt.Errorf("instantiate %s: %w", p.Path, err)
	}

	inst := &Instance{
		mod:        mod,
		mem:        mod.Memory(),
		alloc:      mod.ExportedFunction("alloc"),
		dealloc:    mod.ExportedFunction("dealloc"),
		rank:       mod.ExportedFunction("rank_answer"),
		breakdown:  mod.ExportedFunction("breakdown_answer"),
		embed:      mod.ExportedFunction("embed"),
		correction: mod.ExportedFunction("correction_answer"),
	}

	// A module missing any of these cannot be scored at all, and saying so by
	// name here is far more useful than a nil dereference twenty rows later.
	missing := []string{}
	if inst.mem == nil {
		missing = append(missing, "memory")
	}
	if inst.alloc == nil {
		missing = append(missing, "alloc")
	}
	if inst.dealloc == nil {
		missing = append(missing, "dealloc")
	}
	if inst.rank == nil {
		missing = append(missing, "rank_answer")
	}
	if len(missing) > 0 {
		mod.Close(ctx)
		return nil, fmt.Errorf("%s does not export required ABI: %v\n"+
			"a scoring module must export alloc, dealloc and rank_answer, and expose its memory",
			p.Path, missing)
	}
	inst.hasBreakdown = inst.breakdown != nil
	inst.hasCorrection = inst.correction != nil
	return inst, nil
}

// Size reports how many instances the pool holds.
func (p *Pool) Size() int { return p.size }

// HasBreakdown reports whether the module exports breakdown_answer.
// It is optional in the ABI, so diagnostics degrade rather than fail without it.
func (p *Pool) HasBreakdown() bool { return len(p.all) > 0 && p.all[0].hasBreakdown }

// HasCorrection reports whether the module exports correction_answer.
func (p *Pool) HasCorrection() bool { return len(p.all) > 0 && p.all[0].hasCorrection }

// Correction reads back the correction layer's account of one triple.
func (p *Pool) Correction(q, gt, ma string) (Correction, error) {
	in := p.acquire()
	defer p.release(in)
	if !in.hasCorrection {
		return Correction{}, fmt.Errorf("module %s does not export correction_answer", p.Path)
	}
	ctx := p.ctx

	qp, ql, err := in.writeStr(ctx, q)
	if err != nil {
		return Correction{}, err
	}
	defer in.freeStr(ctx, qp, ql)
	gp, gl, err := in.writeStr(ctx, gt)
	if err != nil {
		return Correction{}, err
	}
	defer in.freeStr(ctx, gp, gl)
	mp, ml, err := in.writeStr(ctx, ma)
	if err != nil {
		return Correction{}, err
	}
	defer in.freeStr(ctx, mp, ml)

	res, err := in.correction.Call(ctx,
		uint64(qp), uint64(ql), uint64(gp), uint64(gl), uint64(mp), uint64(ml))
	if err != nil {
		return Correction{}, fmt.Errorf("correction_answer trapped: %w", err)
	}
	off := uint32(res[0])
	if off == 0 {
		return Correction{}, fmt.Errorf("module %s was built without the corrections feature", p.Path)
	}
	raw, ok := in.mem.Read(off, correctionDim*4)
	if !ok {
		return Correction{}, fmt.Errorf("correction_answer returned out-of-bounds offset %d", off)
	}
	f := make([]float32, correctionDim)
	for i := range f {
		f[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	return Correction{
		Total: f[0], Numeric: f[1], Ident: f[2], Refusal: f[3],
		GTRefused: f[4] != 0, NumericCoverage: f[5],
	}, nil
}

// Close releases every instance and the runtime.
func (p *Pool) Close() {
	for _, inst := range p.all {
		if inst.mod != nil {
			inst.mod.Close(p.ctx)
		}
	}
	if p.rt != nil {
		p.rt.Close(p.ctx)
	}
}

// acquire takes an instance from the pool, blocking until one is free.
func (p *Pool) acquire() *Instance  { return <-p.free }
func (p *Pool) release(i *Instance) { p.free <- i }

// writeStr allocates len(s) bytes inside the module and copies s into them,
// returning the pointer and length the ABI expects.
//
// An empty string is passed as (0, 0) rather than a zero-length allocation. The
// module's own guard is `miner_answer.trim().is_empty()`, which is reached before
// the pointer is ever dereferenced, so a null pointer with zero length is safe
// and avoids asking dlmalloc for a zero-byte block.
func (in *Instance) writeStr(ctx context.Context, s string) (ptr uint32, length uint32, err error) {
	if len(s) == 0 {
		return 0, 0, nil
	}
	res, err := in.alloc.Call(ctx, uint64(len(s)))
	if err != nil {
		return 0, 0, fmt.Errorf("alloc(%d): %w", len(s), err)
	}
	ptr = uint32(res[0])
	if ptr == 0 {
		return 0, 0, fmt.Errorf("alloc(%d) returned null", len(s))
	}
	if !in.mem.Write(ptr, []byte(s)) {
		return 0, 0, fmt.Errorf("write %d bytes at %d: out of module memory bounds", len(s), ptr)
	}
	return ptr, uint32(len(s)), nil
}

// freeStr returns a block to the module's allocator.
//
// Not optional. This harness replays tens of thousands of rows through a single
// instance, and the corpus contains individual answers over 60 KB. Leaking every
// allocation would grow linear memory without bound and eventually trap.
func (in *Instance) freeStr(ctx context.Context, ptr, length uint32) {
	if ptr == 0 || length == 0 {
		return
	}
	_, _ = in.dealloc.Call(ctx, uint64(ptr), uint64(length))
}

// Score runs rank_answer over one triple.
func (p *Pool) Score(q, gt, ma string) (float32, error) {
	in := p.acquire()
	defer p.release(in)
	return in.score(p.ctx, q, gt, ma)
}

func (in *Instance) score(ctx context.Context, q, gt, ma string) (float32, error) {
	qp, ql, err := in.writeStr(ctx, q)
	if err != nil {
		return 0, err
	}
	defer in.freeStr(ctx, qp, ql)

	gp, gl, err := in.writeStr(ctx, gt)
	if err != nil {
		return 0, err
	}
	defer in.freeStr(ctx, gp, gl)

	mp, ml, err := in.writeStr(ctx, ma)
	if err != nil {
		return 0, err
	}
	defer in.freeStr(ctx, mp, ml)

	res, err := in.rank.Call(ctx,
		uint64(qp), uint64(ql),
		uint64(gp), uint64(gl),
		uint64(mp), uint64(ml))
	if err != nil {
		return 0, fmt.Errorf("rank_answer trapped: %w", err)
	}
	if len(res) != 1 {
		return 0, fmt.Errorf("rank_answer returned %d values, expected 1", len(res))
	}
	return api.DecodeF32(res[0]), nil
}

// Breakdown runs breakdown_answer over one triple and reads back the five
// signals from the static buffer whose offset the module returns.
func (p *Pool) Breakdown(q, gt, ma string) (Breakdown, error) {
	in := p.acquire()
	defer p.release(in)
	if !in.hasBreakdown {
		return Breakdown{}, fmt.Errorf("module %s does not export breakdown_answer", p.Path)
	}

	ctx := p.ctx
	qp, ql, err := in.writeStr(ctx, q)
	if err != nil {
		return Breakdown{}, err
	}
	defer in.freeStr(ctx, qp, ql)
	gp, gl, err := in.writeStr(ctx, gt)
	if err != nil {
		return Breakdown{}, err
	}
	defer in.freeStr(ctx, gp, gl)
	mp, ml, err := in.writeStr(ctx, ma)
	if err != nil {
		return Breakdown{}, err
	}
	defer in.freeStr(ctx, mp, ml)

	res, err := in.breakdown.Call(ctx,
		uint64(qp), uint64(ql), uint64(gp), uint64(gl), uint64(mp), uint64(ml))
	if err != nil {
		return Breakdown{}, fmt.Errorf("breakdown_answer trapped: %w", err)
	}
	off := uint32(res[0])
	raw, ok := in.mem.Read(off, breakdownDim*4)
	if !ok {
		return Breakdown{}, fmt.Errorf("breakdown_answer returned out-of-bounds offset %d", off)
	}
	f := make([]float32, breakdownDim)
	for i := range f {
		f[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	return Breakdown{f[0], f[1], f[2], f[3], f[4]}, nil
}

// Job is one row to score.
type Job struct {
	Index    int
	Question string
	GT       string
	MA       string
}

// Result carries a scored job, preserving Index so callers can reassemble order.
type Result struct {
	Index int
	Score float32
	Err   error
}

// ScoreAll runs every job across the pool's instances and returns results in
// input order. progress, if non-nil, is called after each completed job with a
// running count; it must be safe to call from multiple goroutines.
func (p *Pool) ScoreAll(jobs []Job, progress func(done, total int)) []Result {
	results := make([]Result, len(jobs))
	if len(jobs) == 0 {
		return results
	}

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		done int
	)
	queue := make(chan Job)

	workers := p.size
	if workers > len(jobs) {
		workers = len(jobs)
	}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in := p.acquire()
			defer p.release(in)
			for j := range queue {
				s, err := in.score(p.ctx, j.Question, j.GT, j.MA)
				results[j.Index] = Result{Index: j.Index, Score: s, Err: err}
				if progress != nil {
					mu.Lock()
					done++
					d := done
					mu.Unlock()
					progress(d, len(jobs))
				}
			}
		}()
	}
	for _, j := range jobs {
		queue <- j
	}
	close(queue)
	wg.Wait()
	return results
}
