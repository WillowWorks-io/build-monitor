package circleci

import "testing"

func ghPipeline(branch, defaultBranch string) Pipeline {
	var p Pipeline
	ga := &p.TriggerParameters.GitHubApp
	ga.Branch = branch
	ga.DefaultBranch = defaultBranch
	return p
}

func TestOnDefaultBranch(t *testing.T) {
	cases := []struct {
		name          string
		branch, deflt string
		want          bool
	}{
		{"on trunk", "main", "main", true},
		{"feature branch", "fix/auth", "main", false},
		{"trunk not named main", "trunk", "trunk", true},
		{"feature against a non-main default", "topic", "trunk", false},
		// Projects on older webhook triggers return an empty
		// trigger_parameters entirely. Treating that as off-trunk would
		// silently drop them from the board, and a project vanishing
		// from a radiator is its own failure mode.
		{"legacy trigger, no metadata", "", "", true},
		{"branch known, default not", "main", "", true},
		{"default known, branch not", "", "main", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ghPipeline(c.branch, c.deflt).OnDefaultBranch(); got != c.want {
				t.Errorf("OnDefaultBranch() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestErrored(t *testing.T) {
	var clean Pipeline
	clean.State = "created"
	if clean.Errored() {
		t.Error("a created pipeline is not errored")
	}

	byState := Pipeline{State: "errored"}
	if !byState.Errored() {
		t.Error("state=errored should count as errored")
	}

	byList := Pipeline{
		State:  "created",
		Errors: []PipelineError{{Type: "config-fetch", Message: "boom"}},
	}
	if !byList.Errored() {
		t.Error("a populated errors list should count as errored")
	}
	if got := byList.ErrorMessage(); got != "boom" {
		t.Errorf("ErrorMessage() = %q, want boom", got)
	}
	if got := byState.ErrorMessage(); got != "" {
		t.Errorf("ErrorMessage() with no errors = %q, want empty", got)
	}
}

func TestActorPrefersCommitAuthor(t *testing.T) {
	p := ghPipeline("main", "main")
	p.Trigger.Actor.Login = "ci-bot"
	if got := p.Actor(); got != "ci-bot" {
		t.Errorf("Actor() = %q, want the trigger actor when no commit author", got)
	}

	p.TriggerParameters.GitHubApp.CommitAuthor = "ada"
	if got := p.Actor(); got != "ada" {
		t.Errorf("Actor() = %q, want the commit author to win", got)
	}
}
