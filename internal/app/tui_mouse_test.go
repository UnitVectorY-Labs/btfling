package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/UnitVectorY-Labs/btfling/internal/config"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	zone "github.com/lrstanley/bubblezone"
)

func mouseTUI(t *testing.T) tui {
	t.Helper()
	dir := t.TempDir()
	c, err := config.Init(dir, "mouse client")
	if err != nil {
		t.Fatal(err)
	}
	m := newTUI(context.Background(), dir, c)
	t.Cleanup(m.mouse.close)
	t.Cleanup(m.stop)
	return m
}

// BubbleZone publishes bounds asynchronously; wait for both coordinate spaces
// to describe the frame before delivering a simulated terminal event.
func mouseTarget(t *testing.T, m tui, manager *zone.Manager, id, text string) tea.MouseMsg {
	t.Helper()
	view := ansi.Strip(m.View())
	msg := tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
	found := false
	for y, line := range strings.Split(view, "\n") {
		if x := strings.Index(line, text); x >= 0 {
			msg.X, msg.Y = ansi.StringWidth(line[:x]), y
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("control %q not visible:\n%s", text, view)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		local := msg
		if manager == m.mouse.body {
			vp := m.mouse.screen.Get("viewport")
			if !vp.InBounds(msg) {
				time.Sleep(time.Millisecond)
				continue
			}
			local.X, local.Y = vp.Pos(msg)
			local.X -= 3
			local.Y -= 2
			local.Y += m.mouse.offset
		}
		if manager.Get(id).InBounds(local) {
			return msg
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("zone %q does not match visible control %q: screen=%+v body=%+v msg=%+v\n%s", id, text, m.mouse.screen.Get("viewport"), manager.Get(id), msg, view)
	return msg
}

func TestMouseTabsAndActions(t *testing.T) {
	m := mouseTUI(t)
	msg := mouseTarget(t, m, m.mouse.screen, "tab-1", "2  Pair")
	model, cmd := m.Update(msg)
	m = model.(tui)
	defer m.stop()
	if m.tab != 1 || m.active != "pair" || cmd == nil {
		t.Fatal("mouse tab did not start pairing")
	}
	// Releases, motion, and secondary buttons must not activate controls.
	for _, event := range []tea.MouseMsg{
		{X: msg.X, Y: msg.Y, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease},
		{X: msg.X, Y: msg.Y, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion},
		{X: msg.X, Y: msg.Y, Button: tea.MouseButtonRight, Action: tea.MouseActionPress},
	} {
		model, _ = m.Update(event)
		if model.(tui).run != m.run {
			t.Fatal("non-click restarted pairing")
		}
	}
	msg = mouseTarget(t, m, m.mouse.screen, "tab-2", "3  Transfer")
	model, _ = m.Update(msg)
	m = model.(tui)
	if m.active != "" {
		t.Fatal("tab change did not cancel pairing")
	}
	msg = mouseTarget(t, m, m.mouse.body, "key:r", "r  Receive file")
	model, _ = m.Update(msg)
	m = model.(tui)
	if m.direction != "receive" {
		t.Fatal("receive control did not work")
	}
}

func TestMouseScrolledFormAndResize(t *testing.T) {
	m := mouseTUI(t)
	m.View()
	// Client-name action begins below the default viewport. Scroll until visible.
	msg := mouseTarget(t, m, m.mouse.screen, "viewport", "YOUR CLIENT")
	msg.Button = tea.MouseButtonWheelDown
	for range 20 {
		model, _ := m.Update(msg)
		m = model.(tui)
		m.View()
	}
	if m.scroll != m.mouse.maxScroll {
		t.Fatal("wheel did not clamp at bottom")
	}
	click := mouseTarget(t, m, m.mouse.body, "key:n", "n  Edit client name")
	model, _ := m.Update(click)
	m = model.(tui)
	if m.form != "Client name" || m.scroll != 0 {
		t.Fatal("scrolled name control did not open form")
	}
	m.input.SetValue("renamed client")
	model, _ = m.Update(tea.WindowSizeMsg{Width: 70, Height: 30})
	m = model.(tui)
	click = mouseTarget(t, m, m.mouse.body, "key:enter", "Enter save")
	model, _ = m.Update(click)
	m = model.(tui)
	if m.form != "" || m.c.Name != "renamed client" {
		t.Fatal("mouse save failed after resize")
	}
}

func TestMousePeerAndFileSelection(t *testing.T) {
	m := mouseTUI(t)
	m.tab = 2
	m.c.Peers = []config.Peer{{ID: "one", Name: "First peer"}, {ID: "two", Name: "Second peer"}}
	m.refreshLists()
	msg := mouseTarget(t, m, m.mouse.body, "peer-1", "Second peer")
	model, _ := m.Update(msg)
	m = model.(tui)
	if m.saved.Index() != 1 || m.stage != "peer" {
		t.Fatal("click should select peer for review")
	}
	msg.Button = tea.MouseButtonWheelUp
	model, _ = m.Update(msg)
	m = model.(tui)
	if m.saved.Index() != 0 {
		t.Fatal("wheel did not navigate peers")
	}
	msg = mouseTarget(t, m, m.mouse.body, "key:enter", "Enter choose peer")
	model, cmd := m.Update(msg)
	m = model.(tui)
	if m.stage != "file" || cmd == nil {
		t.Fatal("choose peer did not open picker")
	}
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	m.picker.CurrentDirectory = dir
	model, _ = m.Update(m.picker.Init()())
	m = model.(tui)
	msg = mouseTarget(t, m, m.mouse.body, "file-1", "b.txt")
	model, _ = m.Update(msg)
	m = model.(tui)
	// Explicit activation should select the clicked file through the picker API.
	model, _ = m.Update(mouseKey("enter"))
	m = model.(tui)
	if m.stage != "review" || m.path != filepath.Join(dir, "b.txt") {
		t.Fatalf("wrong file selected: %q (%s)", m.path, m.stage)
	}
}
