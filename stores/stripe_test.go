package stores_test

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"testing"

	assert "github.com/stretchr/testify/require"
	"gitlab.com/Roman2K/scat"
	"gitlab.com/Roman2K/scat/checksum"
	"gitlab.com/Roman2K/scat/procs"
	"gitlab.com/Roman2K/scat/stores"
	"gitlab.com/Roman2K/scat/stores/quota"
	"gitlab.com/Roman2K/scat/stripe"
	"gitlab.com/Roman2K/scat/testutil"
)

type testStriper struct {
	calls []striperCall
	s     stripe.S
	err   error
}

type striperCall struct {
	s     stripe.S
	dests stripe.Locs
	seq   stripe.Seq
}

func (ts *testStriper) Stripe(s stripe.S, dests stripe.Locs, seq stripe.Seq) (
	stripe.S, error,
) {
	ts.calls = append(ts.calls, striperCall{s, dests, seq})
	return ts.s, ts.err
}

func TestStripe(t *testing.T) {
	var tester *stripeTester
	setTester := func(striper stripe.Striper) {
		tester = newStripeTester(func(qman *quota.Man) procs.DynProcer {
			sp, err := stores.NewStripe(striper, qman)
			assert.NoError(t, err)
			return sp
		})
	}

	chunk1 := chunkWithHash(checksum.SumBytes([]byte("hash1")))
	chunk2 := chunkWithHash(checksum.SumBytes([]byte("hash2")))

	// unknown copier ID
	setTester(&testStriper{s: stripe.S{
		chunk1: stripe.NewLocs("b", "c", "d"),
	}})
	tester.setCopier("a", chunk1)
	tester.setCopier("b")
	tester.setCopier("c")
	tester.reset()
	var panicMsg interface{}
	func() {
		defer func() {
			panicMsg = recover()
		}()
		tester.sp.Procs(chunk1)
	}()
	assert.Equal(t, "unknown copier ID", panicMsg)

	// ok
	striper := &testStriper{s: stripe.S{
		chunk1: stripe.NewLocs("b", "c"),
	}}
	setTester(striper)
	tester.setCopier("a", chunk1)
	tester.setCopier("b")
	tester.setCopier("c")
	tester.reset()
	tester.test(t, chunk1, []string{"b", "c"})
	assert.Equal(t, 1, len(striper.calls))
	assert.Equal(t, stripe.S{
		chunk1: stripe.NewLocs("a"),
	}, striper.calls[0].s)

	// copies mutex has been unlocked
	tester.resetCalled()
	tester.test(t, chunk1, []string{"b", "c"})

	// copier error
	tester.resetCalled()
	someErr := errors.New("some err")
	tester.errs["b"] = someErr
	err := tester.testE(t, chunk1, []string{"b", "c"})
	tester.resetErrs()
	assert.Equal(t, someErr, err)

	// group
	striper = &testStriper{s: stripe.S{
		chunk1: stripe.NewLocs("a"),
		chunk2: stripe.NewLocs("b"),
	}}
	setTester(striper)
	tester.setCopier("a")
	tester.setCopier("b")
	tester.reset()
	chunk := scat.NewChunk(0, nil)
	chunk.Meta().Set("group", []*scat.Chunk{
		chunk1,
		chunk2,
	})
	tester.testM(t, chunk, callM{
		chunk1.Hash(): []string{"a"},
		chunk2.Hash(): []string{"b"},
	})
	assert.Equal(t, 1, len(striper.calls))
	assert.Equal(t, stripe.S{
		chunk1: stripe.NewLocs(),
		chunk2: stripe.NewLocs(),
	}, striper.calls[0].s)

	// seen
	setTester(&testStriper{s: stripe.S{
		chunk2: stripe.NewLocs("a"),
	}})
	tester.setCopier("a")
	tester.reset()
	panicMsg = nil
	func() {
		defer func() {
			panicMsg = recover()
		}()
		tester.sp.Procs(chunk1)
	}()
	assert.Equal(t, "unknown chunk", panicMsg)

	// Stripe() error
	someErr = errors.New("some err")
	setTester(&testStriper{err: someErr})
	_, err = tester.sp.Procs(chunk1)
	assert.Equal(t, someErr, err)
}

func TestStripeDataUse(t *testing.T) {
	cp1 := stores.Copier{"a", stores.SliceLister{}, procs.Nop}
	cp2 := stores.Copier{"b", stores.SliceLister{}, procs.Nop}
	hash1 := checksum.SumBytes([]byte("hash1"))
	hash2 := checksum.SumBytes([]byte("hash2"))

	qman := quota.NewMan()
	qman.AddResQuota(cp1, 2)
	qman.AddResQuota(cp2, 4)

	bytes := func(n int) scat.Data {
		return scat.BytesData(bytes.Repeat([]byte("a"), n))
	}

	striper := &testStriper{}
	sp, err := stores.NewStripe(striper, qman)
	assert.NoError(t, err)

	// a: OK (2 of 2)
	// b: OK (2 of 4)
	chunk := scat.NewChunk(0, bytes(2))
	_, err = sp.Procs(chunk)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(striper.calls))
	assert.Equal(t, stripe.NewLocs("a", "b"), striper.calls[0].dests)

	// a: !! (3 of 2)
	// b: OK (3 of 4)
	chunk = scat.NewChunk(0, nil)
	chunk1 := scat.NewChunk(0, bytes(2))
	chunk1.SetHash(hash1)
	chunk2 := scat.NewChunk(0, bytes(1))
	chunk2.SetHash(hash2)
	chunk.Meta().Set("group", []*scat.Chunk{
		chunk1,
		chunk2,
	})
	_, err = sp.Procs(chunk)
	assert.NoError(t, err)
	assert.Equal(t, 2, len(striper.calls))
	assert.Equal(t, stripe.NewLocs("b"), striper.calls[1].dests)
}

func TestStripeFinish(t *testing.T) {
	testutil.TestFinishErrForward(t, func(proc procs.Proc) testutil.Finisher {
		qman := quota.NewMan()
		qman.AddRes(stores.Copier{1, stores.SliceLister{}, procs.Nop})
		qman.AddRes(stores.Copier{2, stores.SliceLister{}, proc})
		sp, err := stores.NewStripe(stripe.Config{0, 1}, qman)
		assert.NoError(t, err)
		return sp
	})
}

func processByAll(c *scat.Chunk, procs []procs.Proc) ([]*scat.Chunk, error) {
	all := []*scat.Chunk{}
	var err error
	for _, proc := range procs {
		chunks, e := testutil.ReadChunks(proc.Process(c))
		if e != nil {
			if err == nil {
				err = e
			}
			continue
		}
		all = append(all, chunks...)
	}
	return all, err
}

func chunkWithHash(h checksum.Hash) (c *scat.Chunk) {
	c = scat.NewChunk(0, nil)
	c.SetHash(h)
	return
}

type stripeTester struct {
	newStripe newFn
	qress     map[string]quota.Res
	called    callM
	errs      map[string]error
	sp        procs.DynProcer
}

type newFn func(*quota.Man) procs.DynProcer
type callM map[checksum.Hash][]string

func newStripeTester(new newFn) *stripeTester {
	t := &stripeTester{
		newStripe: new,
		qress:     make(map[string]quota.Res),
		called:    make(callM),
		errs:      make(map[string]error),
	}
	t.reset()
	return t
}

func (t *stripeTester) reset() {
	t.resetSp()
	t.resetCalled()
	t.resetErrs()
}

func (t *stripeTester) resetSp() {
	qman := quota.NewMan()
	for _, res := range t.qress {
		qman.AddRes(res)
	}
	t.sp = t.newStripe(qman)
}

func (t *stripeTester) resetCalled() {
	for k := range t.called {
		delete(t.called, k)
	}
}

func (t *stripeTester) resetErrs() {
	for k := range t.errs {
		delete(t.errs, k)
	}
}

func (t *stripeTester) setCopier(id string, chunks ...*scat.Chunk) {
	lister := make(stores.SliceLister, len(chunks))
	for i, c := range chunks {
		lister[i] = stores.LsEntry{Hash: c.Hash()}
	}
	t.qress[id] = stores.Copier{id, lister, t.testProc(id)}
}

func (t *stripeTester) testProc(id string) procs.Proc {
	return procs.InplaceFunc(func(c *scat.Chunk) error {
		h := c.Hash()
		t.called[h] = append(t.called[h], id)
		return t.errs[id]
	})
}

func (t *stripeTester) test(tt *testing.T, c *scat.Chunk, ids []string) {
	err := t.testE(tt, c, ids)
	assert.NoError(tt, err)
}

func (t *stripeTester) testE(tt *testing.T, c *scat.Chunk, ids []string) error {
	procs, err := t.sp.Procs(c)
	assert.NoError(tt, err)
	chunks, err := processByAll(c, procs)
	assert.Equal(tt, 1, len(chunks))
	assert.Equal(tt, []*scat.Chunk{c}, chunks)
	if len(ids) == 0 {
		assert.Equal(tt, 0, len(t.called))
	} else {
		assert.Equal(tt, 1, len(t.called))
		called := t.called[c.Hash()]
		sort.Strings(ids)
		sort.Strings(called)
		assert.Equal(tt, ids, called)
	}
	return err
}

func (t *stripeTester) testM(tt *testing.T, c *scat.Chunk, calls callM) {
	err := t.testME(tt, c, calls)
	assert.NoError(tt, err)
}

func (t *stripeTester) testME(tt *testing.T, c *scat.Chunk, calls callM) error {
	procs, err := t.sp.Procs(c)
	assert.NoError(tt, err)
	chunks, err := processByAll(c, procs)
	assert.Equal(tt, len(calls), len(chunks))

	callHashes := func(m callM) (hexes []string) {
		hexes = make([]string, 0, len(m))
		for h := range m {
			hexes = append(hexes, fmt.Sprintf("%x", h))
		}
		sort.Strings(hexes)
		return
	}

	chunkHexes := make([]string, len(chunks))
	for i, c := range chunks {
		chunkHexes[i] = fmt.Sprintf("%x", c.Hash())
	}
	sort.Strings(chunkHexes)

	assert.Equal(tt, callHashes(calls), chunkHexes)
	assert.Equal(tt, callHashes(calls), callHashes(t.called))

	for h, ids := range calls {
		assert.Equal(tt, ids, t.called[h])
	}
	return err
}
