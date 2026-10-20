package git

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfineSnapshotPathsPins(t *testing.T) {
	t.Run("symlinked roborev outside", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation needs extra privileges on Windows")
		}
		repo := t.TempDir()
		outside := t.TempDir()
		require.NoError(t, os.Symlink(outside, filepath.Join(repo, ".roborev")))
		target := filepath.Join(repo, ".roborev", "prompt.md")

		err := ConfineSnapshotPaths(repo, filepath.Dir(target), target)

		require.Error(t, err)
		require.ErrorContains(t, err, "symlink")
	})

	t.Run("proc self", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("/proc/self is linux-specific")
		}
		repo := t.TempDir()
		require.NoError(t, os.Symlink("/proc/self", filepath.Join(repo, ".roborev")))
		target := filepath.Join(repo, ".roborev", "environ")

		err := ConfineSnapshotPaths(repo, filepath.Dir(target), target)

		require.Error(t, err)
		require.ErrorContains(t, err, "symlink")
	})

	t.Run("outside target", func(t *testing.T) {
		repo := t.TempDir()
		outside := filepath.Join(t.TempDir(), "prompt.md")

		err := ConfineSnapshotPaths(repo, filepath.Dir(outside), outside)

		require.Error(t, err)
		require.ErrorContains(t, err, "repo root")
	})

	t.Run("traversal", func(t *testing.T) {
		repo := t.TempDir()
		target := repo + string(filepath.Separator) + ".." + string(filepath.Separator) + "prompt.md"

		err := ConfineSnapshotPaths(repo, filepath.Dir(target), target)

		require.Error(t, err)
		require.ErrorContains(t, err, "repo root")
	})

	t.Run("legit", func(t *testing.T) {
		repo := t.TempDir()
		dir := filepath.Join(repo, ".roborev", "roborev-snapshot-legit")
		require.NoError(t, os.MkdirAll(dir, 0o755))
		target := filepath.Join(dir, "prompt.md")
		require.NoError(t, os.WriteFile(target, []byte("legit snapshot\n"), 0o600))

		require.NoError(t, ConfineSnapshotPaths(repo, dir, target))
	})
}
