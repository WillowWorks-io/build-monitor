package monitor

import (
	"testing"
	"time"

	"github.com/willowworks-io/build-monitor/internal/circleci"
)

func TestFromWorkflow(t *testing.T) {
	cases := []struct {
		circleci string
		want     Status
	}{
		{"success", StatusPassed},
		{"failed", StatusFailed},
		{"error", StatusFailed},
		{"failing", StatusFailed},
		{"unauthorized", StatusFailed},
		{"running", StatusRunning},
		{"on_hold", StatusOnHold},
		// Absence of a signal, not failure: a cancelled run tells you
		// nothing about the code, and colouring it red would train people
		// to ignore red.
		{"canceled", StatusUnknown},
		{"not_run", StatusUnknown},
		// Anything CircleCI adds later must not be guessed at.
		{"some_future_status", StatusUnknown},
		{"", StatusUnknown},
	}
	for _, c := range cases {
		if got := fromWorkflow(c.circleci); got != c.want {
			t.Errorf("fromWorkflow(%q) = %q, want %q", c.circleci, got, c.want)
		}
	}
}

func TestSeverityOrdering(t *testing.T) {
	// The tile shows the worst status across a run's workflows, so this
	// ordering is what decides a tile's colour. Assert the relation
	// rather than the numbers, which are arbitrary.
	ordered := []Status{StatusUnknown, StatusPassed, StatusOnHold, StatusRunning, StatusFailed}
	for i := 1; i < len(ordered); i++ {
		lo, hi := ordered[i-1], ordered[i]
		if severity(lo) >= severity(hi) {
			t.Errorf("severity(%q)=%d should rank below severity(%q)=%d",
				lo, severity(lo), hi, severity(hi))
		}
	}
}

func ts(s string) time.Time {
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return v
}

func ptr(t time.Time) *time.Time { return &t }

// pipeline builds a GitHub App pipeline with the metadata the tile reads.
func pipeline(num int, branch, defaultBranch string) circleci.Pipeline {
	p := circleci.Pipeline{
		ID:          "p1",
		Number:      num,
		ProjectSlug: "gh/acme/widgets",
		State:       "created",
		CreatedAt:   ts("2026-09-16T10:00:00Z"),
	}
	ga := &p.TriggerParameters.GitHubApp
	ga.Branch = branch
	ga.DefaultBranch = defaultBranch
	ga.CheckoutSHA = "abcdef1234567890"
	ga.CommitTitle = "Add a thing"
	ga.CommitAuthor = "ada"
	return p
}

func TestTileRollsUpWorstWorkflow(t *testing.T) {
	p := pipeline(7, "main", "main")
	got := tile("acme", "widgets", p, []circleci.Workflow{
		{Name: "setup", Status: "success", CreatedAt: ts("2026-09-16T10:00:00Z"), StoppedAt: ptr(ts("2026-09-16T10:00:30Z"))},
		{Name: "app", Status: "failed", CreatedAt: ts("2026-09-16T10:00:30Z"), StoppedAt: ptr(ts("2026-09-16T10:04:00Z"))},
	})

	if got.Status != StatusFailed {
		t.Errorf("one failed workflow should redden the tile: got %q", got.Status)
	}
	if got.SHA != "abcdef12" {
		t.Errorf("SHA should be shortened to 8 chars, got %q", got.SHA)
	}
	if got.Branch != "main" {
		t.Errorf("Branch = %q, want main", got.Branch)
	}
	// 10:00:00 -> 10:04:00 across both workflows.
	if got.DurationSeconds != 240 {
		t.Errorf("DurationSeconds = %d, want 240", got.DurationSeconds)
	}
}

// A pipeline that could not fetch its config never produces a workflow.
// Rolling up only workflow status leaves such a tile grey, which on a
// wall display reads as "no news" rather than "broken" -- the exact
// failure a radiator exists to prevent.
func TestErroredPipelineWithNoWorkflowsIsFailed(t *testing.T) {
	p := pipeline(43, "main", "main")
	p.State = "errored"
	p.Errors = []circleci.PipelineError{{
		Type:    "config-fetch",
		Message: "failed to fetch Branch 'main' from repository 'acme/widgets'",
	}}

	got := tile("acme", "widgets", p, nil)

	if got.Status != StatusFailed {
		t.Fatalf("errored pipeline with no workflows: got %q, want %q", got.Status, StatusFailed)
	}
	if got.Detail != p.Errors[0].Message {
		t.Errorf("Detail should surface the reason, got %q", got.Detail)
	}
}

func TestErroredStateWithoutErrorListStillFails(t *testing.T) {
	p := pipeline(44, "main", "main")
	p.State = "errored"

	got := tile("acme", "widgets", p, nil)

	if got.Status != StatusFailed {
		t.Errorf("state=errored alone should fail the tile, got %q", got.Status)
	}
	if got.Detail != "pipeline errored" {
		t.Errorf("Detail = %q, want a generic fallback", got.Detail)
	}
}

func TestTileTracksRunningWorkflow(t *testing.T) {
	p := pipeline(8, "main", "main")
	start := ts("2026-09-16T10:00:30Z")
	got := tile("acme", "widgets", p, []circleci.Workflow{
		{Name: "setup", Status: "success", CreatedAt: ts("2026-09-16T10:00:00Z"), StoppedAt: ptr(ts("2026-09-16T10:00:30Z"))},
		{Name: "app", Status: "running", CreatedAt: start},
	})

	if got.Status != StatusRunning {
		t.Errorf("Status = %q, want running", got.Status)
	}
	// The progress estimate needs to know which workflow is running and
	// since when; without these the bar cannot be drawn.
	if got.runningName != "app" {
		t.Errorf("runningName = %q, want app", got.runningName)
	}
	if !got.runningSince.Equal(start) {
		t.Errorf("runningSince = %v, want %v", got.runningSince, start)
	}
}

func TestProjectNameFromSlug(t *testing.T) {
	cases := map[string]string{
		"gh/acme/widgets":         "widgets",
		"bb/acme/widgets":         "widgets",
		"circleci/abc123/widgets": "widgets",
		"gh/acme":                 "", // too few segments to name a project
		"widgets":                 "",
		"":                        "",
	}
	for slug, want := range cases {
		if got := projectName(slug); got != want {
			t.Errorf("projectName(%q) = %q, want %q", slug, got, want)
		}
	}
}

// Reproduces the real pipeline that prompted this: 25,519 of its 25,996
// seconds were a human deciding whether to approve, which wall clock
// counts as build time and reports as a 7.2-hour build.
func TestSumBuildSecondsExcludesApprovalHold(t *testing.T) {
	job := func(name, typ string, secs int) circleci.Job {
		start := ts("2026-09-16T10:00:00Z")
		return circleci.Job{
			Name:      name,
			Type:      typ,
			StartedAt: ptr(start),
			StoppedAt: ptr(start.Add(time.Duration(secs) * time.Second)),
		}
	}
	jobs := []circleci.Job{
		job("tf-validate", "build", 13),
		job("tf-plan", "build", 55),
		job("approve", "approval", 25519),
		job("tf-apply", "build", 36),
		job("serial-start-1", "lock", 0),
		job("deploy", "build", 343),
		job("serial-end-1", "unlock", 0),
	}

	got, ok := sumBuildSeconds(jobs)
	if !ok {
		t.Fatal("expected a usable total")
	}
	if want := 13 + 55 + 36 + 343; got != want {
		t.Errorf("sumBuildSeconds = %d, want %d (approval hold must not count)", got, want)
	}
	if got > 3600 {
		t.Errorf("a %ds build time still looks like an approval wait", got)
	}
}

func TestSumBuildSecondsReportsNothingUsable(t *testing.T) {
	// A run that is only an unfinished approval has no build time to
	// report, and must say so rather than claim zero seconds.
	start := ts("2026-09-16T10:00:00Z")
	jobs := []circleci.Job{
		{Name: "approve", Type: "approval", StartedAt: ptr(start)},
	}
	if secs, ok := sumBuildSeconds(jobs); ok {
		t.Errorf("expected no usable total, got %ds", secs)
	}
	if secs, ok := sumBuildSeconds(nil); ok {
		t.Errorf("expected no usable total from no jobs, got %ds", secs)
	}
}

func TestJobRunSecondsHandlesUnfinished(t *testing.T) {
	start := ts("2026-09-16T10:00:00Z")
	if got := (circleci.Job{StartedAt: ptr(start)}).RunSeconds(); got != 0 {
		t.Errorf("a running job has no duration yet, got %d", got)
	}
	if got := (circleci.Job{StoppedAt: ptr(start)}).RunSeconds(); got != 0 {
		t.Errorf("a job that never started has no duration, got %d", got)
	}
	j := circleci.Job{StartedAt: ptr(start), StoppedAt: ptr(start.Add(90 * time.Second))}
	if got := j.RunSeconds(); got != 90 {
		t.Errorf("RunSeconds = %d, want 90", got)
	}
}

func TestQualifiedOrg(t *testing.T) {
	cases := map[string]string{
		"gh/acme":        "github.com/acme",
		"github/acme":    "github.com/acme",
		"bb/acme":        "bitbucket.org/acme",
		"bitbucket/acme": "bitbucket.org/acme",
		// Newer org-id slugs and anything unrecognised pass through
		// rather than being guessed at.
		"circleci/0a1b2c3d": "circleci/0a1b2c3d",
		"gh/":               "gh/",
		"acme":              "acme",
		"":                  "",
	}
	for in, want := range cases {
		if got := qualifiedOrg(in); got != want {
			t.Errorf("qualifiedOrg(%q) = %q, want %q", in, got, want)
		}
	}
}

// Re-running a workflow in CircleCI adds a second record under the same
// name rather than replacing the first. Reproduces the real pipeline
// that exposed this: a prod workflow that failed, was re-run, and
// succeeded -- which the board kept showing as red.
func TestRerunSupersedesTheFailedAttempt(t *testing.T) {
	p := pipeline(12, "main", "main")
	got := tile("acme", "widgets", p, []circleci.Workflow{
		{Name: "prod", Status: "failed", CreatedAt: ts("2026-09-16T15:48:12Z"), StoppedAt: ptr(ts("2026-09-16T16:18:32Z"))},
		{Name: "prod", Status: "success", CreatedAt: ts("2026-09-16T18:23:04Z"), StoppedAt: ptr(ts("2026-09-16T18:28:15Z"))},
	})

	if got.Status != StatusPassed {
		t.Errorf("a successful re-run should clear the tile: got %q", got.Status)
	}
	if len(got.Workflows) != 1 {
		t.Errorf("the superseded attempt should not be listed twice: %v", got.Workflows)
	}
}

func TestRerunThatFailsAgainStaysRed(t *testing.T) {
	// The reverse must hold too: a re-run that fails again is current,
	// and an earlier success must not mask it.
	p := pipeline(13, "main", "main")
	got := tile("acme", "widgets", p, []circleci.Workflow{
		{Name: "app", Status: "success", CreatedAt: ts("2026-09-16T10:00:00Z"), StoppedAt: ptr(ts("2026-09-16T10:04:00Z"))},
		{Name: "app", Status: "failed", CreatedAt: ts("2026-09-16T12:00:00Z"), StoppedAt: ptr(ts("2026-09-16T12:03:00Z"))},
	})
	if got.Status != StatusFailed {
		t.Errorf("the latest attempt failed, so the tile is red: got %q", got.Status)
	}
}

func TestRerunDoesNotDisturbDistinctWorkflows(t *testing.T) {
	// Collapsing by name must not merge workflows that are genuinely
	// different; worst-wins still applies across them.
	p := pipeline(14, "main", "main")
	got := tile("acme", "widgets", p, []circleci.Workflow{
		{Name: "setup", Status: "success", CreatedAt: ts("2026-09-16T10:00:00Z"), StoppedAt: ptr(ts("2026-09-16T10:00:30Z"))},
		{Name: "app", Status: "failed", CreatedAt: ts("2026-09-16T10:00:30Z"), StoppedAt: ptr(ts("2026-09-16T10:04:00Z"))},
		{Name: "app", Status: "success", CreatedAt: ts("2026-09-16T11:00:00Z"), StoppedAt: ptr(ts("2026-09-16T11:04:00Z"))},
	})
	if got.Status != StatusPassed {
		t.Errorf("setup passed and app's re-run passed: got %q", got.Status)
	}
	if len(got.Workflows) != 2 {
		t.Errorf("expected setup and app, got %v", got.Workflows)
	}
}
