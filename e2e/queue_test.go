package e2e

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTwoPipelinesShareOneSlot(t *testing.T) {
	c := newClient(t)
	a, b := projectID(t)+"-a", projectID(t)+"-b"

	var many []result
	for n := 1; n <= 300; n++ {
		many = append(many, passed(n))
	}

	c.createProject(a)
	c.createProject(b)
	c.upload(a, many...)
	c.upload(b, failed(1), failed(2))

	c.startGeneration(a)
	c.startGeneration(b)

	var st generation
	c.getJSON("/projects/"+a+"/generation", &st)
	require.Equal(t, "running", st.State, "a's build finished before b was accepted - the queue was never exercised; give a more results")

	c.requireSucceeded(a)
	c.requireSucceeded(b)

	var sumA, sumB summary
	c.getJSON("/projects/"+a+"/reports/latest/summary.json", &sumA)
	c.getJSON("/projects/"+b+"/reports/latest/summary.json", &sumB)
	assert.Equal(t, 300, sumA.Stats.Total)
	assert.Equal(t, 300, sumA.Stats.Passed)
	assert.Equal(t, 2, sumB.Stats.Total)
	assert.Equal(t, 2, sumB.Stats.Failed)
}
