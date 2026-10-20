package prompt

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSnapshotWritePins(t *testing.T) {
	const body = "SNAPSHOT-BODY-MUST-STAY-IN-REPO"

	t.Run("symlinked roborev outside", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation needs extra privileges on Windows")
		}
		repoPath, commits := setupTestRepo(t)
		outside := t.TempDir()
		require.NoError(t, os.Symlink(outside, filepath.Join(repoPath, ".roborev")))
		builder := NewBuilder(nil).ForRepo(repoPath, 0)

		file, cleanup, err := builder.WriteDiffSnapshot(commits[len(commits)-1], nil)

		require.Error(t, err)
		require.ErrorContains(t, err, "symlink")
		require.Empty(t, file)
		require.Nil(t, cleanup)
		entries, readErr := os.ReadDir(outside)
		require.NoError(t, readErr)
		require.Empty(t, entries)
	})

	t.Run("proc self", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("/proc/self is linux-specific")
		}
		repoPath, commits := setupTestRepo(t)
		require.NoError(t, os.Symlink("/proc/self", filepath.Join(repoPath, ".roborev")))
		builder := NewBuilder(nil).ForRepo(repoPath, 0)

		file, cleanup, err := builder.WriteDiffSnapshot(commits[len(commits)-1], nil)

		require.Error(t, err)
		require.ErrorContains(t, err, "symlink")
		require.Empty(t, file)
		require.Nil(t, cleanup)
	})

	t.Run("outside target", func(t *testing.T) {
		repoPath, _ := setupTestRepo(t)
		outside := t.TempDir()

		file, cleanup, err := writeExternalSnapshot(repoPath, outside, "prompt.md", body)

		require.Error(t, err)
		require.ErrorContains(t, err, "repo root")
		require.Empty(t, file)
		require.Nil(t, cleanup)
		_, statErr := os.Stat(filepath.Join(outside, "prompt.md"))
		require.ErrorIs(t, statErr, os.ErrNotExist)
	})

	t.Run("traversal", func(t *testing.T) {
		repoPath, _ := setupTestRepo(t)
		escaped := repoPath + string(filepath.Separator) + ".." + string(filepath.Separator) + "snap-escape"

		file, cleanup, err := writeExternalSnapshot(repoPath, escaped, "prompt.md", body)

		require.Error(t, err)
		require.ErrorContains(t, err, "repo root")
		require.Empty(t, file)
		require.Nil(t, cleanup)
		_, statErr := os.Stat(filepath.Join(filepath.Dir(repoPath), "snap-escape"))
		require.ErrorIs(t, statErr, os.ErrNotExist)
	})

	t.Run("legit", func(t *testing.T) {
		repoPath, commits := setupTestRepo(t)
		builder := NewBuilder(nil).ForRepo(repoPath, 0)

		file, cleanup, err := builder.WriteDiffSnapshot(commits[len(commits)-1], nil)

		require.NoError(t, err)
		require.NotNil(t, cleanup)
		t.Cleanup(cleanup)
		require.Contains(t, file, filepath.Join(repoPath, ".roborev")+string(filepath.Separator))
		got, readErr := os.ReadFile(file)
		require.NoError(t, readErr)
		require.NotEmpty(t, got)
	})
}
