module github.com/lusky3/agentsync-tui

go 1.26.0

toolchain go1.26.11

require (
	charm.land/bubbletea/v2 v2.1.1
	charm.land/lipgloss/v2 v2.0.6
	github.com/charmbracelet/x/ansi v0.11.9
	github.com/pelletier/go-toml/v2 v2.4.3
)

// Test-only (only _test.go files import it): teatest is an untagged exp
// module, pinned to this pseudo-version on purpose; expect breaking changes
// on a bump. dependabot.yml ignores it.
require github.com/charmbracelet/x/exp/teatest/v2 v2.0.0-20261101151554-27ecf3e4d454

require (
	github.com/aymanbagabas/go-udiff v0.4.1 // indirect
	github.com/charmbracelet/colorprofile v0.4.3 // indirect
	github.com/charmbracelet/ultraviolet v0.0.0-20261008173134-6b8d4baf91b4 // indirect
	github.com/charmbracelet/x/exp/golden v0.0.0-20251109135125-8916d276318f // indirect
	github.com/charmbracelet/x/term v0.2.2 // indirect
	github.com/charmbracelet/x/termios v0.1.1 // indirect
	github.com/charmbracelet/x/windows v0.2.2 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.1 // indirect
	github.com/mattn/go-runewidth v0.0.30 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/xo/terminfo v1.0.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
)
