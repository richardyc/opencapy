package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

const shellSentinelStart = "# >>> opencapy >>>"
const shellSentinelEnd = "# <<< opencapy <<<"

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init <shell>",
		Short: "Print shell integration code for the given shell (zsh, bash, fish)",
		Long: `Prints the shell function that wraps 'claude' with opencapy monitoring.

Typical usage — add one of these to your shell config:

  zsh:  eval "$(opencapy init zsh)"   # add to ~/.zshrc
  bash: eval "$(opencapy init bash)"  # add to ~/.bash_profile
  fish: opencapy init fish | source   # add to ~/.config/fish/conf.d/opencapy.fish

Or just run 'opencapy install' to do this automatically.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			code, err := shellInitCode(args[0])
			if err != nil {
				return err
			}
			fmt.Print(code)
			return nil
		},
	}
}

// shellInitCode returns the shell function snippet for the given shell.
// The function captures the git branch and delegates to `opencapy shim`.
func shellInitCode(shell string) (string, error) {
	switch shell {
	case "zsh", "bash":
		return `function claude() {
  local _branch
  _branch=$(git branch --show-current 2>/dev/null)
  OPENCAPY_GIT_BRANCH="$_branch" opencapy shim "$@"
}
`, nil
	case "fish":
		return `function claude
    set -lx OPENCAPY_GIT_BRANCH (git branch --show-current 2>/dev/null)
    opencapy shim $argv
end
`, nil
	default:
		return "", fmt.Errorf("unsupported shell %q — supported: zsh, bash, fish", shell)
	}
}

// injectShellIntegration detects the user's shell and injects the opencapy
// eval line into the appropriate RC files. Idempotent via sentinel comments.
func injectShellIntegration() {
	home, _ := os.UserHomeDir()
	switch detectShell() {
	case "fish":
		injectFish(home)
	case "bash":
		injectBash(home)
	default: // zsh and anything else
		injectEval(filepath.Join(home, ".zshrc"), "zsh")
	}
}

// detectShell returns the basename of the user's default shell.
func detectShell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return filepath.Base(sh)
	}
	return "zsh"
}

// injectEval appends `eval "$(opencapy init <shell>)"` wrapped in sentinel
// comments to rcPath. Idempotent — skips if the sentinel is already present.
func injectEval(rcPath, shell string) {
	if alreadyInjected(rcPath) {
		fmt.Printf("✓ shell integration already present in %s\n", rcPath)
		return
	}
	block := fmt.Sprintf("\n%s\neval \"$(opencapy init %s)\"\n%s\n", shellSentinelStart, shell, shellSentinelEnd)
	f, err := os.OpenFile(rcPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not write to %s: %v\n", rcPath, err)
		return
	}
	defer f.Close()
	fmt.Fprint(f, block)
	fmt.Printf("✓ shell integration added to %s\n", rcPath)
	fmt.Printf("  Open a new terminal, or run: source %s\n", rcPath)
}

// injectBash handles the macOS bash trap: Terminal.app opens login shells which
// load ~/.bash_profile, not ~/.bashrc. Write to both files that already exist,
// or create ~/.bash_profile if neither exists.
func injectBash(home string) {
	profile := filepath.Join(home, ".bash_profile")
	bashrc := filepath.Join(home, ".bashrc")
	wrote := false
	for _, path := range []string{profile, bashrc} {
		if _, err := os.Stat(path); err == nil {
			injectEval(path, "bash")
			wrote = true
		}
	}
	if !wrote {
		injectEval(profile, "bash")
	}
}

// injectFish writes the claude function to fish's autoload directory.
// Fish auto-loads ~/.config/fish/functions/claude.fish on first invocation.
func injectFish(home string) {
	funcDir := filepath.Join(home, ".config", "fish", "functions")
	if err := os.MkdirAll(funcDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not create fish functions dir: %v\n", err)
		return
	}
	funcPath := filepath.Join(funcDir, "claude.fish")
	code, _ := shellInitCode("fish")
	content := shellSentinelStart + "\n" + code + shellSentinelEnd + "\n"
	if err := os.WriteFile(funcPath, []byte(content), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not write %s: %v\n", funcPath, err)
		return
	}
	fmt.Printf("✓ fish function written to %s\n", funcPath)
}

// ── Claude Code hooks ──────────────────────────────────────────────────────

const claudeHooksURL = "http://localhost:7242/hooks/claude"
const claudeHookCmd = "curl -sf -X POST " + claudeHooksURL + " -H 'Content-Type: application/json' -d @- || true"

// injectClaudeHooks merges opencapy's hooks into ~/.claude/settings.json.
// Uses curl + async:true so claude is never blocked if the daemon is slow.
// Idempotent — skips if the URL is already present.
func injectClaudeHooks() {
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".claude", "settings.json")

	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return
	}

	// Skip if already configured.
	if strings.Contains(string(data), claudeHooksURL) {
		fmt.Println("✓ Claude Code hooks already configured")
		return
	}

	var settings map[string]interface{}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &settings); err != nil {
			// JSONC or unparseable — print manual instructions.
			fmt.Println("\n  Claude Code hooks — add manually to ~/.claude/settings.json:")
			printHooksSnippet()
			return
		}
	} else {
		settings = make(map[string]interface{})
	}

	hook := map[string]interface{}{
		"hooks": []interface{}{
			map[string]interface{}{
				"type":    "command",
				"command": claudeHookCmd,
				"async":   true,
			},
		},
	}

	hooks, _ := settings["hooks"].(map[string]interface{})
	if hooks == nil {
		hooks = make(map[string]interface{})
	}
	for _, event := range []string{"PermissionRequest", "Stop", "PostToolUse"} {
		existing, _ := hooks[event].([]interface{})
		hooks[event] = append(existing, hook)
	}
	settings["hooks"] = hooks

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "  warning: could not write %s: %v\n", path, err)
		return
	}
	fmt.Println("✓ Claude Code hooks configured (~/.claude/settings.json)")
}

// removeClaudeHooks removes opencapy's hooks from ~/.claude/settings.json.
func removeClaudeHooks() {
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".claude", "settings.json")

	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if !strings.Contains(string(data), claudeHooksURL) {
		return // not present
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return // JSONC, skip
	}

	hooks, _ := settings["hooks"].(map[string]interface{})
	if hooks == nil {
		return
	}

	modified := false
	for event, val := range hooks {
		groups, _ := val.([]interface{})
		var filtered []interface{}
		for _, g := range groups {
			group, _ := g.(map[string]interface{})
			inner, _ := group["hooks"].([]interface{})
			var kept []interface{}
			for _, h := range inner {
				hm, _ := h.(map[string]interface{})
				if cmd, _ := hm["command"].(string); !strings.Contains(cmd, claudeHooksURL) {
					kept = append(kept, h)
				} else {
					modified = true
				}
			}
			if len(kept) > 0 {
				group["hooks"] = kept
				filtered = append(filtered, group)
			} else if len(kept) == 0 && len(inner) > 0 {
				modified = true // entire group removed
			}
		}
		if len(filtered) > 0 {
			hooks[event] = filtered
		} else {
			delete(hooks, event)
		}
	}
	if len(hooks) == 0 {
		delete(settings, "hooks")
	} else {
		settings["hooks"] = hooks
	}

	if !modified {
		return
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, append(out, '\n'), 0o644)
	fmt.Println("✓ Claude Code hooks removed from ~/.claude/settings.json")
}

func printHooksSnippet() {
	fmt.Printf(`
  {
    "hooks": {
      "PermissionRequest": [{"hooks": [{"type": "command", "command": "%s", "async": true}]}],
      "Stop":              [{"hooks": [{"type": "command", "command": "%s", "async": true}]}],
      "PostToolUse":       [{"hooks": [{"type": "command", "command": "%s", "async": true}]}]
    }
  }
`, claudeHookCmd, claudeHookCmd, claudeHookCmd)
}

// alreadyInjected returns true if the sentinel block is already in the file.
func alreadyInjected(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), shellSentinelStart)
}

// removeShellIntegration removes the opencapy sentinel block from all shell
// config files and the fish function file.
func removeShellIntegration(home string) {
	removed := false
	// zsh / bash RC files
	for _, p := range []string{
		filepath.Join(home, ".zshrc"),
		filepath.Join(home, ".bash_profile"),
		filepath.Join(home, ".bashrc"),
	} {
		if removeSentinelBlock(p) {
			fmt.Printf("✓ Shell hook removed from %s\n", p)
			removed = true
		}
	}
	// fish function file
	fishFunc := filepath.Join(home, ".config", "fish", "functions", "claude.fish")
	if _, err := os.Stat(fishFunc); err == nil {
		os.Remove(fishFunc)
		fmt.Printf("✓ Fish function removed (%s)\n", fishFunc)
		removed = true
	}
	if !removed {
		fmt.Println("  No shell hook found (already clean)")
	}
}

// removeSentinelBlock removes the >>> opencapy >>> ... <<< opencapy <<< block
// from a shell RC file. Returns true if something was removed.
func removeSentinelBlock(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	content := string(data)
	start := strings.Index(content, shellSentinelStart)
	if start == -1 {
		return false
	}
	end := strings.Index(content, shellSentinelEnd)
	if end == -1 {
		return false
	}
	end += len(shellSentinelEnd)
	// Trim the surrounding newline so we don't leave a blank line.
	if start > 0 && content[start-1] == '\n' {
		start--
	}
	if end < len(content) && content[end] == '\n' {
		end++
	}
	cleaned := content[:start] + content[end:]
	return os.WriteFile(path, []byte(cleaned), 0o644) == nil
}
