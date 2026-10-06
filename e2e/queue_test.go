package e2e

import "testing"

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
	if st.State != "running" {
		t.Fatalf("a's build was already %q when b was accepted - the queue was never exercised; give a more results", st.State)
	}

	c.requireSucceeded(a)
	c.requireSucceeded(b)

	var sumA, sumB summary
	c.getJSON("/projects/"+a+"/reports/latest/summary.json", &sumA)
	c.getJSON("/projects/"+b+"/reports/latest/summary.json", &sumB)
	if sumA.Stats.Total != 300 || sumA.Stats.Passed != 300 {
		t.Errorf("a's report counts %+v, want 300 passed", sumA.Stats)
	}
	if sumB.Stats.Total != 2 || sumB.Stats.Failed != 2 {
		t.Errorf("b's report counts %+v, want 2 failed", sumB.Stats)
	}
}
