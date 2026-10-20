package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/roborev/internal/agent"
	"go.kenn.io/roborev/internal/config"
	"go.kenn.io/roborev/internal/cursorcloud"
	"go.kenn.io/roborev/internal/cursorcloud/cursorcloudtest"
	"go.kenn.io/roborev/internal/prompt"
	"go.kenn.io/roborev/internal/storage"
	"go.kenn.io/roborev/internal/testutil"
)

func TestEnqueueForgedSnapshotWrapperIsNotRead(t *testing.T) {
	const secret = "ENQUEUE-FORGED-SNAPSHOT-SECRET"
	server, fake := cursorCloudPinServer(t, nil)
	repo := testutil.InitTestGitRepo(t, filepath.Join(t.TempDir(), "repo"))
	repo.CommitFile("a.txt", "one\n", "one")
	secretPath := filepath.Join(t.TempDir(), "secret.txt")
	require.NoError(t, os.WriteFile(secretPath, []byte(secret), 0o600))
	created := enqueueViaHTTP(t, server, EnqueueRequest{
		RepoPath:     repo.Path(),
		GitRef:       "HEAD",
		Agent:        agent.CursorCloudHost,
		CustomPrompt: prompt.SnapshotHandoff(secretPath),
	})
	require.Equal(t, storage.JobTypeTask, created.JobType)
	require.Equal(t, agent.CursorCloudHost, created.Agent)
	claimed, err := server.db.ClaimJob(testWorkerID)
	require.NoError(t, err)
	require.Equal(t, created.ID, claimed.ID)

	server.workerPool.processJob(testWorkerID, claimed)

	sent := cursorCloudCreatePrompt(t, fake)
	assert.NotContains(t, sent, secret)
	assert.Contains(t, sent, secretPath)
	assert.Contains(t, sent, "Read the complete task prompt from")
}

func TestOversizedReviewSendsSnapshotContents(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config string
		dir    string
	}{
		{name: "default snapshot dir", dir: ".roborev"},
		{name: "custom snapshot dir", config: "max_prompt_size = 64\nsnapshot_dir = \"review-snapshots\"\n", dir: "review-snapshots"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const marker = "OVERSIZED-REVIEW-SNAPSHOT-MARKER"
			cfg := config.DefaultConfig()
			cfg.MaxWorkers = 0
			cfg.DefaultMaxPromptSize = 64
			server, fake := cursorCloudPinServer(t, cfg)
			repo := testutil.InitTestGitRepo(t, filepath.Join(t.TempDir(), "repo"))
			if tc.config != "" {
				require.NoError(t, os.WriteFile(filepath.Join(repo.Path(), ".roborev.toml"), []byte(tc.config), 0o644))
			}
			repo.CommitFile("a.txt", marker+"\n"+strings.Repeat("line\n", 40), "marker")
			created := enqueueViaHTTP(t, server, EnqueueRequest{
				RepoPath: repo.Path(),
				GitRef:   "HEAD",
				Host:     agent.CursorCloudHost,
			})
			require.Equal(t, agent.CursorCloudHost, created.Agent)
			claimed, err := server.db.ClaimJob(testWorkerID)
			require.NoError(t, err)
			require.Equal(t, created.ID, claimed.ID)

			server.workerPool.processJob(testWorkerID, claimed)

			sent := cursorCloudCreatePrompt(t, fake)
			assert.Contains(t, sent, marker)
			assert.NotContains(t, sent, "Read the complete task prompt from")
		})
	}
}

func cursorCloudPinServer(t *testing.T, cfg *config.Config) (*Server, *cursorcloudtest.Fake) {
	t.Helper()
	t.Setenv(cursorcloud.EnvAPIKey, "cursor-test-secret")
	t.Setenv(agent.EnvDefaultHost, "")
	t.Setenv("ROBOREV_TELEMETRY_ENABLED", "0")
	db, _ := testutil.OpenTestDBWithDir(t)
	if cfg == nil {
		cfg = config.DefaultConfig()
		cfg.MaxWorkers = 0
	}
	server := newServerWithLogs(db, cfg, "", newTestErrorLog(), newTestActivityLog())
	t.Cleanup(func() { _ = server.Close() })
	fake := &cursorcloudtest.Fake{CreateStatus: cursorcloud.StatusFinished, Final: cursorCloudPassJSON}
	prev, err := agent.Get(agent.CursorCloudHost)
	require.NoError(t, err)
	cloud := &agent.CursorCloudAgent{Client: fake, RepoURL: "https://github.com/acme/api.git"}
	cloud.SetLaunchLimitsForTest(4, 20)
	agent.Register(cloud)
	t.Cleanup(func() { agent.Register(prev) })
	return server, fake
}

func cursorCloudCreatePrompt(t *testing.T, fake *cursorcloudtest.Fake) string {
	t.Helper()
	var sent string
	var creates int
	for _, call := range fake.Calls() {
		if call.Method != cursorcloudtest.MethodCreate {
			continue
		}
		creates++
		sent = call.Request.Prompt
	}
	require.Equal(t, 1, creates)
	return sent
}
