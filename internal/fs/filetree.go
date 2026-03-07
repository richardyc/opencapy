package fs

import (
	"os"
	"path/filepath"
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
	".git":          true,
	"node_modules":  true,
	".build":        true,
	"__pycache__":   true,
}

// BuildTree walks root up to maxDepth levels deep and returns the tree.
// Depth 0 = root node only, depth 1 = root + immediate children, etc.
func BuildTree(root string, maxDepth int) (TreeNode, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return TreeNode{}, err
	}
	return buildNode(abs, 0, maxDepth)
}

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

	// Stop descending when we're already at depth maxDepth-1 so that
	// entries at depth maxDepth are never added to the tree.
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

		// Skip hidden entries and known large/irrelevant dirs
		if name == "" {
			continue
		}
		if entry.IsDir() && skipDirs[name] {
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
