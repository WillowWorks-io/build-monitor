// Package circleci is a minimal client for the parts of the CircleCI v2
// API a build radiator needs.
package circleci

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

const baseURL = "https://circleci.com/api/v2"

// Client talks to the CircleCI v2 API with a personal API token.
type Client struct {
	token string
	http  *http.Client
}

// New builds a Client. The token is held in memory only.
func New(token string) *Client {
	return &Client{
		token: token,
		http:  &http.Client{Timeout: 20 * time.Second},
	}
}

// PipelineError is a pipeline-level failure, such as a config that could
// not be fetched or parsed. A pipeline carrying one never produces
// workflows, so it is only visible here.
type PipelineError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// Pipeline is one pipeline run for a project.
type Pipeline struct {
	ID          string          `json:"id"`
	Number      int             `json:"number"`
	ProjectSlug string          `json:"project_slug"`
	State       string          `json:"state"`
	CreatedAt   time.Time       `json:"created_at"`
	Errors      []PipelineError `json:"errors"`

	// The v2 pipeline endpoint returns no "vcs" block for GitHub App
	// projects -- branch, SHA and commit metadata arrive under
	// trigger_parameters.github_app instead.
	TriggerParameters struct {
		GitHubApp struct {
			Branch        string `json:"branch"`
			DefaultBranch string `json:"default_branch"`
			CheckoutSHA   string `json:"checkout_sha"`
			CommitTitle   string `json:"commit_title"`
			CommitAuthor  string `json:"commit_author_name"`
			RepoFullName  string `json:"repo_full_name"`
		} `json:"github_app"`
	} `json:"trigger_parameters"`

	Trigger struct {
		Actor struct {
			Login string `json:"login"`
		} `json:"actor"`
	} `json:"trigger"`
}

// Errored reports whether the pipeline itself failed before it could run
// anything. Such a pipeline has no workflows at all, so a rollup that
// only reads workflow status would show it as absence of news rather
// than as the breakage it is.
func (p Pipeline) Errored() bool {
	return p.State == "errored" || len(p.Errors) > 0
}

// ErrorMessage returns the first pipeline-level error message, if any.
func (p Pipeline) ErrorMessage() string {
	if len(p.Errors) == 0 {
		return ""
	}
	return p.Errors[0].Message
}

// Branch reports the branch the pipeline ran on.
func (p Pipeline) Branch() string { return p.TriggerParameters.GitHubApp.Branch }

// OnDefaultBranch reports whether the pipeline ran on the repo's default
// branch. A pipeline carrying neither field is treated as on-default so a
// project never silently vanishes from the radiator.
func (p Pipeline) OnDefaultBranch() bool {
	ga := p.TriggerParameters.GitHubApp
	if ga.Branch == "" || ga.DefaultBranch == "" {
		return true
	}
	return ga.Branch == ga.DefaultBranch
}

// Actor reports who triggered the pipeline.
func (p Pipeline) Actor() string {
	if a := p.TriggerParameters.GitHubApp.CommitAuthor; a != "" {
		return a
	}
	return p.Trigger.Actor.Login
}

// Workflow is one workflow within a pipeline.
type Workflow struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Status    string     `json:"status"`
	CreatedAt time.Time  `json:"created_at"`
	StoppedAt *time.Time `json:"stopped_at"`
}

type pipelinePage struct {
	Items         []Pipeline `json:"items"`
	NextPageToken string     `json:"next_page_token"`
}

type workflowPage struct {
	Items []Workflow `json:"items"`
}

// ListPipelines returns the most recent page of pipelines for an org.
// One page is deliberate: the radiator only ever shows each project's
// latest run, and older pages cannot contain it.
func (c *Client) ListPipelines(ctx context.Context, orgSlug string) ([]Pipeline, error) {
	u := fmt.Sprintf("%s/pipeline?org-slug=%s", baseURL, url.QueryEscape(orgSlug))
	var page pipelinePage
	if err := c.get(ctx, u, &page); err != nil {
		return nil, fmt.Errorf("list pipelines for %s: %w", orgSlug, err)
	}
	return page.Items, nil
}

// ListWorkflows returns the workflows belonging to a pipeline.
func (c *Client) ListWorkflows(ctx context.Context, pipelineID string) ([]Workflow, error) {
	u := fmt.Sprintf("%s/pipeline/%s/workflow", baseURL, url.PathEscape(pipelineID))
	var page workflowPage
	if err := c.get(ctx, u, &page); err != nil {
		return nil, fmt.Errorf("list workflows for %s: %w", pipelineID, err)
	}
	return page.Items, nil
}

func (c *Client) get(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Circle-Token", c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("circleci rejected the token (HTTP %d)", resp.StatusCode)
	case http.StatusTooManyRequests:
		return fmt.Errorf("circleci rate limited this poll (HTTP 429)")
	default:
		return fmt.Errorf("circleci returned HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
