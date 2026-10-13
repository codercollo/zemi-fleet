package clock

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The rule from CLAUDE.md "Time handling": only this package may read the system clock.
// Everything else asks an injected Clock, so tests can control time and every timestamp is UTC.
func TestNoTimeNowOutsideClockPackage(t *testing.T) {
	root := filepath.Join("..", "..")
	var offenders []string

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "bin", "node_modules":
				return fs.SkipDir
			}
			if filepath.Clean(path) == filepath.Join(root, "internal", "clock") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "rule_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(src), "time.Now(") {
			offenders = append(offenders, path)
		}
		return nil
	})

	require.NoError(t, err)
	assert.Empty(t, offenders, "use an injected clock.Clock instead of time.Now()")
}
