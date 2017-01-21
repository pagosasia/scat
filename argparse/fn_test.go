package argparse_test

import (
	"errors"
	"testing"

	assert "github.com/stretchr/testify/require"

	"scat/argparse"
)

func TestArgFn(t *testing.T) {
	received := []([]interface{}){}
	reset := func() {
		received = received[:0]
	}
	someErr := errors.New("some err")

	argFn := argparse.ArgFn{
		"abc": argparse.ArgLambda{
			Run: func(iargs []interface{}) (interface{}, error) {
				received = append(received, iargs)
				return "some str", nil
			},
		},
		"abcerr": argparse.ArgLambda{
			Run: func([]interface{}) (interface{}, error) {
				return nil, someErr
			},
		},
		"xyz": argparse.ArgLambda{
			Args: argparse.Args{argparse.ArgBytes, argparse.ArgBytes},
			Run: func(iargs []interface{}) (interface{}, error) {
				return iargs, nil
			},
		},
	}

	// without args
	reset()
	str := "abc[]"
	res, n, err := argFn.Parse(str)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(received))
	assert.Equal(t, 0, len(received[0]))
	assert.Equal(t, "some str", res.(string))
	assert.Equal(t, len(str), n)

	// fn error
	str = "abcerr"
	_, _, err = argFn.Parse(str)
	assert.Equal(t, someErr, err)

	// too many args
	str = "abc[xxx]"
	_, _, err = argFn.Parse(str)
	assert.Equal(t, argparse.ErrTooManyArgs, err)

	// optional brackets
	str = "abc"
	res, n, err = argFn.Parse(str)
	assert.NoError(t, err)
	assert.Equal(t, "some str", res.(string))
	assert.Equal(t, len(str), n)

	// with args
	str = "xyz[1kib 2kib]"
	res, n, err = argFn.Parse(str)
	assert.NoError(t, err)
	vals := res.([]interface{})
	assert.Equal(t, 2, len(vals))
	assert.Equal(t, uint64(1024), vals[0].(uint64))
	assert.Equal(t, uint64(2048), vals[1].(uint64))
	assert.Equal(t, len(str), n)

	// spaces
	str = "xyz[ 1kib 2kib ]"
	res, n, err = argFn.Parse(str)
	assert.NoError(t, err)
	vals = res.([]interface{})
	assert.Equal(t, 2, len(vals))
	assert.Equal(t, len(str), n)

	// too few args
	str = "xyz[1kib]"
	_, _, err = argFn.Parse(str)
	assert.Equal(t, argparse.ErrTooFewArgs, err)

	// inexistent function
	str = "xxx[]"
	_, _, err = argFn.Parse(str)
	assert.Equal(t, `no such function: "xxx"`, err.Error())
}
