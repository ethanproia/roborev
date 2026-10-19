package daemon

import (
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
	if !reviewHostApplies(workflow, reviewType, jobType) {
		return agent.ReviewHostLocal, nil
	}
	repoHost, globalHost := "", ""
	if repo != nil {
		repoHost = repo.Host
	}
	if cfg != nil {
		globalHost = cfg.Host
	}
	host, err := agent.ReviewHost(flag, repoHost, globalHost)
	if err != nil {
		out, _ := rawJSONOutput(http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return "", out
	}
	return host, nil
}
