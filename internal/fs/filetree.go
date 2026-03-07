package fs

import (
	"os"
	"path/filepath"
	"strings"
)

// TreeNode represents a file or directory in the tree.
type TreeNode struct {
	Name     string     `json:"name"`
	Path     string     `json:"path"`
	IsDir    bool       `json:"is_dir"`
	Children []TreeNode `json:"children,omitempty"`
}

// skipDirs contains directory names that should never be descended into.
var skipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	".build":       true,
	"__pycache__":  true,
}

// skipFiles contains exact file names that should never be exposed.
var skipFiles = map[string]bool{
	"id_rsa": true,
}

// hasSensitiveSuffix reports whether a filename ends with a sensitive extension.
func hasSensitiveSuffix(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".pem") || strings.HasSuffix(lower, ".key")
}

// BuildTree walks root up to maxDepth directory levels deep and returns the tree.
// maxDepth=3 means the root plus 2 more directory levels are visible
// (root → level-1 → level-2). Directories at level-2 are returned as nodes
// but their children are not expanded.
func BuildTree(root string, maxDepth int) (TreeNode, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return TreeNode{}, err
	}
	return buildNode(abs, 0, maxDepth)
}

// buildNode builds a TreeNode at the given path.
// depth is the current recursion depth (root = 0).
// A directory at depth d will only have its children expanded when d+1 < maxDepth,
// so directories at depth maxDepth-1 are leaves (no children populated).
func buildNode(path string, depth, maxDepth int) (TreeNode, error) {
	info, err := os.Stat(path)
	if err != nil {
		return TreeNode{}, err
	}

	node := TreeNode{
		Name:  info.Name(),
		Path:  path,
		IsDir: info.IsDir(),
	}

	// Do not descend further once we would exceed maxDepth.
	// depth+1 >= maxDepth means we are at the last visible level; listing
	// children here would add entries at depth maxDepth which is beyond the limit.
	if !info.IsDir() || depth+1 >= maxDepth {
		return node, nil
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		// Return the node without children on permission errors etc.
		return node, nil
	}

	for _, entry := range entries {
		name := entry.Name()

		// Skip all dotfiles and dotdirs (hidden entries incl. .git, .ssh, etc.)
		if strings.HasPrefix(name, ".") {
			continue
		}

		// Skip known large/irrelevant dirs
		if entry.IsDir() && skipDirs[name] {
			continue
		}

		// Skip sensitive files by exact name or extension
		if !entry.IsDir() && (skipFiles[name] || hasSensitiveSuffix(name)) {
			continue
		}

		childPath := filepath.Join(path, name)
		child, err := buildNode(childPath, depth+1, maxDepth)
		if err != nil {
			continue
		}
		node.Children = append(node.Children, child)
	}

	return node, nil
}
