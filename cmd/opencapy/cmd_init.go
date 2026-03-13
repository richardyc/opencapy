package main

import (
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

// alreadyInjected returns true if the sentinel block is already in the file.
func alreadyInjected(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), shellSentinelStart)
}
