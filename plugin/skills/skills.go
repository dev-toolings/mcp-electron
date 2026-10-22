// Package skills embeds the agent skill shipped with the Claude Code plugin
// (plugin/skills/mcp-electron) so `mcp-electron install` writes the same
// files for Claude Code and Codex users who do not use the plugin.
package skills

import (
	"embed"
	"io/fs"
	"strings"
)

const root = "mcp-electron"

//go:embed mcp-electron/SKILL.md mcp-electron/references/*.md
var tree embed.FS

// Files returns every file of the skill keyed by its path relative to the
// skill root (SKILL.md, references/…).
func Files() map[string]string {
	out := map[string]string{}
	_ = fs.WalkDir(tree, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		data, err := tree.ReadFile(path)
		if err != nil {
			return nil
		}
		out[strings.TrimPrefix(path, root+"/")] = string(data)
		return nil
	})
	return out
}
