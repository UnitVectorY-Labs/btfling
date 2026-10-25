package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/UnitVectorY-Labs/btfling/internal/ble"
	"github.com/UnitVectorY-Labs/btfling/internal/config"
	"github.com/charmbracelet/bubbles/filepicker"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const logo = " _     _    __ _ _             \n| |__ | |_ / _| (_)_ __   __ _ \n| '_ \\| __| |_| | | '_ \\ / _` |\n| |_) | |_|  _| | | | | | (_| |\n|_.__/ \\__|_| |_|_|_| |_|\\__, |\n                         |___/ "

var (
	orange     = lipgloss.Color("#FF8A3D")
	blue       = lipgloss.Color("#59B8FF")
	muted      = lipgloss.Color("#92929D")
	accent     = lipgloss.NewStyle().Foreground(orange).Bold(true)
	subtle     = lipgloss.NewStyle().Foreground(muted)
	radioStyle = lipgloss.NewStyle().Foreground(blue)
	panel      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#48404A")).Padding(1, 2)
)

type peerItem struct {
	peer        config.Peer
	description string
	pairing     bool
}

func (p peerItem) Title() string       { return p.peer.Name }
func (p peerItem) Description() string { return p.description }
func (p peerItem) FilterValue() string { return p.peer.Name + " " + p.peer.ID }

type sighting struct {
	peer ble.Peer
	at   time.Time
}
type uiPeer struct {
	run  int
	peer ble.Peer
}
type uiStatus struct {
	run  int
	text string
}
type uiBytes struct {
	run   int
	bytes int64
}
type uiDone struct {
	run  int
	kind string
	peer config.Peer
	err  error
}
type uiClock time.Time

type tui struct {
	mouse                                    *mouseLayout
	scroll                                   int
	ctx                                      context.Context
	dir                                      string
	c                                        *config.Config
	width, height, tab, run                  int
	peers                                    list.Model
	saved                                    list.Model
	spinner                                  spinner.Model
	picker                                   filepicker.Model
	input                                    textinput.Model
	progress                                 progress.Model
	form, stage, direction                   string
	selected                                 config.Peer
	path, folder, alias, fingerprint, status string
	online                                   map[string]sighting
	bytes, total                             int64
	active                                   string
	cancel                                   context.CancelFunc
	selections                               chan string
	events                                   chan tea.Msg
}

func peerDelegate() list.DefaultDelegate {
	d := list.NewDefaultDelegate()
	d.SetSpacing(0)
	d.Styles.SelectedTitle = d.Styles.SelectedTitle.Foreground(orange).BorderForeground(orange)
	d.Styles.SelectedDesc = d.Styles.SelectedDesc.Foreground(muted).BorderForeground(orange)
	return d
}
func peerList() list.Model {
	l := list.New(nil, peerDelegate(), 70, 12)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(false)
	l.DisableQuitKeybindings()
	return l
}
func newTUI(ctx context.Context, dir string, c *config.Config) tui {
	spin := spinner.New()
	spin.Spinner = spinner.Dot
	spin.Style = radioStyle
	input := textinput.New()
	input.Prompt = "› "
	input.PromptStyle = accent
	input.TextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#EEEEF2"))
	input.CharLimit = 4096
	picker := filepicker.New()
	cwd, _ := os.Getwd()
	picker.CurrentDirectory = cwd
	picker.Styles.Selected = picker.Styles.Selected.Foreground(orange)
	picker.Styles.Cursor = picker.Styles.Cursor.Foreground(orange)
	m := tui{mouse: newMouseLayout(), ctx: ctx, dir: dir, c: c, width: 88, height: 36, peers: peerList(), saved: peerList(), spinner: spin, picker: picker, input: input, progress: progress.New(progress.WithSolidFill(string(orange)), progress.WithWidth(60)), direction: "send", stage: "peer", online: map[string]sighting{}, events: make(chan tea.Msg, 128), status: "Ready • all transfers are encrypted"}
	m.peers.SetDelegate(mouseDelegate{peerDelegate(), m.mouse.body})
	m.saved.SetDelegate(mouseDelegate{peerDelegate(), m.mouse.body})
	m.refreshLists()
	m.resize()
	return m
}
func runTUI(ctx context.Context, dir string, in io.Reader, out io.Writer) error {
	c, e := config.Load(dir)
	if e != nil {
		return e
	}
	m := newTUI(ctx, dir, c)
	defer m.mouse.close()
	_, e = tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(out), tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	return e
}
func (m tui) wait() tea.Cmd {
	return func() tea.Msg {
		select {
		case e := <-m.events:
			return e
		case <-m.ctx.Done():
			return tea.Quit()
		}
	}
}
func clockTick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return uiClock(t) })
}
func (m tui) Init() tea.Cmd { return tea.Batch(m.wait(), m.spinner.Tick, clockTick()) }
func emitUI(events chan tea.Msg, msg tea.Msg) {
	select {
	case events <- msg:
	default:
	}
}
func (m *tui) stop() {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.active = ""
	m.run++
}
func (m *tui) startPairing() tea.Cmd {
	m.stop()
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancel = cancel
	m.active = "pair"
	m.status = "Advertising and discovering nearby devices…"
	selections := make(chan string, 1)
	m.selections = selections
	c := *m.c
	c.Peers = append([]config.Peer(nil), c.Peers...)
	run, events, dir := m.run, m.events, m.dir
	options := pairOptions{Alias: m.alias, Trust: m.fingerprint}
	return func() tea.Msg {
		defer cancel()
		p, e := runPairing(ctx, dir, &c, options, selections, func(p ble.Peer) { emitUI(events, uiPeer{run, p}) }, func(s string) { emitUI(events, uiStatus{run, s}) })
		return uiDone{run: run, kind: "pair", peer: p, err: e}
	}
}
func (m *tui) startTransfer() tea.Cmd {
	m.stop()
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancel = cancel
	m.active = "transfer"
	m.stage = "running"
	m.bytes = 0
	m.status = "Waiting for " + m.selected.Name + " over Bluetooth…"
	run, events, c, direction, path, name := m.run, m.events, m.c, m.direction, m.path, m.selected.Name
	option := "-in"
	if direction == "receive" {
		option = "-out"
	}
	return func() tea.Msg {
		defer cancel()
		e := transfer(ctx, c, direction, []string{name, option, path}, strings.NewReader(""), io.Discard, eventWriter{run, events}, func(n int64) { emitUI(events, uiBytes{run, n}) })
		return uiDone{run: run, kind: "transfer", err: e}
	}
}

type eventWriter struct {
	run    int
	events chan tea.Msg
}

func (w eventWriter) Write(p []byte) (int, error) {
	emitUI(w.events, uiStatus{w.run, strings.TrimSpace(string(p))})
	return len(p), nil
}
func (m *tui) refreshLists() {
	selectedID := ""
	if p, ok := m.peers.SelectedItem().(peerItem); ok {
		selectedID = p.peer.ID
	}
	known := map[string]config.Peer{}
	for _, p := range m.c.Peers {
		known[p.ID] = p
	}
	items := []peerItem{}
	for id, s := range m.online {
		if time.Since(s.at) > 20*time.Second {
			continue
		}
		p := config.Peer{ID: id, Name: s.peer.Name}
		tag := "Nearby"
		if old, ok := known[id]; ok {
			p = old
			tag = "Paired"
			delete(known, id)
		}
		state := "Receiving • switch remote to Pair"
		if s.peer.Pairing {
			state = "Pairing mode • Bluetooth active"
		}
		items = append(items, peerItem{p, tag + " · " + state, s.peer.Pairing})
	}
	for _, p := range known {
		items = append(items, peerItem{p, "Paired · not seen nearby", false})
	}
	sort.Slice(items, func(i, j int) bool { return strings.ToLower(items[i].peer.Name) < strings.ToLower(items[j].peer.Name) })
	rows := make([]list.Item, 0, len(items))
	for _, p := range items {
		rows = append(rows, p)
	}
	m.peers.SetItems(rows)
	for i, p := range items {
		if p.peer.ID == selectedID {
			m.peers.Select(i)
		}
	}
	saved := make([]list.Item, 0, len(m.c.Peers))
	for _, p := range m.c.Peers {
		label := "Encrypted · " + p.ID
		saved = append(saved, peerItem{peer: p, description: label})
	}
	m.saved.SetItems(saved)
}
func (m *tui) resize() {
	width := max(20, m.width-12)
	height := max(3, m.height-30)
	m.peers.SetSize(width, height)
	m.saved.SetSize(width, height)
	m.picker.SetHeight(height)
	m.input.Width = max(10, width-4)
	m.progress.Width = max(10, width-4)
}
func (m *tui) openForm(name, value string) tea.Cmd {
	m.form = name
	m.scroll = 0
	m.input.SetValue(value)
	m.input.Focus()
	return textinput.Blink
}
func (m *tui) selectTab(tab int) tea.Cmd {
	if tab == m.tab {
		return nil
	}
	if m.active != "" {
		m.stop()
		m.status = "Operation cancelled"
	}
	m.form = ""
	m.scroll = 0
	m.tab = tab
	if tab == 1 {
		return m.startPairing()
	}
	if tab == 2 {
		m.stage = "peer"
	}
	return nil
}
func (m *tui) submitForm() tea.Cmd {
	value := strings.TrimSpace(m.input.Value())
	field := m.form
	switch field {
	case "Client name":
		if !config.ValidName(value) {
			m.status = "Enter a name of 1–64 bytes without slashes or control characters"
			return nil
		}
		copy := *m.c
		copy.Name = value
		if e := copy.Save(m.dir); e != nil {
			m.status = e.Error()
			return nil
		}
		m.c = &copy
		m.status = "Client name saved"
	case "Peer nickname override":
		if value != "" && !config.ValidName(value) {
			m.status = "Invalid nickname"
			return nil
		}
		m.alias = value
	case "Expected fingerprint (optional)":
		if e := validateFingerprint(value); e != nil {
			m.status = e.Error()
			return nil
		}
		m.fingerprint = value
	case "Destination filename":
		if value == "" || filepath.Base(value) != value || value == "." || value == ".." {
			m.status = "Enter a filename without directory separators"
			return nil
		}
		m.path = filepath.Join(m.folder, value)
		m.stage = "review"
	case "File path":
		if value == "" {
			m.status = "A file path is required"
			return nil
		}
		p, e := filepath.Abs(value)
		if e != nil {
			m.status = e.Error()
			return nil
		}
		m.path = p
		if m.direction == "send" {
			info, e := os.Stat(p)
			if e != nil || !info.Mode().IsRegular() {
				m.status = "Choose an existing regular file"
				return nil
			}
			m.total = info.Size()
		}
		m.stage = "review"
	}
	m.form = ""
	m.input.Blur()
	if field == "Peer nickname override" || field == "Expected fingerprint (optional)" {
		return m.startPairing()
	}
	return nil
}
func (m tui) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.MouseMsg:
		return m.updateMouse(v)
	case tea.WindowSizeMsg:
		m.width = v.Width
		m.height = v.Height
		m.resize()
		return m, nil
	case uiClock:
		m.refreshLists()
		return m, clockTick()
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case uiPeer:
		if v.run == m.run {
			m.online[v.peer.ID] = sighting{v.peer, time.Now()}
			m.refreshLists()
		}
		return m, m.wait()
	case uiStatus:
		if v.run == m.run {
			m.status = v.text
		}
		return m, m.wait()
	case uiBytes:
		if v.run == m.run {
			m.bytes = v.bytes
		}
		return m, m.wait()
	case uiDone:
		if c, e := config.Load(m.dir); e == nil {
			m.c = c
			m.refreshLists()
		}
		if v.run != m.run {
			return m, nil
		}
		m.active = ""
		m.cancel = nil
		if v.err != nil {
			m.status = "Error: " + v.err.Error()
		} else if v.kind == "pair" {
			m.status = "Paired with " + v.peer.Name + " • compare fingerprints out of band"
		} else {
			m.status = "Transfer complete • receiver confirmed the content"
		}
		if c, e := config.Load(m.dir); e == nil {
			m.c = c
		}
		m.refreshLists()
		if v.kind == "transfer" {
			m.stage = "review"
		}
		return m, nil
	case tea.KeyMsg:
		if v.String() == "ctrl+c" {
			m.stop()
			return m, tea.Quit
		}
		if m.form != "" {
			if v.String() == "esc" {
				m.form = ""
				m.input.Blur()
				return m, nil
			}
			if v.String() == "enter" {
				return m, m.submitForm()
			}
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
		switch v.String() {
		case "pgdown":
			m.scroll = min(m.mouse.maxScroll, m.mouse.offset+5)
			return m, nil
		case "pgup":
			m.scroll = max(0, m.mouse.offset-5)
			return m, nil
		case "q":
			m.stop()
			return m, tea.Quit
		case "tab":
			return m, m.selectTab((m.tab + 1) % 3)
		case "shift+tab":
			return m, m.selectTab((m.tab + 2) % 3)
		case "1", "2", "3":
			return m, m.selectTab(int(v.String()[0] - '1'))
		case "esc":
			m.stop()
			m.stage = "peer"
			m.status = "Ready • operation cancelled"
			return m, nil
		}
		switch m.tab {
		case 0:
			if v.String() == "n" {
				return m, m.openForm("Client name", m.c.Name)
			}
		case 1:
			switch v.String() {
			case "p":
				if m.active == "pair" {
					m.stop()
					m.status = "Pairing paused"
					return m, nil
				}
				return m, m.startPairing()
			case "a":
				m.stop()
				return m, m.openForm("Peer nickname override", m.alias)
			case "v":
				m.stop()
				return m, m.openForm("Expected fingerprint (optional)", m.fingerprint)
			case "enter":
				if m.active != "pair" {
					return m, m.startPairing()
				}
				if p, ok := m.peers.SelectedItem().(peerItem); ok && p.pairing {
					select {
					case m.selections <- p.peer.ID:
						m.status = "Pairing with " + p.peer.Name + "…"
					default:
					}
				} else {
					m.status = "Choose a nearby device that is in Pair mode"
				}
				return m, nil
			}
			var cmd tea.Cmd
			m.peers, cmd = m.peers.Update(msg)
			return m, cmd
		case 2:
			if m.active == "transfer" {
				return m, nil
			}
			if m.stage == "peer" {
				switch v.String() {
				case "s", "r":
					m.direction = "send"
					if v.String() == "r" {
						m.direction = "receive"
					}
					return m, nil
				case "enter":
					p, ok := m.saved.SelectedItem().(peerItem)
					if !ok {
						m.status = "Pair a device first"
						return m, nil
					}
					m.selected = p.peer
					m.stage = "file"
					m.picker.DirAllowed = m.direction == "receive"
					m.picker.FileAllowed = m.direction == "send"
					return m, m.picker.Init()
				}
				var cmd tea.Cmd
				m.saved, cmd = m.saved.Update(msg)
				return m, cmd
			}
			if m.stage == "file" && v.String() == "f" {
				return m, m.openForm("File path", m.path)
			}
			if m.stage == "review" && v.String() == "enter" {
				return m, m.startTransfer()
			}
			if m.stage == "review" && v.String() == "backspace" {
				m.stage = "peer"
				return m, nil
			}
		}
	}
	if m.tab == 2 && m.stage == "file" {
		var cmd tea.Cmd
		m.picker, cmd = m.picker.Update(msg)
		if ok, path := m.picker.DidSelectFile(msg); ok {
			if m.direction == "receive" {
				m.folder = path
				return m, m.openForm("Destination filename", "received.bin")
			}
			m.path = path
			if info, e := os.Stat(path); e == nil {
				m.total = info.Size()
			}
			m.stage = "review"
		}
		return m, cmd
	}
	if m.form != "" {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}
func (m tui) View() string {
	width := max(24, m.width-8)
	inner := max(18, width-6)
	heading := accent.Render(logo) + "\n" + subtle.Render("NEARBY DEVICES. ENCRYPTED STREAMS. BLUETOOTH.")
	tabs := []string{}
	for i, name := range []string{"1  Client", "2  Pair", "3  Transfer"} {
		style := lipgloss.NewStyle().Foreground(muted).Padding(0, 2)
		if i == m.tab {
			style = style.Foreground(lipgloss.Color("#171719")).Background(orange).Bold(true)
		}
		tabs = append(tabs, m.mouse.screen.Mark(fmt.Sprintf("tab-%d", i), style.Render(name)))
	}
	mark := m.mouse.body.Mark
	var body strings.Builder
	if m.form != "" {
		body.WriteString(accent.Render(m.form) + "\n\n" + m.input.View() + "\n\n" + mark("key:enter", subtle.Render("Enter save")) + " · " + mark("key:esc", subtle.Render("Esc cancel")))
	} else {
		switch m.tab {
		case 0:
			body.WriteString(accent.Render("YOUR CLIENT") + "\n\n" + lipgloss.NewStyle().Bold(true).Render(m.c.Name) + "\n" + subtle.Render(m.c.ID) + "\n\n" + accent.Render("SECURE CONNECTIONS") + "\n" + "Mutual TLS 1.3 · Ed25519 identity · X25519 session keys\n\n" + subtle.Render("Your public-key fingerprint") + "\n" + config.Fingerprint(m.c.PublicKey()) + "\n\n" + fmt.Sprintf("%d paired devices", len(m.c.Peers)) + "\n" + subtle.Render(m.dir) + "\n\n" + mark("key:n", "n  Edit client name") + "\n" + mark("key:tab", "Tab  Discover and pair a nearby device"))
			if len(m.c.Peers) > 0 {
				body.WriteString("\n\n" + accent.Render("PAIRED DEVICES"))
				for _, p := range m.c.Peers {
					body.WriteString("\n" + p.Name + "\n" + subtle.Render(config.Fingerprint(p.PublicKey)))
				}
			}
		case 1:
			label := "Pairing paused"
			if m.active == "pair" {
				label = m.spinner.View() + radioStyle.Render(" Bluetooth active · broadcasting + discovering")
			}
			body.WriteString(accent.Render("PAIR A DEVICE") + "\n" + label + "\n\n")
			if len(m.peers.Items()) == 0 {
				body.WriteString(subtle.Render("Nearby devices will appear here. Open Pair on the other device."))
			} else {
				body.WriteString(m.peers.View())
			}
			trust := "First-use trust · compare fingerprints out of band"
			if m.fingerprint != "" {
				trust = "Verify fingerprint: " + m.fingerprint
			}
			alias := "Learn the other client's name"
			if m.alias != "" {
				alias = "Save as " + m.alias
			}
			body.WriteString("\n" + subtle.Render(trust) + "\n" + subtle.Render(alias) + "\n\n" + mark("key:enter", "Enter pair selected") + " · " + mark("key:p", "p start/stop") + "\n" + mark("key:a", "a override name") + " · " + mark("key:v", "v optional fingerprint"))
		case 2:
			send, receive := "s  Send file", "r  Receive file"
			if m.direction == "send" {
				send = accent.Render("● " + send)
			} else {
				receive = accent.Render("● " + receive)
			}
			body.WriteString(accent.Render("TRANSFER") + "\n" + mark("key:s", send) + "     " + mark("key:r", receive) + "\n\n")
			switch m.stage {
			case "peer":
				body.WriteString("Choose a paired device\n\n")
				if len(m.saved.Items()) == 0 {
					body.WriteString(subtle.Render("No paired devices yet. Visit Pair to get started."))
				} else {
					body.WriteString(m.saved.View())
				}
				body.WriteString("\n" + mark("key:enter", "Enter choose peer"))
			case "file":
				prompt := "Choose the file to send"
				if m.direction == "receive" {
					prompt = "Choose a destination folder"
				}
				body.WriteString(prompt + "\n" + subtle.Render(m.picker.CurrentDirectory) + "\n\n" + m.mouse.pickerView(m.picker) + "\n\n↑/↓ choose · " + mark("key:right", "→ open folder") + " · " + mark("key:enter", "Enter select") + "\n" + mark("key:left", "← parent folder") + " · " + mark("key:f", "f type path"))
			default:
				body.WriteString("Peer  " + m.selected.Name + "\nFile  " + m.path + "\n\n" + accent.Render("ENCRYPTED · TRUSTED KEY REQUIRED") + "\n\n")
				if m.active == "transfer" {
					body.WriteString(m.spinner.View() + radioStyle.Render(" Bluetooth transfer active") + "\n")
					if m.direction == "send" && m.total > 0 {
						body.WriteString(m.progress.ViewAs(float64(m.bytes)/float64(m.total)) + "\n")
					}
					body.WriteString(fmt.Sprintf("%s transferred", humanBytes(m.bytes)))
				} else {
					body.WriteString(mark("key:enter", "Enter start "+m.direction) + " · " + mark("key:backspace", "Backspace choose another peer"))
				}
			}
		}
	}
	footer := subtle.Render("Tab switch tabs · PgUp/PgDn scroll · ") + m.mouse.screen.Mark("cancel", subtle.Render("Esc cancel")) + " · " + m.mouse.screen.Mark("quit", subtle.Render("q quit")) + "\n" + subtle.Render("Click controls · Wheel navigate/scroll")
	vp := viewport.New(inner, max(4, m.height-21))
	// Scan body zones before clipping; mouse coordinates are translated into this
	// content space, so partially visible controls remain clickable.
	vp.SetContent(m.mouse.body.Scan(lipgloss.NewStyle().Width(inner).Render(body.String())))
	vp.SetYOffset(m.scroll)
	m.mouse.offset = vp.YOffset
	m.mouse.maxScroll = max(0, vp.TotalLineCount()-vp.Height)
	m.mouse.width, m.mouse.height = inner, vp.Height
	// Include the panel frame so its content width matches the scanned body.
	// Otherwise long lines wrap again after scanning and shift mouse targets.
	content := m.mouse.screen.Mark("viewport", panel.Width(inner+panel.GetHorizontalFrameSize()).Render(vp.View()))
	status := lipgloss.NewStyle().Foreground(orange).Width(width).Render(m.status)
	return m.mouse.screen.Scan(lipgloss.NewStyle().Padding(1, 3).Render(heading + "\n\n" + lipgloss.JoinHorizontal(lipgloss.Top, tabs...) + "\n" + content + "\n" + status + "\n" + footer))
}
func humanBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	if n < 1024*1024 {
		return fmt.Sprintf("%.1f KiB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MiB", float64(n)/(1024*1024))
}
