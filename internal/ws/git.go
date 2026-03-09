package ws

import (
	"bytes"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// GitFileStatus represents the status of a single file in a git repo.
type GitFileStatus struct {
	Path     string `json:"path"`
	Staged   string `json:"staged"`   // 'M','A','D','R',' '
	Unstaged string `json:"unstaged"` // 'M','D','?',' '
}

// GitStatusResult is the response for git_status and mutation operations.
type GitStatusResult struct {
	Session string          `json:"session"`
	Branch  string          `json:"branch"`
	Ahead   int             `json:"ahead"`
	Behind  int             `json:"behind"`
	Files   []GitFileStatus `json:"files"`
	OK      bool            `json:"ok"`
	Error   string          `json:"error,omitempty"`
}

// GitDiffResult is the response for git_diff.
type GitDiffResult struct {
	Path   string `json:"path"`
	Before string `json:"before"`
	After  string `json:"after"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

// runGit runs a git command in dir and returns combined stdout.
func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

// parseGitStatus runs `git status --porcelain=v1 -b` in dir and parses the output.
func parseGitStatus(dir string) GitStatusResult {
	out, err := runGit(dir, "status", "--porcelain=v1", "-b")
	if err != nil {
		return GitStatusResult{OK: false, Error: err.Error()}
	}

	result := GitStatusResult{OK: true, Files: []GitFileStatus{}}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")

	for _, line := range lines {
		if len(line) == 0 {
			continue
		}

		// Branch line: ## main...origin/main [ahead 1, behind 2]
		if strings.HasPrefix(line, "## ") {
			branch := strings.TrimPrefix(line, "## ")
			// Strip tracking info
			if idx := strings.Index(branch, "..."); idx != -1 {
				tracking := branch[idx+3:]
				branch = branch[:idx]
				// Parse ahead/behind
				if ahead := parseCount(tracking, "ahead "); ahead > 0 {
					result.Ahead = ahead
				}
				if behind := parseCount(tracking, "behind "); behind > 0 {
					result.Behind = behind
				}
			}
			// Handle "No commits yet"
			if strings.HasPrefix(branch, "No commits yet on ") {
				branch = strings.TrimPrefix(branch, "No commits yet on ")
			}
			result.Branch = branch
			continue
		}

		// Status line: XY path (or XY oldpath -> newpath for renames)
		if len(line) < 3 {
			continue
		}
		staged := string(line[0])
		unstaged := string(line[1])
		path := line[3:]

		// Handle renames: "R  oldname -> newname" — use new name
		if staged == "R" || unstaged == "R" {
			if idx := strings.Index(path, " -> "); idx != -1 {
				path = path[idx+4:]
			}
		}

		result.Files = append(result.Files, GitFileStatus{
			Path:     path,
			Staged:   staged,
			Unstaged: unstaged,
		})
	}

	return result
}

// parseCount extracts an integer after a keyword like "ahead " from a string like "[ahead 3, behind 1]".
func parseCount(s, keyword string) int {
	idx := strings.Index(s, keyword)
	if idx == -1 {
		return 0
	}
	rest := s[idx+len(keyword):]
	end := strings.IndexAny(rest, ", ]")
	if end == -1 {
		end = len(rest)
	}
	n, _ := strconv.Atoi(rest[:end])
	return n
}

// gitFileContent returns the content of a file at a given git ref (e.g. "HEAD", ":").
// Returns empty string if the ref doesn't exist (new file).
func gitFileContent(dir, ref, path string) string {
	out, err := runGit(dir, "show", ref+":"+path)
	if err != nil {
		return ""
	}
	return out
}

// gitDiff returns before/after content for a file.
// staged=false: before=HEAD, after=disk; staged=true: before=HEAD, after=index
func gitDiff(dir, path string, staged bool) GitDiffResult {
	before := gitFileContent(dir, "HEAD", path)

	var after string
	if staged {
		after = gitFileContent(dir, ":", path)
	} else {
		full := dir + "/" + path
		data, err := os.ReadFile(full)
		if err != nil {
			return GitDiffResult{Path: path, OK: false, Error: err.Error()}
		}
		after = string(data)
	}

	return GitDiffResult{Path: path, Before: before, After: after, OK: true}
}
