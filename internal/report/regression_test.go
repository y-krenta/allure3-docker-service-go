package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/y-krenta/allure3-docker-service-go/internal/projects"
)

func writeResultWithStatus(t *testing.T, baseDir, projectID, historyID, status string, n int) {
	t.Helper()

	uuid := fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
	body := fmt.Sprintf(`{
		"uuid": %q,
		"historyId": %q,
		"fullName": "suite.%s",
		"name": %q,
		"status": %q,
		"stage": "finished",
		"start": 1700000000000,
		"stop": 1700000000250
	}`, uuid, historyID, historyID, historyID, status)

	path := filepath.Join(projects.ResultsDir(baseDir, projectID), uuid+"-result.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing result file: %v", err)
	}
}

type treeLeaf struct {
	NodeID     string `json:"nodeId"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Transition string `json:"transition"`
}

func readTreeLeaves(t *testing.T, baseDir, projectID string) map[string]treeLeaf {
	t.Helper()

	path := filepath.Join(projects.LatestReportDir(baseDir, projectID), "widgets", "tree.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading tree.json: %v", err)
	}

	var tree struct {
		LeavesByID map[string]treeLeaf `json:"leavesById"`
	}
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatalf("decoding tree.json: %v", err)
	}

	byName := make(map[string]treeLeaf, len(tree.LeavesByID))
	for _, leaf := range tree.LeavesByID {
		byName[leaf.Name] = leaf
	}
	return byName
}

// A test green on master and red in a merge request seeded from it comes out
// regressed - and only that test, so the gate is not just reading a red suite.
func TestSeededHistoryMakesAFailureRegressed(t *testing.T) {
	allure := requireAllureCLI(t)

	dir := t.TempDir()
	const baseline, mr = "baseline", "mr-1"
	for _, id := range []string{baseline, mr} {
		if err := projects.CreateDir(dir, id); err != nil {
			t.Fatalf("CreateDir(%q) = %v", id, err)
		}
	}
	g := New(dir, allure, testHistoryLimit, testBaseURL, testMaxBuilds, 0)

	writeResultWithStatus(t, dir, baseline, "steady", "passed", 1)
	writeResultWithStatus(t, dir, baseline, "breaks", "passed", 2)
	if err := g.Generate(t.Context(), baseline); err != nil {
		t.Fatalf("Generate(baseline) = %v, want nil", err)
	}

	if err := projects.SeedHistory(dir, mr, baseline); err != nil {
		t.Fatalf("SeedHistory = %v, want nil", err)
	}

	writeResultWithStatus(t, dir, mr, "steady", "passed", 3)
	writeResultWithStatus(t, dir, mr, "breaks", "failed", 4)
	if err := g.Generate(t.Context(), mr); err != nil {
		t.Fatalf("Generate(mr) = %v, want nil", err)
	}

	leaves := readTreeLeaves(t, dir, mr)

	broken, ok := leaves["breaks"]
	if !ok {
		t.Fatalf("tree.json has no leaf named %q, only %v", "breaks", leaves)
	}
	if broken.Transition != "regressed" {
		t.Errorf("transition of the failing test = %q, want %q", broken.Transition, "regressed")
	}

	if broken.NodeID == "" {
		t.Error("the regressed leaf carries no nodeId, so the gate cannot link to it")
	}

	steady, ok := leaves["steady"]
	if !ok {
		t.Fatalf("tree.json has no leaf named %q, only %v", "steady", leaves)
	}
	if steady.Transition != "" {
		t.Errorf("transition of the unchanged test = %q, want none", steady.Transition)
	}
}

// Against its own previous build, a test failing on two pushes to one merge
// request is no change, and the gate would let the second through. It stays
// regressed only because each seed overwrites the merge request's history.
func TestReseedingKeepsASecondFailureRegressed(t *testing.T) {
	allure := requireAllureCLI(t)

	dir := t.TempDir()
	const baseline, mr = "baseline", "mr-1"
	for _, id := range []string{baseline, mr} {
		if err := projects.CreateDir(dir, id); err != nil {
			t.Fatalf("CreateDir(%q) = %v", id, err)
		}
	}
	g := New(dir, allure, testHistoryLimit, testBaseURL, testMaxBuilds, 0)

	writeResultWithStatus(t, dir, baseline, "breaks", "passed", 1)
	if err := g.Generate(t.Context(), baseline); err != nil {
		t.Fatalf("Generate(baseline) = %v, want nil", err)
	}

	for push := 1; push <= 2; push++ {
		if err := projects.ClearResults(dir, mr); err != nil {
			t.Fatalf("push %d: ClearResults = %v, want nil", push, err)
		}
		if err := projects.SeedHistory(dir, mr, baseline); err != nil {
			t.Fatalf("push %d: SeedHistory = %v, want nil", push, err)
		}
		writeResultWithStatus(t, dir, mr, "breaks", "failed", push)
		if err := g.Generate(t.Context(), mr); err != nil {
			t.Fatalf("push %d: Generate = %v, want nil", push, err)
		}
	}

	leaf, ok := readTreeLeaves(t, dir, mr)["breaks"]
	if !ok {
		t.Fatal("tree.json has no leaf named \"breaks\"")
	}
	if leaf.Transition != "regressed" {
		t.Errorf("transition on the second push = %q, want %q", leaf.Transition, "regressed")
	}
}
