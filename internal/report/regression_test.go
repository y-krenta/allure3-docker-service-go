package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
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
	require.NoError(t, err)

	var tree struct {
		LeavesByID map[string]treeLeaf `json:"leavesById"`
	}
	require.NoError(t, json.Unmarshal(raw, &tree))

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
		require.NoError(t, projects.CreateDir(dir, id))
	}
	g := New(dir, allure, testHistoryLimit, testBaseURL, testMaxBuilds, 0)

	writeResultWithStatus(t, dir, baseline, "steady", "passed", 1)
	writeResultWithStatus(t, dir, baseline, "breaks", "passed", 2)
	require.NoError(t, g.Generate(t.Context(), baseline))

	require.NoError(t, projects.SeedHistory(dir, mr, baseline))

	writeResultWithStatus(t, dir, mr, "steady", "passed", 3)
	writeResultWithStatus(t, dir, mr, "breaks", "failed", 4)
	require.NoError(t, g.Generate(t.Context(), mr))

	leaves := readTreeLeaves(t, dir, mr)

	require.Contains(t, leaves, "breaks")
	broken := leaves["breaks"]
	assert.Equal(t, "regressed", broken.Transition)
	assert.NotEmpty(t, broken.NodeID, "the regressed leaf carries no nodeId, so the gate cannot link to it")

	require.Contains(t, leaves, "steady")
	assert.Empty(t, leaves["steady"].Transition)
}

// Against its own previous build, a test failing on two pushes to one merge
// request is no change, and the gate would let the second through. It stays
// regressed only because each seed overwrites the merge request's history.
func TestReseedingKeepsASecondFailureRegressed(t *testing.T) {
	allure := requireAllureCLI(t)

	dir := t.TempDir()
	const baseline, mr = "baseline", "mr-1"
	for _, id := range []string{baseline, mr} {
		require.NoError(t, projects.CreateDir(dir, id))
	}
	g := New(dir, allure, testHistoryLimit, testBaseURL, testMaxBuilds, 0)

	writeResultWithStatus(t, dir, baseline, "breaks", "passed", 1)
	require.NoError(t, g.Generate(t.Context(), baseline))

	for push := 1; push <= 2; push++ {
		require.NoError(t, projects.ClearResults(dir, mr), "push %d", push)
		require.NoError(t, projects.SeedHistory(dir, mr, baseline), "push %d", push)
		writeResultWithStatus(t, dir, mr, "breaks", "failed", push)
		require.NoError(t, g.Generate(t.Context(), mr), "push %d", push)
	}

	leaves := readTreeLeaves(t, dir, mr)

	require.Contains(t, leaves, "breaks")
	assert.Equal(t, "regressed", leaves["breaks"].Transition)
}
