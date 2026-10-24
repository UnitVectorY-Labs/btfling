package app

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/filepicker"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	zone "github.com/lrstanley/bubblezone"
)

// Each coordinate space is scanned once: body coordinates survive viewport
// clipping, while screen coordinates locate tabs, the viewport, and the footer.
type mouseLayout struct {
	width, height              int
	screen, body               *zone.Manager
	offset, maxScroll          int
	pickerRows, pickerSelected int
}

func newMouseLayout() *mouseLayout {
	return &mouseLayout{screen: zone.New(), body: zone.New()}
}

func (l *mouseLayout) close() { l.screen.Close(); l.body.Close() }

type mouseDelegate struct {
	list.DefaultDelegate
	zones *zone.Manager
}

func (d mouseDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	var row strings.Builder
	d.DefaultDelegate.Render(&row, m, index, item)
	fmt.Fprint(w, d.zones.Mark(fmt.Sprintf("peer-%d", index), row.String()))
}

// The filepicker keeps its cursor private. Its rendered rows provide a stable
// mapping to relative up/down commands, preserving its sorting and symlink rules.
func (l *mouseLayout) pickerView(p filepicker.Model) string {
	rows := strings.Split(p.View(), "\n")
	l.pickerRows = 0
	l.pickerSelected = -1
	// Only a populated picker renders a cursor. Empty-directory messages have
	// no selectable rows, regardless of the message's styling or wording.
	for i, row := range rows {
		if strings.HasPrefix(ansi.Strip(row), p.Cursor) {
			l.pickerSelected = i
			break
		}
	}
	if l.pickerSelected < 0 {
		return strings.Join(rows, "\n")
	}
	for i, row := range rows {
		if strings.TrimSpace(ansi.Strip(row)) == "" {
			continue
		}
		rows[i] = l.body.Mark(fmt.Sprintf("file-%d", i), row)
		l.pickerRows = i + 1
	}
	return strings.Join(rows, "\n")
}

func mouseKey(key string) tea.KeyMsg {
	switch key {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
}

func (m tui) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	click := msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress
	wheel := msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown
	if !click && !wheel {
		return m, nil
	}
	if click {
		for i := range 3 {
			if m.mouse.screen.Get(fmt.Sprintf("tab-%d", i)).InBounds(msg) {
				return m, m.selectTab(i)
			}
		}
		for id, key := range map[string]string{"cancel": "esc", "quit": "q"} {
			if m.mouse.screen.Get(id).InBounds(msg) {
				if key == "q" {
					m.stop()
					return m, tea.Quit
				}
				return m.Update(mouseKey(key))
			}
		}
	}
	viewport := m.mouse.screen.Get("viewport")
	if !viewport.InBounds(msg) {
		return m, nil
	}
	x, y := viewport.Pos(msg)
	x, y = x-3, y-2 // panel border and padding
	if x < 0 || y < 0 || x >= m.mouse.width || y >= m.mouse.height {
		return m, nil
	}
	local := msg
	local.X, local.Y = x, y+m.mouse.offset
	if wheel {
		key, step := "down", 3
		if msg.Button == tea.MouseButtonWheelUp {
			key, step = "up", -3
		}
		if m.form == "" {
			if m.tab == 1 || (m.tab == 2 && m.stage == "peer") {
				l := &m.peers
				if m.tab == 2 {
					l = &m.saved
				}
				for i := range l.Items() {
					if m.mouse.body.Get(fmt.Sprintf("peer-%d", i)).InBounds(local) {
						var cmd tea.Cmd
						*l, cmd = l.Update(mouseKey(key))
						return m, cmd
					}
				}
			}
			if m.tab == 2 && m.stage == "file" {
				for i := range m.mouse.pickerRows {
					if m.mouse.body.Get(fmt.Sprintf("file-%d", i)).InBounds(local) {
						return m.Update(mouseKey(key))
					}
				}
			}
		}
		m.scroll = min(m.mouse.maxScroll, max(0, m.mouse.offset+step))
		return m, nil
	}
	if m.form == "" {
		if m.tab == 1 || (m.tab == 2 && m.stage == "peer") {
			l := &m.peers
			if m.tab == 2 {
				l = &m.saved
			}
			for i := range l.Items() {
				if m.mouse.body.Get(fmt.Sprintf("peer-%d", i)).InBounds(local) {
					l.Select(i)
					return m, nil
				}
			}
		}
		if m.tab == 2 && m.stage == "file" && m.mouse.pickerSelected >= 0 {
			for i := range m.mouse.pickerRows {
				if !m.mouse.body.Get(fmt.Sprintf("file-%d", i)).InBounds(local) {
					continue
				}
				delta := i - m.mouse.pickerSelected
				key := "down"
				if delta < 0 {
					key, delta = "up", -delta
				}
				for range delta {
					m.picker, _ = m.picker.Update(mouseKey(key))
				}
				return m, nil
			}
		}
	}
	for _, key := range []string{"enter", "esc", "n", "tab", "p", "a", "v", "s", "r", "right", "left", "f", "backspace"} {
		if m.mouse.body.Get("key:" + key).InBounds(local) {
			return m.Update(mouseKey(key))
		}
	}
	return m, nil
}
