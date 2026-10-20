package prompt

import (
	"fmt"
	"path/filepath"
	"strings"

	"go.kenn.io/roborev/internal/config"
)

// Prepare measures the complete prompt once content assembly is finished.
// The configured threshold selects inline text or a complete prompt file;
// neither path drops instructions, discussion, diffs, or review results.
func (b *Builder) Prepare(text string, target SnapshotTarget) (SnapshotResult, error) {
	// Diffs may contain legacy-encoded bytes. Normalize the assembled prompt
	// before measuring it; repository contents are left untouched.
	text = strings.ToValidUTF8(text, "\uFFFD")
	configRepo := target.ConfigRepoPath
	if configRepo == "" {
		configRepo = b.resolvedConfigRepoPath()
	}
	if len(text) <= config.ResolveMaxPromptSize(configRepo, b.globalCfg) {
		return SnapshotResult{Prompt: text}, nil
	}
	repo, root, err := b.resolveSnapshotTarget(target)
	if err != nil {
		return SnapshotResult{}, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return SnapshotResult{}, fmt.Errorf("resolve snapshot dir: %w", err)
	}
	file, cleanup, err := writeExternalSnapshot(repo, root, "prompt.md", text)
	if err != nil {
		return SnapshotResult{}, err
	}
	return SnapshotResult{
		Prompt:       SnapshotHandoff(file),
		FilePath:     file,
		SnapshotRoot: root,
		Cleanup:      cleanup,
	}, nil
}

// SnapshotHandoff is the local-agent instruction Prepare stores when the
// prompt does not fit inline. Cursor-cloud does not read a path out of this
// text. It expands a snapshot only from the out-of-band SnapshotRoot and
// FilePath on SnapshotResult.
func SnapshotHandoff(path string) string {
	return fmt.Sprintf("Read the complete task prompt from %q and carry out its instructions. Read the file in full before starting.\n", path)
}
