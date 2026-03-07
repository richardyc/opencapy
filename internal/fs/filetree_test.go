package fs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildTree(t *testing.T) {
	// Create a temp directory structure:
	//   root/
	//     a.txt
	//     subdir/
	//       b.txt
	//       deep/
	//         c.txt
	//           deeper/       ← beyond maxDepth=3, should not appear
	//             d.txt
	root := t.TempDir()

	writeFile := func(path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	writeFile(filepath.Join(root, "a.txt"))
	writeFile(filepath.Join(root, "subdir", "b.txt"))
	writeFile(filepath.Join(root, "subdir", "deep", "c.txt"))
	writeFile(filepath.Join(root, "subdir", "deep", "deeper", "d.txt"))

	// Also add a skipped dir to verify it is excluded
	if err := os.MkdirAll(filepath.Join(root, "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}

	tree, err := BuildTree(root, 3)
	if err != nil {
		t.Fatalf("BuildTree: %v", err)
	}

	// Root should be a dir
	if !tree.IsDir {
		t.Error("root should be a directory")
	}

	// Find subdir
	var subdir *TreeNode
	for i := range tree.Children {
		if tree.Children[i].Name == "subdir" {
			subdir = &tree.Children[i]
			break
		}
	}
	if subdir == nil {
		t.Fatal("expected subdir in root children")
	}

	// Find deep inside subdir
	var deep *TreeNode
	for i := range subdir.Children {
		if subdir.Children[i].Name == "deep" {
			deep = &subdir.Children[i]
			break
		}
	}
	if deep == nil {
		t.Fatal("expected deep in subdir children")
	}

	// deep/deeper should NOT appear (depth 3 = root->subdir->deep, we're at max so no children)
	for _, child := range deep.Children {
		if child.Name == "deeper" {
			t.Error("deeper dir should not appear: exceeds maxDepth=3")
		}
	}

	// node_modules should NOT appear anywhere at root level
	for _, child := range tree.Children {
		if child.Name == "node_modules" {
			t.Error("node_modules should be skipped")
		}
	}
}
