package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/UnitVectorY-Labs/btfling/internal/ble"
	"github.com/UnitVectorY-Labs/btfling/internal/config"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestTUITabsAndPeerNames(t *testing.T) {
	dir := t.TempDir()
	c, e := config.Init(dir, "My laptop")
	if e != nil {
		t.Fatal(e)
	}
	m := newTUI(context.Background(), dir, c)
	if view := ansi.Strip(m.View()); !strings.Contains(view, "YOUR CLIENT") || !strings.Contains(view, "My laptop") || !strings.Contains(view, "1  Client") {
		t.Fatal("missing client tab details")
	}
	model, cmd := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = model.(tui)
	if m.tab != 1 || m.active != "pair" || cmd == nil {
		t.Fatal("Pair tab did not start mutual discovery")
	}
	m.stop()
	m.online["00000000000000000000000000000009"] = sighting{peer: ble.Peer{ID: "00000000000000000000000000000009", Name: "Other laptop", Pairing: true}, at: time.Now()}
	m.refreshLists()
	if len(m.peers.Items()) != 1 || m.peers.Items()[0].(peerItem).Title() != "Other laptop" {
		t.Fatal("remote name was not learned")
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = model.(tui)
	if m.tab != 2 || m.active != "" {
		t.Fatal("switching tabs did not stop pairing")
	}
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m = model.(tui)
	if m.direction != "receive" {
		t.Fatal("cannot select receive")
	}
}
func TestTUIRequiresDestinationAndNoPlaintextControls(t *testing.T) {
	dir := t.TempDir()
	c, _ := config.Init(dir, "client")
	m := newTUI(context.Background(), dir, c)
	m.tab = 2
	m.direction = "receive"
	m.folder = dir
	m.form = "Destination filename"
	m.input.SetValue("")
	m.submitForm()
	if m.form == "" || m.stage == "review" {
		t.Fatal("accepted missing destination")
	}
	m.input.SetValue("report.pdf")
	m.submitForm()
	if m.stage != "review" || !strings.HasSuffix(m.path, "report.pdf") {
		t.Fatal("destination was not selected")
	}
	if strings.Contains(strings.ToLower(ansi.Strip(m.View())), "plaintext mode") {
		t.Fatal("TUI offered unencrypted transfers")
	}
}
func TestTUIViewFitsStandardTerminal(t *testing.T) {
	c, _ := config.Init(t.TempDir(), "client")
	m := newTUI(context.Background(), "example", c)
	for tab := range 3 {
		m.tab = tab
		lines := strings.Split(ansi.Strip(m.View()), "\n")
		if len(lines) > m.height {
			t.Fatalf("tab %d uses %d lines for %d-row terminal", tab, len(lines), m.height)
		}
	}
}
