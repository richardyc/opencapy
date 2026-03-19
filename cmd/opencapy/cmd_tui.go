package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/richardyc/opencapy/internal/project"
	"github.com/richardyc/opencapy/internal/session"
)

// ── styles ────────────────────────────────────────────────────────────────────

var (
	styleCursor  = lipgloss.NewStyle().Foreground(lipgloss.Color("#F5E6D3")).Background(lipgloss.Color("#7B5B3A")).Bold(true)
	styleHeader  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#888888"))
	styleDivider = lipgloss.NewStyle().Foreground(lipgloss.Color("#444444"))
	styleTitle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#F5E6D3"))
	styleNew     = lipgloss.NewStyle().Foreground(lipgloss.Color("#7B5B3A")).Bold(true)
	styleHelp    = lipgloss.NewStyle().Foreground(lipgloss.Color("#666666"))
	styleConfirm = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF6B6B")).Bold(true)
)

// ── model ────────────────────────────────────────────────────────────────────

type tuiModel struct {
	sessions []session.SessionInfo
	reg      *project.Registry
	cwd      string
	cwdBase  string
	cursor   int
	confirm  int // index of session pending kill confirmation, -1 = none
	action   func() error
	width    int
	height   int
}

// total rows = len(sessions) + 1 (new session row)
func (m tuiModel) totalRows() int { return len(m.sessions) + 1 }

// newRow is the index of the "[+] new session here" row
func (m tuiModel) newRow() int { return len(m.sessions) }

func (m tuiModel) Init() tea.Cmd { return nil }

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case tea.KeyMsg:
		// Kill confirmation mode
		if m.confirm >= 0 {
			switch msg.String() {
			case "y", "Y":
				idx := m.confirm
				m.confirm = -1
				name := m.sessions[idx].Name
				m.action = func() error {
					if err := session.KillSession(name); err != nil {
						return fmt.Errorf("kill session: %w", err)
					}
					reg, err := project.Load()
					if err == nil {
						_ = reg.Unregister(name)
					}
					fmt.Printf("Killed session %q\n", name)
					return nil
				}
				return m, tea.Quit
			case "n", "N", "esc":
				m.confirm = -1
			}
			return m, nil
		}

		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit

		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}

		case "down", "j":
			if m.cursor < m.totalRows()-1 {
				m.cursor++
			}

		case "enter", " ":
			if m.cursor == m.newRow() {
				// Create new session
				name, cwd := m.cwdBase, m.cwd
				m.action = func() error {
					return createSession(name, cwd, defaultShell(), nil)
				}
			} else {
				name := m.sessions[m.cursor].Name
				m.action = func() error {
					cols, rows := session.GetTerminalSize()
					return session.Attach(name, rows, cols)
				}
			}
			return m, tea.Quit

		case "n":
			name, cwd := m.cwdBase, m.cwd
			m.action = func() error {
				return createSession(name, cwd, defaultShell(), nil)
			}
			return m, tea.Quit

		case "d", "x":
			if m.cursor < len(m.sessions) {
				m.confirm = m.cursor
			}
		}
	}
	return m, nil
}

func (m tuiModel) View() string {
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString("  " + styleTitle.Render("opencapy") + "\n\n")

	// Column widths
	nameW := 20
	pathW := 28
	createdW := 14
	statusW := 12

	// Header
	header := fmt.Sprintf("  %-*s  %-*s  %-*s  %-*s",
		nameW, "SESSION",
		pathW, "PATH",
		createdW, "CREATED",
		statusW, "STATUS",
	)
	b.WriteString(styleHeader.Render(header) + "\n")

	divider := "  " + strings.Repeat("─", nameW+pathW+createdW+statusW+8)
	b.WriteString(styleDivider.Render(divider) + "\n")

	// Session rows
	for i, s := range m.sessions {
		path := s.Cwd
		if m.reg != nil {
			if p, ok := m.reg.GetProject(s.Name); ok {
				path = p
			}
		}
		path = shortenPath(path)

		created := humanDuration(time.Since(s.CreatedAt))
		status := "alive"
		if !s.Alive {
			status = "dead"
		}

		name := truncate(s.Name, nameW)
		pathStr := truncate(path, pathW)

		line := fmt.Sprintf("%-*s  %-*s  %-*s  %-*s",
			nameW, name,
			pathW, pathStr,
			createdW, created,
			statusW, status,
		)

		if m.confirm == i {
			b.WriteString(styleConfirm.Render(fmt.Sprintf("▶ Kill %q? (y/n) ", s.Name)) + "\n")
			continue
		}

		if m.cursor == i {
			b.WriteString(styleCursor.Render("▶ "+line) + "\n")
		} else {
			b.WriteString("  " + line + "\n")
		}
	}

	// New session row
	newLine := fmt.Sprintf("[+] new session here  (%s)", m.cwd)
	if m.cursor == m.newRow() {
		b.WriteString(styleCursor.Render("▶ "+newLine) + "\n")
	} else {
		b.WriteString("  " + styleNew.Render(newLine) + "\n")
	}

	b.WriteString("\n")

	// Pairing hint when no sessions exist yet
	if len(m.sessions) == 0 {
		b.WriteString("\n")
		b.WriteString(styleHelp.Render("  No sessions yet — press Enter to create one") + "\n")
		b.WriteString(styleHelp.Render("  To pair your iPhone: run  opencapy qr  in another terminal") + "\n")
	}

	// Help bar
	help := "  ↑↓/kj navigate   enter attach   d kill   n new   q quit"
	b.WriteString(styleHelp.Render(help) + "\n")

	return b.String()
}

// ── helpers ───────────────────────────────────────────────────────────────────

func shortenPath(p string) string {
	home, err := os.UserHomeDir()
	if err == nil && strings.HasPrefix(p, home) {
		p = "~" + p[len(home):]
	}
	return p
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func humanDuration(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// ── entry point ───────────────────────────────────────────────────────────────

// runTUI launches the full-screen session manager and executes the chosen action.
func runTUI(cwd string) error {
	sessions, _ := session.ListSessions()
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].CreatedAt.After(sessions[j].CreatedAt)
	})
	reg, _ := project.Load()

	// Auto-increment name for "new session here"
	base := filepath.Base(cwd)
	name := base
	for i := 2; ; i++ {
		if !session.SessionExists(name) {
			break
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}

	m := tuiModel{
		sessions: sessions,
		reg:      reg,
		cwd:      cwd,
		cwdBase:  name,
		cursor:   0,
		confirm:  -1,
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	result, err := p.Run()
	if err != nil {
		return err
	}

	final, ok := result.(tuiModel)
	if !ok || final.action == nil {
		return nil
	}
	return final.action()
}
