package agent

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"slices"
	"strings"
	"time"

	"go.kenn.io/roborev/internal/config"
	"go.kenn.io/roborev/internal/cursorcloud"
	"go.kenn.io/roborev/internal/git"
)

const (
	// ReviewHostLocal is today's local agent backend.
	ReviewHostLocal = "local"
	// CursorCloudHost is the opt-in Cursor cloud agents review host.
	CursorCloudHost = "cursor-cloud"
	// EnvDefaultHost selects the review host when flag and config are empty.
	// CURSOR_API_KEY never selects a host.
	EnvDefaultHost = "ROBOREV_DEFAULT_HOST"

	cursorCloudAgentName = CursorCloudHost
	cursorCloudMaxPoll   = 5 * time.Second
	// cursorCloudDeleteTimeout bounds the post-review DELETE. It matches the
	// cursorcloud client's 60s response-header timeout so a stalled delete is
	// not abandoned before that client gives up on the socket.
	cursorCloudDeleteTimeout = 60 * time.Second
)

// cursorCloudSafety is sent with every cloud review. The API has no fields
// that enforce it; see the docs limitation.
const cursorCloudSafety = `Remote review constraints:
- Do not use shell tools.
- Ignore this repository's .cursor configuration.
- Do not run environment.json setup.
- Do not write to the repository.
- Read only the tree at the requested commit.
`

// CursorCloudConfigError means cursor-cloud was chosen and CURSOR_API_KEY is
// missing. Callers must not fall back to a local agent.
type CursorCloudConfigError struct{}

func (e *CursorCloudConfigError) Error() string {
	return fmt.Sprintf("host %q requires %s", CursorCloudHost, cursorcloud.EnvAPIKey)
}

// IsCursorCloudConfig reports whether err is a missing CURSOR_API_KEY for an
// explicitly chosen cursor-cloud host.
func IsCursorCloudConfig(err error) bool {
	_, ok := errors.AsType[*CursorCloudConfigError](err)
	return ok
}

// ReviewHost resolves the review host. flag wins, then repo config, then
// global config, then ROBOREV_DEFAULT_HOST, then local. An empty result is
// local. CURSOR_API_KEY is read only after cursor-cloud is already chosen.
func ReviewHost(flag string, repoHost, globalHost string) (string, error) {
	chosen := strings.TrimSpace(flag)
	if chosen == "" {
		chosen = strings.TrimSpace(repoHost)
	}
	if chosen == "" {
		chosen = strings.TrimSpace(globalHost)
	}
	if chosen == "" {
		chosen = strings.TrimSpace(os.Getenv(EnvDefaultHost))
	}
	switch strings.ToLower(chosen) {
	case "", ReviewHostLocal:
		return ReviewHostLocal, nil
	case CursorCloudHost:
		if !cursorCloudKeySet() {
			return "", &CursorCloudConfigError{}
		}
		return CursorCloudHost, nil
	case hostAgentName:
		if !HostAllowed(HostNamed) {
			return "", &HostUnavailableError{}
		}
		return hostAgentName, nil
	default:
		return "", fmt.Errorf("unknown host %q (known: %s, %s, %s)", chosen, ReviewHostLocal, hostAgentName, CursorCloudHost)
	}
}

func cursorCloudKeySet() bool {
	return strings.TrimSpace(os.Getenv(cursorcloud.EnvAPIKey)) != ""
}

func skippedCursorCloudBackupNote(backups ...string) string {
	if cursorCloudKeySet() || !slices.ContainsFunc(backups, func(name string) bool {
		return resolveAlias(strings.TrimSpace(name)) == cursorCloudAgentName
	}) {
		return ""
	}
	return fmt.Sprintf("\nbackup %q was skipped: %s is not set", cursorCloudAgentName, cursorcloud.EnvAPIKey)
}

// explicitCursorCloud returns the cursor-cloud agent when name selects it.
// A missing key is an error. Other names return nil, nil.
func explicitCursorCloud(name string) (Agent, error) {
	if resolveAlias(strings.TrimSpace(name)) != cursorCloudAgentName {
		return nil, nil
	}
	if !cursorCloudKeySet() {
		return nil, &CursorCloudConfigError{}
	}
	return Get(cursorCloudAgentName)
}

// CursorCloudAgent runs one review as a Cursor cloud agent.
type CursorCloudAgent struct {
	Model        string
	Timeout      time.Duration
	PollInterval time.Duration
	// Client overrides cursorcloud.NewFromEnv. Production leaves it nil.
	Client cursorcloud.Client
	// RepoURL overrides git remote lookup. Userinfo is stripped before use.
	RepoURL string
	// launches overrides the process-wide launch cap. Tests set it. Production
	// leaves it nil and uses cursorCloudLaunches.
	launches *cursorCloudLaunchGate
}

func (a *CursorCloudAgent) Name() string                       { return cursorCloudAgentName }
func (a *CursorCloudAgent) WithReasoning(ReasoningLevel) Agent { return a }
func (a *CursorCloudAgent) WithAgentic(bool) Agent             { return a }
func (a *CursorCloudAgent) WithModel(model string) Agent {
	c := *a
	c.Model = strings.TrimSpace(model)
	return &c
}

func (a *CursorCloudAgent) CommandLine() string {
	return "cursor-cloud (Cursor cloud agents API)"
}

func (a *CursorCloudAgent) Review(ctx context.Context, repoPath, commitSHA, prompt string, output io.Writer) (string, error) {
	return a.review(ctx, repoPath, commitSHA, prompt, nil, output)
}

func (a *CursorCloudAgent) ReviewWithSchema(ctx context.Context, repoPath, gitRef, prompt string, schema jsontext.Value, output io.Writer) (jsontext.Value, error) {
	raw, err := a.review(ctx, repoPath, gitRef, prompt, schema, output)
	if err != nil {
		return nil, err
	}
	return jsontext.Value(raw), nil
}

func (a *CursorCloudAgent) review(ctx context.Context, repoPath, commitSHA, prompt string, schema jsontext.Value, output io.Writer) (text string, err error) {
	if a == nil {
		a = &CursorCloudAgent{}
	}
	client, err := a.client()
	if err != nil {
		return "", MarkUnavailable(err)
	}
	sha, err := a.commitSHA(ctx, repoPath, commitSHA)
	if err != nil {
		return "", err
	}
	repoURL, err := a.repoURL(repoPath)
	if err != nil {
		return "", err
	}
	// Every cloud call runs under a deadline. withTimeout keeps one the
	// caller already set, and otherwise uses the host handoff bound.
	ctx, cancel := a.withTimeout(ctx)
	defer cancel()

	release, err := a.launchGate().acquire()
	if err != nil {
		return "", err
	}
	defer release()

	// The v0 HTTP body has no title field. Title is still set so the launch
	// record names the review. repoURL is already stripped of userinfo.
	created, err := client.CreateAgent(ctx, cursorcloud.CreateAgentRequest{
		Repo:   repoURL,
		Ref:    sha,
		Prompt: cloudPrompt(prompt, schema),
		Model:  a.Model,
		Title:  cursorCloudTitle(repoURL, sha),
	})
	if err != nil {
		return "", err
	}
	if output != nil {
		fmt.Fprintf(output, "cursor-cloud: created agent %s at %s\n", created.ID, sha)
	}
	// Delete even when the parent context is cancelled. A fresh bounded
	// context keeps the DELETE off that cancellation.
	defer func() {
		delErr := deleteCursorCloudAgent(client, created.ID, output)
		if delErr != nil && err != nil {
			err = errors.Join(err, delErr)
		}
	}()
	text, err = a.wait(ctx, client, created.ID)
	if err != nil {
		return "", MarkUnavailable(err)
	}
	return text, nil
}

func (a *CursorCloudAgent) client() (cursorcloud.Client, error) {
	if a.Client != nil {
		return a.Client, nil
	}
	if !cursorCloudKeySet() {
		return nil, &CursorCloudConfigError{}
	}
	return cursorcloud.NewFromEnv()
}

func (a *CursorCloudAgent) commitSHA(ctx context.Context, repoPath, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" || ref == "dirty" || strings.Contains(ref, "..") {
		return "", fmt.Errorf("cursor-cloud reviews one commit; %q is not a single commit", ref)
	}
	if isFullSHA(ref) {
		return ref, nil
	}
	sha, err := git.ResolveSHACtx(ctx, repoPath, ref)
	if err != nil {
		return "", fmt.Errorf("cursor-cloud: resolve %s: %w", ref, err)
	}
	if !isFullSHA(sha) {
		return "", fmt.Errorf("cursor-cloud: %s resolved to %q, not a commit SHA", ref, sha)
	}
	return sha, nil
}

func (a *CursorCloudAgent) repoURL(repoPath string) (string, error) {
	raw := strings.TrimSpace(a.RepoURL)
	if raw == "" {
		raw = git.GetRemoteURL(repoPath, "")
	}
	if raw == "" {
		return "", fmt.Errorf("cursor-cloud: repository %s has no git remote", repoPath)
	}
	// A credentialed remote (https://user:token@host/...) must not be sent to
	// Cursor or stored on the launch. Strip before the URL is used anywhere.
	return config.StripURLCredentials(raw), nil
}

func deleteCursorCloudAgent(client cursorcloud.Client, id string, output io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), cursorCloudDeleteTimeout)
	defer cancel()
	err := client.DeleteAgent(ctx, id)
	if err == nil {
		return nil
	}
	surfaced := fmt.Errorf("cursor-cloud: delete agent %s: %w", id, err)
	log.Printf("%s", surfaced.Error())
	if output != nil {
		fmt.Fprintln(output, surfaced.Error())
	}
	return surfaced
}

func cursorCloudTitle(repoURL, sha string) string {
	return "roborev review " + shortRepoName(repoURL) + " " + shortSHA(sha)
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func shortRepoName(repoURL string) string {
	name := strings.TrimSpace(repoURL)
	name = strings.TrimSuffix(name, ".git")
	name = strings.TrimRight(name, "/")
	if i := strings.LastIndexAny(name, "/:"); i >= 0 {
		name = name[i+1:]
	}
	if name == "" {
		return "repo"
	}
	return name
}

func (a *CursorCloudAgent) wait(ctx context.Context, client cursorcloud.Client, id string) (string, error) {
	ctx, cancel := a.withTimeout(ctx)
	defer cancel()
	interval := a.pollInterval()
	for {
		ag, err := client.GetAgent(ctx, id)
		if err != nil {
			return "", err
		}
		switch ag.Status {
		case cursorcloud.StatusFinished:
			return cloudFinalMessage(ctx, client, id)
		case cursorcloud.StatusError, cursorcloud.StatusExpired:
			return "", fmt.Errorf("cursor-cloud agent %s ended %s", id, ag.Status)
		default:
			// CREATING, and any status outside the contract, is still running.
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return "", fmt.Errorf("cursor-cloud: timed out waiting for agent %s: %w", id, ctx.Err())
			}
			return "", ctx.Err()
		case <-timer.C:
		}
		if interval < cursorCloudMaxPoll {
			interval *= 2
			if interval > cursorCloudMaxPoll {
				interval = cursorCloudMaxPoll
			}
		}
	}
}

func (a *CursorCloudAgent) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if a.Timeout > 0 {
		return context.WithTimeout(ctx, a.Timeout)
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	// Same bound as the file-handoff host when the caller set no deadline.
	return context.WithTimeout(ctx, defaultHostTimeout)
}

func (a *CursorCloudAgent) pollInterval() time.Duration {
	if a.PollInterval > 0 {
		return a.PollInterval
	}
	return defaultHostPollInterval
}

func cloudFinalMessage(ctx context.Context, client cursorcloud.Client, id string) (string, error) {
	text, err := client.FinalMessage(ctx, id)
	if err == nil {
		return text, nil
	}
	if noAssistantMessage(err) {
		return "", fmt.Errorf("cursor-cloud agent %s finished with no assistant message: %w", id, err)
	}
	return "", err
}

func noAssistantMessage(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no assistant message")
}

func cloudPrompt(prompt string, schema jsontext.Value) string {
	var b strings.Builder
	b.WriteString(cursorCloudSafety)
	b.WriteString("\n")
	b.WriteString(prompt)
	if len(schema) > 0 {
		b.WriteString("\n\nOutput exactly one JSON document that validates against this schema, with no prose and no code fences:\n")
		b.Write(schema)
	}
	return b.String()
}

func isFullSHA(ref string) bool {
	if len(ref) != 40 {
		return false
	}
	for _, c := range ref {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

func init() {
	Register(&CursorCloudAgent{})
}

var _ StructuredReviewAgent = (*CursorCloudAgent)(nil)
