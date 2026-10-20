package daemon

import (
	"fmt"
	"net/http"

	"go.kenn.io/roborev/internal/agent"
	"go.kenn.io/roborev/internal/config"
	"go.kenn.io/roborev/internal/storage"
)

// reviewHostApplies reports whether this enqueue may leave the local agent
// backend. Goal classification and fix jobs stay local: cursor-cloud reviews
// one commit and does not classify or edit.
func reviewHostApplies(workflow, reviewType, jobType string) bool {
	if workflow == "fix" || reviewType == config.ReviewTypeGoal {
		return false
	}
	switch jobType {
	case "", storage.JobTypeReview, storage.JobTypeRange, storage.JobTypeDirty:
		return true
	default:
		return false
	}
}

func resolveEnqueueHost(workflow, reviewType, jobType, flag string, repo *config.RepoConfig, cfg *config.Config) (string, *RawJSONOutput) {
	repoHost, globalHost := "", ""
	if repo != nil {
		repoHost = repo.Host
	}
	if cfg != nil {
		globalHost = cfg.Host
	}
	// An explicit cursor-cloud host on a dirty or range review is rejected
	// before agent selection. It is not rewritten to the local backend.
	if jobType == storage.JobTypeDirty || jobType == storage.JobTypeRange {
		host, err := agent.ReviewHost(flag, repoHost, globalHost)
		if host == agent.CursorCloudHost || agent.IsCursorCloudConfig(err) {
			out, _ := rawJSONOutput(http.StatusBadRequest, ErrorResponse{
				Error: fmt.Sprintf("cursor-cloud reviews one commit and cannot run a %s review", jobType),
			})
			return "", out
		}
	}
	if !reviewHostApplies(workflow, reviewType, jobType) {
		return agent.ReviewHostLocal, nil
	}
	host, err := agent.ReviewHost(flag, repoHost, globalHost)
	if err != nil {
		out, _ := rawJSONOutput(http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return "", out
	}
	return host, nil
}
