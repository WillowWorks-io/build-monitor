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

func TestFollowedProjectSlug(t *testing.T) {
	cases := []struct {
		vcs, want string
	}{
		{"github", "gh/acme/widgets"},
		{"bitbucket", "bb/acme/widgets"},
	}
	for _, c := range cases {
		f := FollowedProject{VCSType: c.vcs, Username: "acme", Reponame: "widgets"}
		if got := f.Slug(); got != c.want {
			t.Errorf("Slug() for %s = %q, want %q", c.vcs, got, c.want)
		}
	}
}

func TestFollowedProjectInOrg(t *testing.T) {
	gh := FollowedProject{VCSType: "github", Username: "Acme", Reponame: "widgets"}
	cases := map[string]bool{
		"gh/Acme":     true,
		"gh/acme":     true, // config casing need not match CircleCI's
		"github/Acme": true,
		"bb/Acme":     false,
		"gh/Acme-IT":  false,
		"Acme":        false,
	}
	for org, want := range cases {
		if got := gh.InOrg(org); got != want {
			t.Errorf("InOrg(%q) = %v, want %v", org, got, want)
		}
	}

	// Native CircleCI projects have opaque slugs that cannot be built
	// from these fields, so they must never match a configured org.
	native := FollowedProject{VCSType: "circleci", Username: "Acme", Reponame: "widgets"}
	if native.InOrg("circleci/Acme") {
		t.Error("a circleci-type project should not match any org")
	}
}
