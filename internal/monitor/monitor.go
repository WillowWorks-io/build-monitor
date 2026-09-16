// Package monitor polls CircleCI and keeps the current board in memory.
package monitor

import (
	"context"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/willowworks-io/build-monitor/internal/circleci"
	"github.com/willowworks-io/build-monitor/internal/config"
)

// Status is a project's rolled-up build state, in radiator terms.
type Status string

const (
	StatusPassed  Status = "passed"
	StatusFailed  Status = "failed"
	StatusRunning Status = "running"
	StatusOnHold  Status = "on_hold"
	StatusUnknown Status = "unknown"
)

// severity orders statuses for rollup: a project's tile shows the worst
// status across the workflows of its latest pipeline, so one failed
// workflow colours the tile red even when its siblings pass.
func severity(s Status) int {
	switch s {
	case StatusFailed:
		return 4
	case StatusRunning:
		return 3
	case StatusOnHold:
		return 2
	case StatusPassed:
		return 1
	default:
		return 0
	}
}

// fromWorkflow maps a CircleCI workflow status onto a radiator status.
func fromWorkflow(s string) Status {
	switch s {
	case "success":
		return StatusPassed
	case "failed", "error", "failing", "unauthorized":
		return StatusFailed
	case "running":
		return StatusRunning
	case "on_hold":
		return StatusOnHold
	default:
		// "canceled", "not_run" and anything CircleCI adds later read as
		// absence of a signal, not as failure.
		return StatusUnknown
	}
}

// Project is one tile on the board.
type Project struct {
	Org         string    `json:"org"`
	Name        string    `json:"name"`
	Status      Status    `json:"status"`
	Pipeline    int       `json:"pipeline"`
	Branch      string    `json:"branch"`
	SHA         string    `json:"sha"`
	CommitTitle string    `json:"commit_title"`
	Actor       string    `json:"actor"`
	Detail      string    `json:"detail"`
	FinishedAt  time.Time `json:"finished_at"`
	URL         string    `json:"url"`
	Workflows   []string  `json:"workflows"`

	// DurationSeconds is how long the latest finished run took.
	DurationSeconds int `json:"duration_seconds"`
	// Progress is 0..1 for a running build, estimated from the median
	// duration of that workflow. Zero when there is no basis to estimate.
	Progress float64 `json:"progress"`
	// ETASeconds is the estimated remaining time for a running build.
	ETASeconds int `json:"eta_seconds"`
	// BrokenSince is when this project last went red, and BrokenBuilds
	// how many consecutive runs have failed. A radiator that shows only
	// the latest result cannot distinguish "just broke" from "broken for
	// a month", which is the difference between noise and an emergency.
	BrokenSince  time.Time `json:"broken_since"`
	BrokenBuilds int       `json:"broken_builds"`

	// Internal, for the enrichment pass; not part of the feed.
	slug         string
	runningName  string
	runningSince time.Time
	workflowIDs  []string
}

// heldDurationThreshold is the wall-clock duration past which a run is
// assumed to have been parked on an approval gate rather than genuinely
// running that long, and its real build time is recomputed from jobs.
const heldDurationThreshold = 20 * time.Minute

// Board is the whole radiator at a moment in time.
type Board struct {
	Projects  []Project `json:"projects"`
	UpdatedAt time.Time `json:"updated_at"`
	Errors    []string  `json:"errors"`
	Counts    struct {
		Passed  int `json:"passed"`
		Failed  int `json:"failed"`
		Running int `json:"running"`
		OnHold  int `json:"on_hold"`
		Unknown int `json:"unknown"`
	} `json:"counts"`
}

// Monitor polls CircleCI on an interval and serves the latest Board.
type Monitor struct {
	cfg    *config.Config
	client *circleci.Client
	log    *slog.Logger

	mu    sync.RWMutex
	board Board

	// Median workflow durations change slowly, so they are cached well
	// past the poll interval rather than refetched every cycle.
	durMu    sync.RWMutex
	durCache map[string]map[string]int
	durFetch time.Time
}

// durationTTL is how long cached median durations stay fresh.
const durationTTL = 10 * time.Minute

// New builds a Monitor.
func New(cfg *config.Config, client *circleci.Client, log *slog.Logger) *Monitor {
	return &Monitor{
		cfg:    cfg,
		client: client,
		log:    log,
		// Empty rather than nil: the feed always presents projects and
		// errors as JSON arrays, so a client never has to tell an empty
		// board apart from a null one.
		board:    Board{Projects: []Project{}, Errors: []string{}},
		durCache: map[string]map[string]int{},
	}
}

// Board returns the most recent board.
func (m *Monitor) Board() Board {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.board
}

// Run polls until ctx is cancelled, refreshing once immediately so the
// radiator is populated before the first tick.
func (m *Monitor) Run(ctx context.Context) {
	m.refresh(ctx)

	t := time.NewTicker(m.cfg.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.refresh(ctx)
		}
	}
}

func (m *Monitor) refresh(ctx context.Context) {
	start := time.Now()

	var (
		mu       sync.Mutex
		projects = []Project{}
		errs     = []string{}
		wg       sync.WaitGroup
	)

	for _, org := range m.cfg.Orgs {
		wg.Add(1)
		go func(org string) {
			defer wg.Done()
			ps, err := m.pollOrg(ctx, org)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err.Error())
				return
			}
			projects = append(projects, ps...)
		}(org)
	}
	wg.Wait()

	projects = m.enrich(ctx, projects)

	// Failures first, then oldest-finished first: whatever needs a human
	// rises to the top-left, where the eye lands.
	sort.Slice(projects, func(i, j int) bool {
		si, sj := severity(projects[i].Status), severity(projects[j].Status)
		if si != sj {
			return si > sj
		}
		if !projects[i].FinishedAt.Equal(projects[j].FinishedAt) {
			return projects[i].FinishedAt.After(projects[j].FinishedAt)
		}
		return projects[i].Name < projects[j].Name
	})

	board := Board{Projects: projects, UpdatedAt: time.Now(), Errors: errs}
	for _, p := range projects {
		switch p.Status {
		case StatusPassed:
			board.Counts.Passed++
		case StatusFailed:
			board.Counts.Failed++
		case StatusRunning:
			board.Counts.Running++
		case StatusOnHold:
			board.Counts.OnHold++
		default:
			board.Counts.Unknown++
		}
	}

	m.mu.Lock()
	m.board = board
	m.mu.Unlock()

	m.log.Info("refreshed board",
		"projects", len(projects),
		"failed", board.Counts.Failed,
		"errors", len(errs),
		"took", time.Since(start).Round(time.Millisecond))
}

// pollOrg builds a tile per project in one org.
func (m *Monitor) pollOrg(ctx context.Context, org string) ([]Project, error) {
	pipelines, err := m.client.ListPipelines(ctx, org)
	if err != nil {
		return nil, err
	}

	// Two forms of the same org: the bare name is what exclude patterns
	// match against, and the host-qualified name is what tiles display.
	// Qualifying the matched form would add a path segment and silently
	// stop every configured "*/pattern" from matching.
	orgName := org
	if _, after, ok := strings.Cut(org, "/"); ok {
		orgName = after
	}
	orgLabel := qualifiedOrg(org)

	// Keep only the newest qualifying pipeline per project. The API
	// returns newest first, so the first sighting wins.
	latest := map[string]circleci.Pipeline{}
	var order []string
	for _, p := range pipelines {
		name := projectName(p.ProjectSlug)
		if name == "" || m.cfg.Excluded(orgName+"/"+name) {
			continue
		}
		if m.cfg.BranchFilter == config.BranchDefault && !p.OnDefaultBranch() {
			continue
		}
		if _, seen := latest[name]; seen {
			continue
		}
		latest[name] = p
		order = append(order, name)
	}

	var (
		mu   sync.Mutex
		out  = []Project{}
		wg   sync.WaitGroup
		sem  = make(chan struct{}, 6)
		errs []string
	)
	for _, name := range order {
		wg.Add(1)
		go func(name string, p circleci.Pipeline) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			wfs, err := m.client.ListWorkflows(ctx, p.ID)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err.Error())
				return
			}
			out = append(out, tile(orgLabel, name, p, wfs))
		}(name, latest[name])
	}
	wg.Wait()

	if len(errs) > 0 && len(out) == 0 {
		return nil, errWorkflows{org: org, msgs: errs}
	}
	return out, nil
}

func tile(org, name string, p circleci.Pipeline, wfs []circleci.Workflow) Project {
	ga := p.TriggerParameters.GitHubApp

	status := StatusUnknown
	var finished, started, runningSince time.Time
	var runningName string
	names := make([]string, 0, len(wfs))
	ids := make([]string, 0, len(wfs))
	for _, w := range wfs {
		names = append(names, w.Name)
		ids = append(ids, w.ID)
		if s := fromWorkflow(w.Status); severity(s) > severity(status) {
			status = s
		}
		if w.StoppedAt != nil && w.StoppedAt.After(finished) {
			finished = *w.StoppedAt
		}
		if started.IsZero() || w.CreatedAt.Before(started) {
			started = w.CreatedAt
		}
		if w.Status == "running" {
			runningName = w.Name
			runningSince = w.CreatedAt
		}
	}

	// How long the latest finished run took, end to end across workflows.
	var durSecs int
	if !finished.IsZero() && !started.IsZero() && finished.After(started) {
		durSecs = int(finished.Sub(started).Seconds())
	}
	if finished.IsZero() {
		finished = p.CreatedAt
	}

	// A pipeline that errored never reached a workflow, so the loop above
	// left the tile grey. That reads as "no news" on a wall display when
	// it actually means the build is broken.
	detail := ga.CommitTitle
	if p.Errored() {
		status = StatusFailed
		if msg := p.ErrorMessage(); msg != "" {
			detail = msg
		} else {
			detail = "pipeline errored"
		}
	}

	sha := ga.CheckoutSHA
	if len(sha) > 8 {
		sha = sha[:8]
	}

	return Project{
		Org:             org,
		Name:            name,
		Status:          status,
		Pipeline:        p.Number,
		Branch:          p.Branch(),
		SHA:             sha,
		CommitTitle:     ga.CommitTitle,
		Actor:           p.Actor(),
		Detail:          detail,
		FinishedAt:      finished,
		URL:             "https://app.circleci.com/pipelines/" + p.ProjectSlug + "/" + itoa(p.Number),
		Workflows:       names,
		DurationSeconds: durSecs,
		slug:            p.ProjectSlug,
		runningName:     runningName,
		runningSince:    runningSince,
		workflowIDs:     ids,
	}
}

// projectName pulls "repo" out of a "gh/org/repo" project slug.
func projectName(slug string) string {
	parts := strings.Split(slug, "/")
	if len(parts) < 3 {
		return ""
	}
	return parts[len(parts)-1]
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

type errWorkflows struct {
	org  string
	msgs []string
}

func (e errWorkflows) Error() string {
	return e.org + ": " + strings.Join(e.msgs, "; ")
}

// medianDurations returns median seconds per workflow for a project,
// refetching only when the cache has gone stale. Medians move slowly, so
// paying for them every poll would be waste.
func (m *Monitor) medianDurations(ctx context.Context, slug, branch string) map[string]int {
	m.durMu.RLock()
	fresh := time.Since(m.durFetch) < durationTTL
	cached, ok := m.durCache[slug]
	m.durMu.RUnlock()
	if fresh && ok {
		return cached
	}

	wfs, err := m.client.InsightsWorkflows(ctx, slug, branch)
	if err != nil {
		// Insights is an enhancement, not a dependency: a project with no
		// history yet simply gets no progress bar.
		return cached
	}

	out := map[string]int{}
	for _, w := range wfs {
		if d := w.Metrics.DurationMetrics.Median; d > 0 {
			out[w.Name] = d
		}
	}

	m.durMu.Lock()
	m.durCache[slug] = out
	m.durFetch = time.Now()
	m.durMu.Unlock()
	return out
}

// failureStreak reports how many consecutive runs have failed and when
// the run of failures began.
func (m *Monitor) failureStreak(ctx context.Context, slug, workflow, branch string) (int, time.Time) {
	runs, err := m.client.InsightsWorkflowRuns(ctx, slug, workflow, branch, 30)
	if err != nil || len(runs) == 0 {
		return 0, time.Time{}
	}

	count := 0
	since := time.Time{}
	for _, r := range runs { // newest first
		if r.Status == "success" {
			break
		}
		count++
		since = r.CreatedAt // keeps walking back to the oldest in the run
	}
	return count, since
}

// enrich adds progress estimates and breakage history to the board.
// Duration lookups are per project; streak lookups happen only for
// projects that are actually red, so the cost scales with breakage
// rather than with fleet size.
func (m *Monitor) enrich(ctx context.Context, projects []Project) []Project {
	branch := ""
	if m.cfg.BranchFilter == config.BranchDefault {
		branch = "main"
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)

	for i := range projects {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			p := &projects[i]

			if p.Status == StatusRunning && p.runningName != "" && !p.runningSince.IsZero() {
				medians := m.medianDurations(ctx, p.slug, branch)
				if med, ok := medians[p.runningName]; ok && med > 0 {
					elapsed := time.Since(p.runningSince).Seconds()
					// Cap just short of complete: a build that overruns its
					// median is still running, and a full bar would read as
					// finished.
					p.Progress = math.Min(elapsed/float64(med), 0.99)
					if eta := med - int(elapsed); eta > 0 {
						p.ETASeconds = eta
					}
				}
			}

			// Wall clock counts time parked on an approval gate, which is a
			// human deciding, not a build running. Left alone, a pipeline
			// someone approved the next morning reports as a seven-hour
			// build. Recomputed from jobs only when the number is already
			// implausible, so the common case costs nothing.
			if time.Duration(p.DurationSeconds)*time.Second > heldDurationThreshold {
				if secs, ok := m.buildSeconds(ctx, p.workflowIDs); ok {
					p.DurationSeconds = secs
				}
			}

			if p.Status == StatusFailed {
				if p.runningName == "" && len(p.Workflows) > 0 {
					// Use whichever workflow the tile rolled up from.
					n, since := m.failureStreak(ctx, p.slug, p.Workflows[0], branch)
					p.BrokenBuilds, p.BrokenSince = n, since
				}
				if p.BrokenSince.IsZero() {
					// A pipeline that errored produced no workflow to ask
					// about, so fall back to when it happened.
					p.BrokenSince = p.FinishedAt
				}
			}
		}(i)
	}
	wg.Wait()
	return projects
}

// buildSeconds sums the time a run's jobs actually spent running,
// excluding approval gates -- and lock/unlock jobs, which serialise
// deploys and are likewise waiting rather than working.
func (m *Monitor) buildSeconds(ctx context.Context, workflowIDs []string) (int, bool) {
	var all []circleci.Job
	for _, id := range workflowIDs {
		jobs, err := m.client.ListWorkflowJobs(ctx, id)
		if err != nil {
			return 0, false
		}
		all = append(all, jobs...)
	}
	return sumBuildSeconds(all)
}

// sumBuildSeconds totals the time jobs spent actually running. Approval
// gates are a human deciding, and lock/unlock jobs serialise deploys --
// all three are waiting, not working, and counting them is what makes a
// pipeline someone approved the next morning look like a seven-hour
// build.
func sumBuildSeconds(jobs []circleci.Job) (int, bool) {
	total := 0
	any := false
	for _, j := range jobs {
		switch j.Type {
		case "approval", "lock", "unlock":
			continue
		}
		if secs := j.RunSeconds(); secs > 0 {
			total += secs
			any = true
		}
	}
	return total, any
}

// qualifiedOrg turns a CircleCI org slug into its host-qualified form,
// so a tile reads "github.com/acme" rather than a bare "ACME" that says
// nothing about where the code lives. Slugs CircleCI may add later pass
// through unchanged rather than being guessed at.
func qualifiedOrg(slug string) string {
	prefix, rest, ok := strings.Cut(slug, "/")
	if !ok || rest == "" {
		return slug
	}
	switch prefix {
	case "gh", "github":
		return "github.com/" + rest
	case "bb", "bitbucket":
		return "bitbucket.org/" + rest
	default:
		return slug
	}
}
