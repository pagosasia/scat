package stores

import (
	"errors"
	"sync"

	"gitlab.com/Roman2K/scat"
	"gitlab.com/Roman2K/scat/checksum"
	"gitlab.com/Roman2K/scat/concur"
	"gitlab.com/Roman2K/scat/procs"
	"gitlab.com/Roman2K/scat/stores/copies"
	"gitlab.com/Roman2K/scat/stores/quota"
	"gitlab.com/Roman2K/scat/stripe"
)

type stripeP struct {
	cfg    stripe.Striper
	qman   *quota.Man
	reg    *copies.Reg
	seq    stripe.Seq
	seqMu  sync.Mutex
	finish func() error
}

func NewStripe(cfg stripe.Striper, qman *quota.Man) (procs.DynProcer, error) {
	reg := copies.NewReg()
	ress := copiersRes(qman.Resources(0))
	ids := ress.ids()
	rrItems := make([]stripe.Item, len(ids))
	for i, id := range ids {
		rrItems[i] = id
	}
	seq := &stripe.RR{Items: rrItems}
	ml := MultiLister(ress.listers())
	err := ml.AddEntriesTo([]LsEntryAdder{
		QuotaEntryAdder{Qman: qman},
		CopiesEntryAdder{Reg: reg},
	})
	dynp := &stripeP{
		cfg:    cfg,
		qman:   qman,
		reg:    reg,
		seq:    seq,
		finish: ress.finishFuncs().FirstErr,
	}
	return dynp, err
}

func (sp *stripeP) Procs(chunk *scat.Chunk) ([]procs.Proc, error) {
	chunks := map[checksum.Hash]*scat.Chunk{}
	if group, ok := chunk.Meta().Get("group").([]*scat.Chunk); ok {
		for _, c := range group {
			chunks[c.Hash()] = c
		}
	} else {
		chunks[chunk.Hash()] = chunk
	}
	curStripe := make(stripe.S, len(chunks))
	for hash, c := range chunks {
		copies := sp.reg.List(hash)
		copies.Mu.Lock()
		owners := copies.Owners()
		locs := make(stripe.Locs, len(owners))
		for _, o := range owners {
			locs[o.Id()] = struct{}{}
		}
		curStripe[c] = locs
	}
	var dataUse uint64
	for _, c := range chunks {
		use, err := calcDataUse(c.Data())
		if err != nil {
			return nil, err
		}
		dataUse += use
	}
	all := copiersRes(sp.qman.Resources(dataUse)).copiersById()
	dests := make(stripe.Locs, len(all))
	for _, cp := range all {
		dests[cp.Id()] = struct{}{}
	}
	sp.seqMu.Lock()
	newStripe, err := sp.cfg.Stripe(curStripe, dests, sp.seq)
	sp.seqMu.Unlock()
	if err != nil {
		return nil, err
	}
	nprocs := 0
	for _, locs := range newStripe {
		nprocs += len(locs)
	}
	cpProcs := make([]procs.Proc, 1, nprocs+1)
	{
		proc := make(sliceProc, 0, len(chunks))
		for _, c := range chunks {
			proc = append(proc, c)
		}
		cpProcs[0] = proc
	}
	for item, locs := range newStripe {
		chunk := item.(*scat.Chunk)
		hash := chunk.Hash()
		if _, ok := chunks[hash]; !ok {
			panic("unknown chunk")
		}
		copies := sp.reg.List(hash)
		cProcs := make([]procs.Proc, 0, len(locs))
		wg := sync.WaitGroup{}
		wg.Add(cap(cProcs))
		go func() {
			defer copies.Mu.Unlock()
			wg.Wait()
		}()
		for id := range locs {
			copier, ok := all[id]
			if !ok {
				panic("unknown copier ID")
			}
			var proc procs.Proc = copier
			proc = chunkArgProc{proc, chunk}
			proc = procs.DiscardChunks{proc}
			proc = procs.OnEnd{proc, func(err error) {
				defer wg.Done()
				if err != nil {
					sp.qman.Delete(copier)
					return
				}
				copies.Add(copier)
				sp.qman.AddUse(copier, dataUse)
			}}
			cProcs = append(cProcs, proc)
		}
		cpProcs = append(cpProcs, cProcs...)
	}
	return cpProcs, nil
}

func calcDataUse(d scat.Data) (uint64, error) {
	sz, ok := d.(scat.Sizer)
	if !ok {
		return 0, errors.New("sized-data required for calculating data use")
	}
	return uint64(sz.Size()), nil
}

func (sp *stripeP) Finish() error {
	return sp.finish()
}

type sliceProc []*scat.Chunk

func (s sliceProc) Process(*scat.Chunk) <-chan procs.Res {
	ch := make(chan procs.Res, len(s))
	defer close(ch)
	for _, c := range s {
		ch <- procs.Res{Chunk: c}
	}
	return ch
}

func (s sliceProc) Finish() error {
	return nil
}

type chunkArgProc struct {
	procs.Proc
	chunk *scat.Chunk
}

func (p chunkArgProc) Process(*scat.Chunk) <-chan procs.Res {
	return p.Proc.Process(p.chunk)
}

type copiersRes []quota.Res

func (ress copiersRes) listers() (lsers []Lister) {
	lsers = make([]Lister, len(ress))
	for i, res := range ress {
		lsers[i] = res.(Lister)
	}
	return
}

func (ress copiersRes) ids() (ids []interface{}) {
	ids = make([]interface{}, len(ress))
	for i, res := range ress {
		ids[i] = res.Id()
	}
	return
}

func (ress copiersRes) finishFuncs() (fns concur.Funcs) {
	fns = make(concur.Funcs, len(ress))
	for i, res := range ress {
		fns[i] = res.(procs.Proc).Finish
	}
	return
}

func (ress copiersRes) copiersById() map[interface{}]Copier {
	cps := make(map[interface{}]Copier, len(ress))
	for _, res := range ress {
		cps[res.Id()] = res.(Copier)
	}
	return cps
}
