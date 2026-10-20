package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/roborev/internal/cursorcloud"
	"go.kenn.io/roborev/internal/cursorcloud/cursorcloudtest"
)

func TestCursorCloudSnapshotReadPins(t *testing.T) {
	t.Run("symlinked roborev outside", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation needs extra privileges on Windows")
		}
		repo := t.TempDir()
		outside := t.TempDir()
		const secret = "OUTSIDE-ROBOREV-SNAPSHOT"
		require.NoError(t, os.WriteFile(filepath.Join(outside, "prompt.md"), []byte(secret), 0o600))
		require.NoError(t, os.Symlink(outside, filepath.Join(repo, ".roborev")))

		sent, creates, err := launchSnapshotRead(t, repo, filepath.Join(repo, ".roborev", "prompt.md"))

		require.Error(t, err)
		require.Equal(t, 0, creates)
		require.NotContains(t, sent, secret)
	})

	t.Run("proc self", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("/proc/self is linux-specific")
		}
		repo := t.TempDir()
		require.NoError(t, os.Symlink("/proc/self", filepath.Join(repo, ".roborev")))

		_, creates, err := launchSnapshotRead(t, repo, filepath.Join(repo, ".roborev", "environ"))

		require.Error(t, err)
		require.Equal(t, 0, creates)
	})

	t.Run("outside target", func(t *testing.T) {
		repo := t.TempDir()
		outside := t.TempDir()
		const secret = "OUTSIDE-SNAPSHOT-TARGET"
		target := filepath.Join(outside, "prompt.md")
		require.NoError(t, os.WriteFile(target, []byte(secret), 0o600))

		sent, creates, err := launchSnapshotRead(t, repo, target)

		require.Error(t, err)
		require.Equal(t, 0, creates)
		require.NotContains(t, sent, secret)
	})

	t.Run("traversal", func(t *testing.T) {
		parent := t.TempDir()
		repo := filepath.Join(parent, "repo")
		require.NoError(t, os.Mkdir(repo, 0o755))
		const secret = "TRAVERSAL-SNAPSHOT-SECRET"
		require.NoError(t, os.WriteFile(filepath.Join(parent, "secret.md"), []byte(secret), 0o600))
		target := repo + string(filepath.Separator) + ".." + string(filepath.Separator) + "secret.md"

		sent, creates, err := launchSnapshotRead(t, repo, target)

		require.Error(t, err)
		require.Equal(t, 0, creates)
		require.NotContains(t, sent, secret)
	})

	t.Run("legit", func(t *testing.T) {
		repo := t.TempDir()
		dir := filepath.Join(repo, ".roborev", "roborev-snapshot-legit")
		require.NoError(t, os.MkdirAll(dir, 0o755))
		const body = "LEGIT-IN-REPO-SNAPSHOT"
		target := filepath.Join(dir, "prompt.md")
		require.NoError(t, os.WriteFile(target, []byte(body), 0o600))

		sent, creates, err := launchSnapshotRead(t, repo, target)

		require.NoError(t, err)
		require.Equal(t, 1, creates)
		require.Contains(t, sent, body)
		require.NotContains(t, sent, target)
	})
}

func launchSnapshotRead(t *testing.T, repo, path string) (string, int, error) {
	t.Helper()
	const sha = "0123456789abcdef0123456789abcdef01234567"
	handoff := fmt.Sprintf("Read the complete task prompt from %q and carry out its instructions. Read the file in full before starting.\n", path)
	fake := &cursorcloudtest.Fake{CreateStatus: cursorcloud.StatusFinished, Final: "ok"}
	_, err := (&CursorCloudAgent{Client: fake, RepoURL: "https://github.com/acme/api.git"}).Review(
		context.Background(), repo, sha, handoff, nil,
	)
	var sent string
	for _, call := range fake.Calls() {
		if call.Method == cursorcloudtest.MethodCreate {
			sent = call.Request.Prompt
		}
	}
	return sent, createCount(fake), err
}
