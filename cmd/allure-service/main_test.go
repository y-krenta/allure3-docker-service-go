package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The startup line is what an operator reads to learn how builds are capped.
// With BUILD_HEAP_MB=0 there is no cap at all - Node takes a quarter of the
// memory per build - and "0 MB" reads as the opposite: a heap of nothing.
func TestDescribeHeap(t *testing.T) {
	tests := []struct {
		mb   int
		want string
	}{
		{mb: 0, want: "left to Node"},
		{mb: 2048, want: "2048 MB"},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.want, describeHeap(tt.mb), "describeHeap(%d)", tt.mb)
	}
}
