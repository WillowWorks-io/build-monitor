// Package monitor polls CircleCI and keeps the current board in memory.
package monitor

import (
	"context"
	"log/slog"
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
}

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
}

// New builds a Monitor.
func New(cfg *config.Config, client *circleci.Client, log *slog.Logger) *Monitor {
	return &Monitor{
		cfg:    cfg,
		client: client,
		log:    log,
		// Empty rather than nil: the feed always presents projects and
		// errors as JSON arrays, so a client never has to tell an empty
		// board apart from a null one.
		board: Board{Projects: []Project{}, Errors: []string{}},
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

	orgName := org
	if _, after, ok := strings.Cut(org, "/"); ok {
		orgName = after
	}

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
			out = append(out, tile(orgName, name, p, wfs))
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
	var finished time.Time
	names := make([]string, 0, len(wfs))
	for _, w := range wfs {
		names = append(names, w.Name)
		if s := fromWorkflow(w.Status); severity(s) > severity(status) {
			status = s
		}
		if w.StoppedAt != nil && w.StoppedAt.After(finished) {
			finished = *w.StoppedAt
		}
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
		Org:         org,
		Name:        name,
		Status:      status,
		Pipeline:    p.Number,
		Branch:      p.Branch(),
		SHA:         sha,
		CommitTitle: ga.CommitTitle,
		Actor:       p.Actor(),
		Detail:      detail,
		FinishedAt:  finished,
		URL:         "https://app.circleci.com/pipelines/" + p.ProjectSlug + "/" + itoa(p.Number),
		Workflows:   names,
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
