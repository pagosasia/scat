package stores_test

import (
	"testing"

	"github.com/Roman2K/scat"
	"github.com/Roman2K/scat/procs"
	"github.com/Roman2K/scat/stores"
	"github.com/Roman2K/scat/testutil"
	"github.com/stretchr/testify/assert"
)

func TestMemMissingData(t *testing.T) {
	const (
		data = "xxx"
	)

	mem := stores.NewMem()

	c := scat.NewChunk(0, nil)
	chunks, err := testutil.ReadChunks(mem.Unproc().Process(c))
	assert.IsType(t, procs.MissingDataError{}, err)
	assert.Equal(t, []*scat.Chunk{c}, chunks)

	c = scat.NewChunk(0, nil)
	mem.Set(c.Hash(), []byte(data))
	chunks, err = testutil.ReadChunks(mem.Unproc().Process(c))
	assert.NoError(t, err)
	assert.Equal(t, 1, len(chunks))
	b, err := chunks[0].Data().Bytes()
	assert.NoError(t, err)
	assert.Equal(t, data, string(b))
}
