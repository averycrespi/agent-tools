package reconcile

import (
	"fmt"
	"sort"
	"strings"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/config"
	gitclient "github.com/averycrespi/agent-tools/worktree-sync/internal/git"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/naming"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/tmux"
)

type OperationType string

const (
	CreateSession OperationType = "create_session"
	CreateWindow  OperationType = "create_window"
	RepairWindow  OperationType = "repair_window"
	KillWindow    OperationType = "kill_window"
)

type Operation struct {
	Type     OperationType `json:"type"`
	TargetID string        `json:"target_id,omitempty"`
	Identity string        `json:"identity"`
	Name     string        `json:"name,omitempty"`
	Path     string        `json:"path,omitempty"`
	Role     string        `json:"role,omitempty"`
}

type Desired struct {
	SessionName string        `json:"session_name"`
	Windows     []tmux.Window `json:"windows"`
}

type Plan struct {
	Desired    Desired     `json:"desired"`
	Operations []Operation `json:"operations"`
	Conflicts  []string    `json:"conflicts"`
	Report     []string    `json:"report"`
}

func ownedMetadata(metadata tmux.Metadata, repo config.Repository, role, identity string) bool {
	return metadata.Schema == tmux.MetadataSchema && metadata.Repository == repo.RepositoryIdentity && metadata.Role == role && metadata.Identity == identity
}

func desiredState(repo config.Repository, snapshot gitclient.Snapshot) (Desired, []string) {
	desired := Desired{SessionName: "wts-" + repo.ID}
	desired.Windows = append(desired.Windows, tmux.Window{Name: "base", Path: repo.PrimaryRoot, Metadata: tmux.Metadata{Schema: tmux.MetadataSchema, Repository: repo.RepositoryIdentity, Role: "base", Identity: repo.RepositoryIdentity}})
	items := make([]naming.Item, 0)
	worktrees := make(map[string]gitclient.Worktree)
	report := make([]string, 0)
	for _, worktree := range snapshot.Worktrees {
		if worktree.Path == repo.PrimaryRoot || worktree.Identity == repo.RepositoryIdentity {
			continue
		}
		if worktree.Prunable != "" || worktree.Exclusion == "prunable" {
			report = append(report, fmt.Sprintf("%s: prunable", worktree.Path))
			continue
		}
		if worktree.Exclusion != "" {
			report = append(report, fmt.Sprintf("%s: %s", worktree.Path, worktree.Exclusion))
			continue
		}
		inside := false
		for _, root := range repo.AllowedRoots {
			if config.Contains(root, worktree.Path) {
				inside = true
				break
			}
		}
		if !inside {
			report = append(report, fmt.Sprintf("%s: outside allowed roots", worktree.Path))
			continue
		}
		label := worktree.Branch
		if worktree.Detached || label == "" {
			label = naming.Detached(worktree.HEAD, worktree.Path)
		}
		items = append(items, naming.Item{Identity: worktree.Identity, Label: label})
		worktrees[worktree.Identity] = worktree
	}
	names := naming.Windows(items)
	ids := make([]string, 0, len(worktrees))
	for id := range worktrees {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		worktree := worktrees[id]
		desired.Windows = append(desired.Windows, tmux.Window{Name: names[id], Path: worktree.Path, Metadata: tmux.Metadata{Schema: tmux.MetadataSchema, Repository: repo.RepositoryIdentity, Role: "worktree", Identity: id}})
	}
	return desired, report
}

func Build(repo config.Repository, gitSnapshot gitclient.Snapshot, actual tmux.Snapshot) Plan {
	desired, report := desiredState(repo, gitSnapshot)
	plan := Plan{Desired: desired, Report: report}
	var session *tmux.Session
	for i := range actual.Sessions {
		candidate := &actual.Sessions[i]
		if candidate.Name != desired.SessionName {
			continue
		}
		if !ownedMetadata(candidate.Metadata, repo, "session", repo.RepositoryIdentity) {
			kind := "foreign-owned"
			if candidate.Metadata.Schema == 0 {
				kind = "untagged"
			}
			plan.Conflicts = append(plan.Conflicts, fmt.Sprintf("%s session %q conflicts with registered repository", kind, candidate.Name))
			return plan
		}
		session = candidate
		break
	}
	if session == nil {
		base := desired.Windows[0]
		plan.Operations = append(plan.Operations, Operation{Type: CreateSession, Identity: base.Metadata.Identity, Name: base.Name, Path: base.Path, Role: "base"})
		for _, window := range desired.Windows[1:] {
			plan.Operations = append(plan.Operations, Operation{Type: CreateWindow, Identity: window.Metadata.Identity, Name: window.Name, Path: window.Path, Role: "worktree"})
		}
		return plan
	}
	manualNames := make(map[string]bool)
	managed := make(map[string][]tmux.Window)
	for _, window := range session.Windows {
		if window.Metadata.Schema == tmux.MetadataSchema && window.Metadata.Repository == repo.RepositoryIdentity && (window.Metadata.Role == "base" || window.Metadata.Role == "worktree") {
			managed[window.Metadata.Identity] = append(managed[window.Metadata.Identity], window)
		} else {
			manualNames[window.Name] = true
		}
	}
	for i := range desired.Windows {
		window := &desired.Windows[i]
		if manualNames[window.Name] {
			window.Name += "-" + identitySuffix(window.Metadata.Identity)
		}
	}
	duplicates := make([]Operation, 0)
	desiredIDs := make(map[string]bool)
	for _, window := range desired.Windows {
		identity := window.Metadata.Identity
		desiredIDs[identity] = true
		matches := managed[identity]
		sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
		role := window.Metadata.Role
		if len(matches) == 0 {
			plan.Operations = append(plan.Operations, Operation{Type: CreateWindow, Identity: identity, Name: window.Name, Path: window.Path, Role: role})
			continue
		}
		if !gitSnapshot.Complete {
			continue
		}
		keep := matches[0]
		if keep.Name != window.Name || keep.Path != window.Path || !ownedMetadata(keep.Metadata, repo, role, identity) {
			plan.Operations = append(plan.Operations, Operation{Type: RepairWindow, TargetID: keep.ID, Identity: identity, Name: window.Name, Path: window.Path, Role: role})
		}
		for _, duplicate := range matches[1:] {
			duplicates = append(duplicates, Operation{Type: KillWindow, TargetID: duplicate.ID, Identity: identity})
		}
	}
	plan.Operations = append(plan.Operations, duplicates...)
	if gitSnapshot.Complete && actual.Complete {
		stale := make([]tmux.Window, 0)
		for identity, windows := range managed {
			if !desiredIDs[identity] {
				stale = append(stale, windows...)
			}
		}
		sort.Slice(stale, func(i, j int) bool { return stale[i].ID < stale[j].ID })
		for _, window := range stale {
			plan.Operations = append(plan.Operations, Operation{Type: KillWindow, TargetID: window.ID, Identity: window.Metadata.Identity})
		}
	}
	return plan
}

func identitySuffix(identity string) string {
	name := naming.Windows([]naming.Item{{Identity: identity, Label: "x"}, {Identity: identity + "-collision", Label: "x"}})[identity]
	return strings.TrimPrefix(name, "x-")
}
