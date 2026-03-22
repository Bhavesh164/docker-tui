package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/creack/pty"
)

type focusArea int

type viewMode int

type resourceMode int

type filterMode int

const (
	focusList focusArea = iota
	focusSearch
	focusCommand
	focusSnippet
	focusLogSearch
)

const (
	viewMain viewMode = iota
	viewLogs
	viewShell
)

const (
	resourceContainers resourceMode = iota
	resourceVolumes
)

const (
	filterAll filterMode = iota
	filterRunning
)

var (
	appStyle           = lipgloss.NewStyle().Padding(1, 2)
	titleStyle         = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("86"))
	sectionStyle       = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	selectedStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Background(lipgloss.Color("62")).Bold(true)
	mutedStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	errorStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	runningStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true)
	stoppedStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	helpStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	snippetStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("141"))
	cursorStyle        = lipgloss.NewStyle().Background(lipgloss.Color("230")).Foreground(lipgloss.Color("16"))
	activeSectionStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("86")).Padding(0, 1)
	matchStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("16")).Background(lipgloss.Color("221")).Bold(true)
	logSelectionStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Background(lipgloss.Color("62"))
	logErrorStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	logWarnStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	logInfoStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("81"))
	logDebugStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("141"))
	logTraceStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
)

type Container struct {
	ID     string `json:"ID"`
	Image  string `json:"Image"`
	Names  string `json:"Names"`
	State  string `json:"State"`
	Status string `json:"Status"`
}

type Volume struct {
	Driver     string `json:"Driver"`
	Name       string `json:"Name"`
	Mountpoint string `json:"Mountpoint"`
	Scope      string `json:"Scope"`
}

type SnippetStore struct {
	Snippets map[string][]string `json:"snippets"`
}

type dockerStatusMsg struct {
	ok  bool
	err error
}

type containersMsg struct {
	items []Container
	ok    bool
	err   error
}

type volumesMsg struct {
	items []Volume
	err   error
}

type logsMsg struct {
	containerID string
	content     string
	err         error
}

type cmdResultMsg struct {
	output string
	err    error
}

type shellResultMsg struct {
	containerID string
	command     string
	output      string
	err         error
}

type shellStartedMsg struct {
	session *shellSession
	err     error
}

type shellChunkMsg struct {
	sessionID int
	data      string
	err       error
}

type shellClosedMsg struct {
	sessionID int
	err       error
}

type tickMsg time.Time

type shellSession struct {
	id   int
	cmd  *exec.Cmd
	pty  *os.File
	name string
}

type terminalBuffer struct {
	cols          int
	rows          int
	cells         [][]rune
	cursorRow     int
	cursorCol     int
	savedRow      int
	savedCol      int
	cursorVisible bool
	state         int
	csiBuf        strings.Builder
	oscEsc        bool
}

var nextShellSessionID int

func newShellSessionID() int {
	nextShellSessionID++
	return nextShellSessionID
}

func newTerminalBuffer(cols, rows int) *terminalBuffer {
	t := &terminalBuffer{}
	t.resize(cols, rows)
	t.cursorVisible = true
	return t
}

func (t *terminalBuffer) resize(cols, rows int) {
	cols = max(10, cols)
	rows = max(4, rows)
	old := t.cells
	oldRows, oldCols := len(old), 0
	if oldRows > 0 {
		oldCols = len(old[0])
	}
	cells := make([][]rune, rows)
	for i := range cells {
		cells[i] = make([]rune, cols)
		for j := range cells[i] {
			cells[i][j] = ' '
		}
	}
	copyRows := min(oldRows, rows)
	copyCols := min(oldCols, cols)
	rowOffsetOld := max(0, oldRows-copyRows)
	for r := 0; r < copyRows; r++ {
		copy(cells[r], old[rowOffsetOld+r][:copyCols])
	}
	t.cells = cells
	t.cols = cols
	t.rows = rows
	if t.cursorRow >= rows {
		t.cursorRow = rows - 1
	}
	if t.cursorCol >= cols {
		t.cursorCol = cols - 1
	}
}

func (t *terminalBuffer) clearAll() {
	for r := 0; r < t.rows; r++ {
		for c := 0; c < t.cols; c++ {
			t.cells[r][c] = ' '
		}
	}
	t.cursorRow = 0
	t.cursorCol = 0
}

func (t *terminalBuffer) scrollUp() {
	if t.rows <= 0 {
		return
	}
	copy(t.cells[0:], t.cells[1:])
	t.cells[t.rows-1] = make([]rune, t.cols)
	for i := range t.cells[t.rows-1] {
		t.cells[t.rows-1][i] = ' '
	}
}

func (t *terminalBuffer) lineFeed() {
	if t.cursorRow >= t.rows-1 {
		t.scrollUp()
	} else {
		t.cursorRow++
	}
}

func (t *terminalBuffer) putRune(r rune) {
	if t.cols == 0 || t.rows == 0 {
		return
	}
	if t.cursorCol >= t.cols {
		t.cursorCol = 0
		t.lineFeed()
	}
	t.cells[t.cursorRow][t.cursorCol] = r
	t.cursorCol++
	if t.cursorCol >= t.cols {
		t.cursorCol = 0
		t.lineFeed()
	}
}

func (t *terminalBuffer) clearLine(mode int) {
	if t.rows == 0 || t.cols == 0 {
		return
	}
	switch mode {
	case 1:
		for c := 0; c <= min(t.cursorCol, t.cols-1); c++ {
			t.cells[t.cursorRow][c] = ' '
		}
	case 2:
		for c := 0; c < t.cols; c++ {
			t.cells[t.cursorRow][c] = ' '
		}
	default:
		for c := min(t.cursorCol, t.cols-1); c < t.cols; c++ {
			t.cells[t.cursorRow][c] = ' '
		}
	}
}

func (t *terminalBuffer) clearScreen(mode int) {
	switch mode {
	case 1:
		for r := 0; r <= t.cursorRow; r++ {
			end := t.cols
			if r == t.cursorRow {
				end = min(t.cursorCol+1, t.cols)
			}
			for c := 0; c < end; c++ {
				t.cells[r][c] = ' '
			}
		}
	case 2, 3:
		t.clearAll()
	default:
		for r := t.cursorRow; r < t.rows; r++ {
			start := 0
			if r == t.cursorRow {
				start = min(t.cursorCol, t.cols-1)
			}
			for c := start; c < t.cols; c++ {
				t.cells[r][c] = ' '
			}
		}
	}
}

func (t *terminalBuffer) setCursor(row, col int) {
	if t.rows == 0 || t.cols == 0 {
		return
	}
	if row < 0 {
		row = 0
	}
	if col < 0 {
		col = 0
	}
	if row >= t.rows {
		row = t.rows - 1
	}
	if col >= t.cols {
		col = t.cols - 1
	}
	t.cursorRow = row
	t.cursorCol = col
}

func parseCSIParams(body string) []int {
	if body == "" || body == "?" {
		return []int{}
	}
	body = strings.TrimPrefix(body, "?")
	parts := strings.Split(body, ";")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			out = append(out, 0)
			continue
		}
		var v int
		_, err := fmt.Sscanf(p, "%d", &v)
		if err != nil {
			out = append(out, 0)
			continue
		}
		out = append(out, v)
	}
	return out
}

func csiDefault(params []int, idx, def int) int {
	if idx >= len(params) || params[idx] == 0 {
		return def
	}
	return params[idx]
}

func (t *terminalBuffer) handleCSI(seq string) {
	if seq == "" {
		return
	}
	final := seq[len(seq)-1]
	body := seq[:len(seq)-1]
	params := parseCSIParams(body)
	switch final {
	case 'A':
		t.setCursor(t.cursorRow-csiDefault(params, 0, 1), t.cursorCol)
	case 'B':
		t.setCursor(t.cursorRow+csiDefault(params, 0, 1), t.cursorCol)
	case 'C':
		t.setCursor(t.cursorRow, t.cursorCol+csiDefault(params, 0, 1))
	case 'D':
		t.setCursor(t.cursorRow, t.cursorCol-csiDefault(params, 0, 1))
	case 'G':
		t.setCursor(t.cursorRow, csiDefault(params, 0, 1)-1)
	case 'H', 'f':
		t.setCursor(csiDefault(params, 0, 1)-1, csiDefault(params, 1, 1)-1)
	case 'J':
		t.clearScreen(csiDefault(params, 0, 0))
	case 'K':
		t.clearLine(csiDefault(params, 0, 0))
	case 'P':
		n := csiDefault(params, 0, 1)
		for c := t.cursorCol; c < t.cols; c++ {
			src := c + n
			if src < t.cols {
				t.cells[t.cursorRow][c] = t.cells[t.cursorRow][src]
			} else {
				t.cells[t.cursorRow][c] = ' '
			}
		}
	case '@':
		n := csiDefault(params, 0, 1)
		for c := t.cols - 1; c >= t.cursorCol; c-- {
			src := c - n
			if src >= t.cursorCol {
				t.cells[t.cursorRow][c] = t.cells[t.cursorRow][src]
			} else {
				t.cells[t.cursorRow][c] = ' '
			}
		}
	case 's':
		t.savedRow, t.savedCol = t.cursorRow, t.cursorCol
	case 'u':
		t.setCursor(t.savedRow, t.savedCol)
	case 'h', 'l':
		if strings.HasPrefix(body, "?") {
			if strings.TrimPrefix(body, "?") == "25" {
				t.cursorVisible = final == 'h'
			}
		}
	case 'm':
		return
	}
}

func (t *terminalBuffer) feed(data string) {
	for i := 0; i < len(data); i++ {
		b := data[i]
		switch t.state {
		case 1:
			switch b {
			case '[':
				t.state = 2
				t.csiBuf.Reset()
			case ']':
				t.state = 3
				t.oscEsc = false
			case '7':
				t.savedRow, t.savedCol = t.cursorRow, t.cursorCol
				t.state = 0
			case '8':
				t.setCursor(t.savedRow, t.savedCol)
				t.state = 0
			case 'c':
				t.clearAll()
				t.state = 0
			default:
				t.state = 0
			}
			continue
		case 2:
			t.csiBuf.WriteByte(b)
			if b >= 0x40 && b <= 0x7e {
				t.handleCSI(t.csiBuf.String())
				t.csiBuf.Reset()
				t.state = 0
			}
			continue
		case 3:
			if b == 0x07 {
				t.state = 0
				t.oscEsc = false
				continue
			}
			if t.oscEsc && b == '\\' {
				t.state = 0
				t.oscEsc = false
				continue
			}
			t.oscEsc = b == 0x1b
			continue
		}
		switch b {
		case 0x1b:
			t.state = 1
		case '\r':
			t.cursorCol = 0
		case '\n':
			t.lineFeed()
		case '\b':
			if t.cursorCol > 0 {
				t.cursorCol--
			}
		case '\t':
			nextTab := ((t.cursorCol / 8) + 1) * 8
			for t.cursorCol < nextTab {
				t.putRune(' ')
			}
		default:
			if b >= 32 {
				t.putRune(rune(b))
			}
		}
	}
}

func (t *terminalBuffer) viewLines() []string {
	if t == nil || t.rows == 0 || t.cols == 0 {
		return []string{}
	}
	lines := make([]string, 0, t.rows)
	for r := 0; r < t.rows; r++ {
		var b strings.Builder
		for c := 0; c < t.cols; c++ {
			ch := t.cells[r][c]
			if ch == 0 {
				ch = ' '
			}
			cell := string(ch)
			if t.cursorVisible && r == t.cursorRow && c == t.cursorCol {
				cell = cursorStyle.Render(cell)
			}
			b.WriteString(cell)
		}
		if t.cursorVisible && r == t.cursorRow && t.cursorCol >= t.cols {
			b.WriteString(cursorStyle.Render(" "))
		}
		lines = append(lines, b.String())
	}
	return lines
}

type model struct {
	width           int
	height          int
	ready           bool
	dockerChecked   bool
	dockerOK        bool
	dockerErr       string
	focus           focusArea
	mode            viewMode
	resource        resourceMode
	filter          filterMode
	containers      []Container
	filtered        []Container
	volumes         []Volume
	filteredVols    []Volume
	cursor          int
	search          textinput.Model
	logSearch       textinput.Model
	cmdInput        textinput.Model
	snippetInput    textinput.Model
	snippetSearch   textinput.Model
	shellInput      textinput.Model
	commandMode     bool
	snippetMode     bool
	snippetRun      bool
	snippetBrowse   bool
	showHelp        bool
	markMode        bool
	marked          map[string]bool
	snippets        map[string][]string
	filteredSnips   []string
	snippetCursor   int
	snippetMarked   map[string]bool
	status          string
	lastOutput      string
	logContent      string
	logCursor       int
	logXOffset      int
	logSearchActive bool
	logsFullscreen  bool
	logSelecting    bool
	logSelectionOn  bool
	logSelStartLine int
	logSelStartCol  int
	logSelEndLine   int
	logSelEndCol    int
	followLogs      bool
	activeLogID     string
	activeLogName   string
	shellOutput     string
	shellSession    *shellSession
	shellTerm       *terminalBuffer
	suggestionIx    int
}

func main() {
	m := initialModel()
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func initialModel() model {
	search := textinput.New()
	search.Placeholder = "fuzzy search containers"
	search.CharLimit = 128
	search.Width = 40

	logSearch := textinput.New()
	logSearch.Placeholder = "search logs"
	logSearch.CharLimit = 128
	logSearch.Width = 28

	cmdInput := textinput.New()
	cmdInput.Placeholder = "docker ..."
	cmdInput.CharLimit = 256
	cmdInput.SetValue("docker ")
	cmdInput.Width = 60

	snippetInput := textinput.New()
	snippetInput.Placeholder = "snippet name"
	snippetInput.CharLimit = 64
	snippetInput.Width = 30

	snippetSearch := textinput.New()
	snippetSearch.Placeholder = "fuzzy search snippets"
	snippetSearch.CharLimit = 128
	snippetSearch.Width = 36

	shellInput := textinput.New()
	shellInput.Placeholder = "pty shell active"
	shellInput.CharLimit = 256
	shellInput.Width = 60

	snippets := loadSnippets()
	filteredSnips := snippetNames(snippets, "")

	return model{
		focus:         focusList,
		mode:          viewMain,
		resource:      resourceContainers,
		filter:        filterAll,
		search:        search,
		logSearch:     logSearch,
		cmdInput:      cmdInput,
		snippetInput:  snippetInput,
		snippetSearch: snippetSearch,
		shellInput:    shellInput,
		marked:        map[string]bool{},
		snippets:      snippets,
		filteredSnips: filteredSnips,
		snippetMarked: map[string]bool{},
		status:        "loading docker status...",
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(checkDockerCmd(), refreshContainersCmd(), refreshVolumesCmd(), tickCmd())
}

func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func checkDockerCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "docker", "info")
		if err := cmd.Run(); err != nil {
			return dockerStatusMsg{ok: false, err: errors.New("docker service is not running or docker CLI is unavailable")}
		}
		return dockerStatusMsg{ok: true}
	}
}

func refreshContainersCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "docker", "ps", "-a", "--format", "{{json .}}")
		out, err := cmd.Output()
		if err != nil {
			return containersMsg{ok: false, err: err}
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		items := make([]Container, 0, len(lines))
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var c Container
			if err := json.Unmarshal([]byte(line), &c); err == nil {
				items = append(items, c)
			}
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].State == items[j].State {
				return items[i].Names < items[j].Names
			}
			return items[i].State == "running"
		})
		return containersMsg{items: items, ok: true}
	}
}

func refreshVolumesCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "docker", "volume", "ls", "--format", "{{json .}}")
		out, err := cmd.Output()
		if err != nil {
			return volumesMsg{err: err}
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		items := make([]Volume, 0, len(lines))
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var v Volume
			if err := json.Unmarshal([]byte(line), &v); err == nil {
				items = append(items, v)
			}
		}
		sort.Slice(items, func(i, j int) bool {
			return items[i].Name < items[j].Name
		})
		return volumesMsg{items: items}
	}
}

func fetchLogsCmd(id string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "docker", "logs", "--tail", "200", id)
		out, err := cmd.CombinedOutput()
		return logsMsg{containerID: id, content: string(out), err: err}
	}
}

func runCommandCmd(command string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "sh", "-lc", command)
		out, err := cmd.CombinedOutput()
		return cmdResultMsg{output: strings.TrimSpace(string(out)), err: err}
	}
}

func startShellSessionCmd(containerID, name string, width, height int) tea.Cmd {
	return func() tea.Msg {
		sessionID := newShellSessionID()
		cmd := exec.Command("docker", "exec", "-u", "0", "-it", containerID, "sh")
		ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(max(20, width)), Rows: uint16(max(8, height))})
		if err != nil {
			cmd = exec.Command("docker", "exec", "-u", "0", "-it", containerID, "bash")
			ptmx, err = pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(max(20, width)), Rows: uint16(max(8, height))})
			if err != nil {
				return shellStartedMsg{err: err}
			}
		}
		return shellStartedMsg{session: &shellSession{id: sessionID, cmd: cmd, pty: ptmx, name: name}}
	}
}

func readShellChunkCmd(session *shellSession) tea.Cmd {
	return func() tea.Msg {
		buf := make([]byte, 4096)
		n, err := session.pty.Read(buf)
		if n > 0 {
			return shellChunkMsg{sessionID: session.id, data: string(buf[:n])}
		}
		if err != nil {
			return shellClosedMsg{sessionID: session.id, err: err}
		}
		return nil
	}
}

func writeShellInputCmd(session *shellSession, data string) tea.Cmd {
	return func() tea.Msg {
		if session == nil || session.pty == nil {
			return nil
		}
		_, err := io.WriteString(session.pty, data)
		if err != nil {
			return shellClosedMsg{sessionID: session.id, err: err}
		}
		return nil
	}
}

func closeShellSession(session *shellSession) {
	if session == nil {
		return
	}
	if session.pty != nil {
		_ = session.pty.Close()
	}
	if session.cmd != nil && session.cmd.Process != nil {
		_ = session.cmd.Process.Kill()
		_, _ = session.cmd.Process.Wait()
	}
}

func resizeShellSession(session *shellSession, width, height int) {
	if session == nil || session.pty == nil {
		return
	}
	_ = pty.Setsize(session.pty, &pty.Winsize{Cols: uint16(max(20, width)), Rows: uint16(max(8, height))})
}

func shellClosedMsgCmd(sessionID int) tea.Cmd {
	return func() tea.Msg {
		return shellClosedMsg{sessionID: sessionID}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		if m.shellSession != nil {
			resizeShellSession(m.shellSession, m.shellTerminalCols(), m.shellTerminalRows())
		}
		if m.shellTerm != nil {
			m.shellTerm.resize(m.shellTerminalCols(), m.shellTerminalRows())
		}
		m.clampLogCursor()
		m.clampLogXOffset()
		return m, nil
	case dockerStatusMsg:
		m.dockerChecked = true
		m.dockerOK = msg.ok
		if msg.err != nil {
			m.dockerErr = msg.err.Error()
			m.status = m.dockerErr
		} else {
			m.dockerErr = ""
		}
		return m, nil
	case containersMsg:
		if msg.err != nil {
			m.dockerChecked = true
			m.dockerOK = false
			m.dockerErr = "docker service is not running"
			m.status = m.dockerErr
			return m, nil
		}
		m.dockerChecked = true
		m.dockerOK = true
		m.dockerErr = ""
		m.containers = msg.items
		m.applyFilter()
		m.clampCursor()
		m.status = fmt.Sprintf("loaded %d containers", len(m.containers))
		return m, nil
	case volumesMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("volume load failed: %v", msg.err)
			return m, nil
		}
		m.dockerChecked = true
		m.dockerOK = true
		m.dockerErr = ""
		m.volumes = msg.items
		m.applyFilter()
		m.clampCursor()
		m.status = fmt.Sprintf("loaded %d containers • %d volumes", len(m.containers), len(m.volumes))
		return m, nil
	case logsMsg:
		if msg.err != nil && strings.TrimSpace(msg.content) == "" {
			m.status = fmt.Sprintf("logs error: %v", msg.err)
		} else {
			m.logContent = stripANSI(msg.content)
			m.activeLogID = msg.containerID
			if m.followLogs {
				m.logCursor = 1 << 30
			}
			m.clampLogCursor()
			m.clampLogXOffset()
			m.status = "logs refreshed"
		}
		return m, nil
	case shellStartedMsg:
		if msg.err != nil {
			m.mode = viewMain
			m.status = fmt.Sprintf("shell open failed: %v", msg.err)
			return m, nil
		}
		m.shellSession = msg.session
		m.shellOutput = ""
		m.shellTerm = newTerminalBuffer(m.shellTerminalCols(), m.shellTerminalRows())
		m.status = fmt.Sprintf("shell opened for %s • ctrl+q to close", m.activeLogName)
		resizeShellSession(m.shellSession, m.shellTerminalCols(), m.shellTerminalRows())
		return m, tea.Batch(readShellChunkCmd(m.shellSession), tea.HideCursor)
	case shellChunkMsg:
		if m.shellSession == nil || m.shellSession.id != msg.sessionID {
			return m, nil
		}
		if m.shellTerm == nil {
			m.shellTerm = newTerminalBuffer(m.shellTerminalCols(), m.shellTerminalRows())
		}
		m.shellTerm.feed(msg.data)
		return m, readShellChunkCmd(m.shellSession)
	case shellClosedMsg:
		if m.shellSession == nil || m.shellSession.id != msg.sessionID {
			return m, nil
		}
		closeShellSession(m.shellSession)
		m.shellSession = nil
		m.shellTerm = nil
		m.mode = viewMain
		if msg.err != nil && !errors.Is(msg.err, io.EOF) {
			m.status = fmt.Sprintf("shell closed: %v", msg.err)
		} else {
			m.status = fmt.Sprintf("shell closed for %s", m.activeLogName)
		}
		return m, tea.ShowCursor
	case cmdResultMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("command failed: %v", msg.err)
		} else {
			m.status = "command completed"
		}
		m.lastOutput = msg.output
		return m, tea.Batch(refreshContainersCmd(), refreshVolumesCmd(), checkDockerCmd())
	case tickMsg:
		if m.mode == viewLogs && m.followLogs && m.activeLogID != "" && m.dockerOK {
			return m, tea.Batch(fetchLogsCmd(m.activeLogID), tickCmd())
		}
		return m, tickCmd()
	case tea.MouseMsg:
		return m.handleMouse(msg)
	case tea.KeyMsg:
		if m.commandMode {
			return m.handleCommandMode(msg)
		}
		if m.snippetMode {
			return m.handleSnippetMode(msg)
		}
		if m.snippetBrowse {
			return m.handleSnippetBrowser(msg)
		}
		if m.mode == viewLogs && m.logSearchActive {
			return m.handleLogSearchMode(msg)
		}
		if m.mode == viewShell {
			return m.handleShellMode(msg)
		}
		if !m.dockerOK {
			switch msg.String() {
			case "q", "ctrl+c":
				return m, tea.Quit
			case "r":
				return m, tea.Batch(checkDockerCmd(), refreshContainersCmd(), refreshVolumesCmd())
			case "?":
				m.showHelp = !m.showHelp
				return m, nil
			}
			return m, nil
		}

		switch msg.String() {
		case "?":
			m.showHelp = !m.showHelp
			return m, nil
		case "q", "ctrl+c":
			return m, tea.Quit
		case "/":
			if m.mode == viewLogs {
				m.logSearchActive = true
				m.focus = focusLogSearch
				m.logSearch.Focus()
				m.status = "logs search active"
				return m, tea.ShowCursor
			}
			m.focus = focusSearch
			m.search.Focus()
			return m, nil
		case "esc":
			m.focus = focusList
			m.search.Blur()
			if m.mode == viewLogs {
				m.logSearchActive = false
				m.logSearch.SetValue("")
				m.logSearch.Blur()
				m.clearLogSelection()
				m.mode = viewMain
				return m, tea.ShowCursor
			}
			m.mode = viewMain
			return m, nil
		case ":":
			m.commandMode = true
			m.focus = focusCommand
			m.cmdInput.SetValue("docker ")
			m.cmdInput.CursorEnd()
			m.cmdInput.Focus()
			m.suggestionIx = 0
			return m, nil
		case "tab":
			if m.resource == resourceContainers {
				if m.filter == filterAll {
					m.filter = filterRunning
				} else {
					m.filter = filterAll
				}
				m.applyFilter()
				m.clampCursor()
			}
			return m, nil
		case "v":
			if m.mode == viewLogs {
				return m, nil
			}
			if m.resource == resourceContainers {
				m.resource = resourceVolumes
			} else {
				m.resource = resourceContainers
			}
			m.applyFilter()
			m.cursor = 0
			return m, nil
		case "h", "left":
			if m.mode == viewLogs {
				m.logXOffset -= 8
				m.clampLogXOffset()
				return m, nil
			}
		case "j", "down":
			if m.mode == viewLogs {
				m.followLogs = false
				m.logCursor++
				m.clampLogCursor()
				return m, nil
			}
			if m.cursor < m.itemCount()-1 {
				m.cursor++
			}
			return m, nil
		case "k", "up":
			if m.mode == viewLogs {
				m.followLogs = false
				if m.logCursor > 0 {
					m.logCursor--
				}
				m.clampLogCursor()
				return m, nil
			}
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil
		case "g":
			m.cursor = 0
			if m.mode == viewLogs {
				m.followLogs = false
				m.logCursor = 0
				return m, nil
			}
			m.logCursor = 0
			return m, nil
		case "G":
			if m.mode == viewLogs {
				m.followLogs = false
				m.logCursor = m.maxLogStart()
				return m, nil
			}
			if m.itemCount() > 0 {
				m.cursor = m.itemCount() - 1
			}
			return m, nil
		case "ctrl+d", "pgdown":
			if m.mode == viewLogs {
				m.followLogs = false
				m.logCursor += max(1, m.logsViewportLines()/2)
				m.clampLogCursor()
				return m, nil
			}
		case "ctrl+u", "pgup":
			if m.mode == viewLogs {
				m.followLogs = false
				m.logCursor -= max(1, m.logsViewportLines()/2)
				m.clampLogCursor()
				return m, nil
			}
		case "n":
			if m.mode == viewLogs && strings.TrimSpace(m.logSearch.Value()) != "" {
				m.followLogs = false
				m.logCursor++
				m.clampLogCursor()
				return m, nil
			}
		case "y":
			if m.mode == viewLogs {
				selected := m.logSelectionText()
				if selected == "" {
					m.status = "no selected logs to copy"
					return m, nil
				}
				if err := writeClipboardText(selected); err != nil {
					m.status = fmt.Sprintf("clipboard copy failed: %v", err)
				} else {
					m.status = fmt.Sprintf("copied %d chars from logs to clipboard", runeWidth(selected))
				}
				return m, nil
			}
		case "c":
			if m.mode == viewLogs {
				m.clearLogSelection()
				m.status = "log selection cleared"
				return m, nil
			}
		case "N":
			if m.mode == viewLogs && strings.TrimSpace(m.logSearch.Value()) != "" {
				m.followLogs = false
				m.logCursor--
				m.clampLogCursor()
				return m, nil
			}
		case "r":
			return m, tea.Batch(checkDockerCmd(), refreshContainersCmd(), refreshVolumesCmd())
		case " ", "space":
			if m.resource == resourceContainers {
				if c, ok := m.currentContainer(); ok {
					m.marked[c.ID] = !m.marked[c.ID]
					if !m.marked[c.ID] {
						delete(m.marked, c.ID)
						m.status = fmt.Sprintf("unselected %s", c.Names)
					} else {
						m.status = fmt.Sprintf("selected %s", c.Names)
					}
				}
			}
			return m, nil
		case "a":
			if m.resource == resourceContainers {
				ids := []string{}
				for _, c := range m.filtered {
					ids = append(ids, c.ID)
				}
				m.toggleAllMarked(ids)
			}
			return m, nil
		case "s":
			if m.resource != resourceContainers {
				return m, nil
			}
			ids := m.selectedIDs()
			if len(ids) == 0 {
				ids = m.runningIDs()
			}
			if len(ids) == 0 {
				m.status = "no containers selected/running"
				return m, nil
			}
			return m, runCommandCmd("docker stop " + strings.Join(ids, " "))
		case "S":
			if m.resource != resourceContainers {
				return m, nil
			}
			if len(m.selectedIDs()) == 0 {
				m.status = "select containers first to save a snippet"
				return m, nil
			}
			m.snippetMode = true
			m.snippetRun = false
			m.focus = focusSnippet
			m.snippetInput.SetValue("")
			m.snippetInput.Placeholder = "snippet name"
			m.snippetInput.Focus()
			return m, nil
		case "x":
			if m.resource != resourceContainers {
				return m, nil
			}
			ids := m.selectedIDs()
			if len(ids) == 0 {
				ids = m.allIDs()
			}
			if len(ids) == 0 {
				m.status = "no containers to start"
				return m, nil
			}
			return m, runCommandCmd("docker start " + strings.Join(ids, " "))
		case "l", "right":
			if m.mode == viewLogs {
				m.logXOffset += 8
				m.clampLogXOffset()
				return m, nil
			}
			if m.resource != resourceContainers {
				return m, nil
			}
			if c, ok := m.currentContainer(); ok {
				m.mode = viewLogs
				m.activeLogID = c.ID
				m.activeLogName = c.Names
				m.followLogs = true
				m.logCursor = 1 << 30
				m.logXOffset = 0
				m.logsFullscreen = false
				m.clearLogSelection()
				m.logSearchActive = false
				m.logSearch.SetValue("")
				m.logSearch.Blur()
				m.status = fmt.Sprintf("showing latest logs for %s", c.Names)
				return m, tea.Batch(fetchLogsCmd(c.ID), tea.HideCursor)
			}
			return m, nil
		case "z":
			if m.mode == viewLogs {
				m.logsFullscreen = !m.logsFullscreen
				m.clampLogCursor()
				m.clampLogXOffset()
				if m.logsFullscreen {
					m.status = "logs fullscreen enabled"
				} else {
					m.status = "logs fullscreen disabled"
				}
				return m, nil
			}
		case "f":
			m.followLogs = !m.followLogs
			if m.mode == viewLogs && m.activeLogID != "" {
				if m.followLogs {
					m.logCursor = 1 << 30
				}
				return m, fetchLogsCmd(m.activeLogID)
			}
			return m, nil
		case "enter":
			if m.mode == viewLogs {
				m.logSearchActive = false
				m.logSearch.SetValue("")
				m.logSearch.Blur()
				m.clearLogSelection()
				m.mode = viewMain
				return m, tea.ShowCursor
			}
			if m.resource != resourceContainers {
				return m, nil
			}
			if c, ok := m.currentContainer(); ok {
				if c.State != "running" {
					m.status = fmt.Sprintf("container %s is not running", c.Names)
					return m, nil
				}
				m.mode = viewShell
				m.activeLogID = c.ID
				m.activeLogName = c.Names
				m.shellOutput = ""
				m.shellTerm = newTerminalBuffer(m.shellTerminalCols(), m.shellTerminalRows())
				m.status = fmt.Sprintf("opening shell for %s...", c.Names)
				return m, startShellSessionCmd(c.ID, c.Names, m.shellTerminalCols(), m.shellTerminalRows())
			}
			return m, nil
		case "p":
			if len(m.snippets) == 0 {
				m.status = "no snippets saved"
				return m, nil
			}
			m.snippetBrowse = true
			m.focus = focusSnippet
			m.applySnippetFilter()
			m.snippetSearch.Focus()
			m.status = "snippet browser opened"
			return m, nil
		}

		if m.focus == focusSearch {
			var cmd tea.Cmd
			m.search, cmd = m.search.Update(msg)
			m.applyFilter()
			return m, cmd
		}
	}

	return m, nil
}

func execIntoContainerCmd(id string) tea.Cmd {
	return nil
}

func (m model) handleCommandMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	suggestions := m.commandSuggestions()
	switch msg.String() {
	case "esc":
		m.commandMode = false
		m.focus = focusList
		m.cmdInput.Blur()
		return m, nil
	case "tab":
		if len(suggestions) > 0 {
			m.cmdInput.SetValue(suggestions[m.suggestionIx%len(suggestions)])
			m.cmdInput.CursorEnd()
			m.suggestionIx++
		}
		return m, nil
	case "enter":
		command := strings.TrimSpace(m.cmdInput.Value())
		if command == "" || command == "docker" {
			return m, nil
		}
		m.commandMode = false
		m.focus = focusList
		m.cmdInput.Blur()
		return m, runCommandCmd(command)
	}
	var cmd tea.Cmd
	m.cmdInput, cmd = m.cmdInput.Update(msg)
	m.suggestionIx = 0
	return m, cmd
}

func (m model) handleSnippetMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.snippetMode = false
		m.snippetRun = false
		m.focus = focusList
		m.snippetInput.Placeholder = "snippet name"
		m.snippetInput.Blur()
		return m, nil
	case "enter":
		value := strings.TrimSpace(m.snippetInput.Value())
		if m.snippetRun {
			parts := strings.Fields(value)
			if len(parts) != 2 {
				m.status = "use: snippet-name start|stop"
				return m, nil
			}
			name, action := parts[0], parts[1]
			ids := m.snippets[name]
			if len(ids) == 0 {
				m.status = fmt.Sprintf("snippet %q not found", name)
				return m, nil
			}
			if action != "start" && action != "stop" {
				m.status = "action must be start or stop"
				return m, nil
			}
			m.snippetMode = false
			m.snippetRun = false
			m.focus = focusList
			m.snippetInput.Placeholder = "snippet name"
			m.snippetInput.Blur()
			return m, runCommandCmd("docker " + action + " " + strings.Join(ids, " "))
		}
		name := value
		if name == "" {
			m.status = "snippet name is required"
			return m, nil
		}
		m.snippets[name] = m.selectedNames()
		m.applySnippetFilter()
		if err := saveSnippets(m.snippets); err != nil {
			m.status = fmt.Sprintf("save failed: %v", err)
		} else {
			m.status = fmt.Sprintf("snippet %q saved", name)
		}
		m.snippetMode = false
		m.snippetRun = false
		m.focus = focusList
		m.snippetInput.Placeholder = "snippet name"
		m.snippetInput.Blur()
		return m, nil
	}
	var cmd tea.Cmd
	m.snippetInput, cmd = m.snippetInput.Update(msg)
	return m, cmd
}

func (m model) handleShellMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.shellSession == nil {
		return m, nil
	}
	switch msg.Type {
	case tea.KeyCtrlQ:
		closeShellSession(m.shellSession)
		m.shellSession = nil
		m.shellTerm = nil
		m.mode = viewMain
		m.focus = focusList
		m.status = fmt.Sprintf("shell closed for %s", m.activeLogName)
		return m, tea.ShowCursor
	case tea.KeyCtrlL:
		return m, writeShellInputCmd(m.shellSession, "\f")
	case tea.KeyCtrlC:
		return m, writeShellInputCmd(m.shellSession, "\x03")
	case tea.KeySpace:
		return m, writeShellInputCmd(m.shellSession, " ")
	case tea.KeyEnter:
		return m, writeShellInputCmd(m.shellSession, "\r")
	case tea.KeyBackspace:
		return m, writeShellInputCmd(m.shellSession, "\177")
	case tea.KeyTab:
		return m, writeShellInputCmd(m.shellSession, "\t")
	case tea.KeyUp:
		return m, writeShellInputCmd(m.shellSession, "\x1b[A")
	case tea.KeyDown:
		return m, writeShellInputCmd(m.shellSession, "\x1b[B")
	case tea.KeyRight:
		return m, writeShellInputCmd(m.shellSession, "\x1b[C")
	case tea.KeyLeft:
		return m, writeShellInputCmd(m.shellSession, "\x1b[D")
	case tea.KeyEsc:
		return m, writeShellInputCmd(m.shellSession, "\x1b")
	case tea.KeyRunes:
		return m, writeShellInputCmd(m.shellSession, string(msg.Runes))
	}
	return m, nil
}

func (m model) handleSnippetBrowser(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.focus == focusSearch {
		switch msg.String() {
		case "esc":
			m.focus = focusSnippet
			m.snippetSearch.Blur()
			return m, nil
		}
		var cmd tea.Cmd
		m.snippetSearch, cmd = m.snippetSearch.Update(msg)
		m.applySnippetFilter()
		return m, cmd
	}

	switch msg.String() {
	case "q", "esc":
		m.snippetBrowse = false
		m.focus = focusList
		m.snippetSearch.Blur()
		m.status = "snippet browser closed"
		return m, nil
	case "/":
		m.focus = focusSearch
		m.snippetSearch.Focus()
		return m, nil
	case "j", "down":
		if m.snippetCursor < len(m.filteredSnips)-1 {
			m.snippetCursor++
		}
		return m, nil
	case "k", "up":
		if m.snippetCursor > 0 {
			m.snippetCursor--
		}
		return m, nil
	case "g":
		m.snippetCursor = 0
		return m, nil
	case "G":
		if len(m.filteredSnips) > 0 {
			m.snippetCursor = len(m.filteredSnips) - 1
		}
		return m, nil
	case " ", "space":
		if name, ok := m.currentSnippetName(); ok {
			m.snippetMarked[name] = !m.snippetMarked[name]
			if !m.snippetMarked[name] {
				delete(m.snippetMarked, name)
			}
		}
		return m, nil
	case "a":
		m.toggleAllSnippetMarked()
		return m, nil
	case "x":
		return m, m.runSnippetActionCmd("start")
	case "s":
		return m, m.runSnippetActionCmd("stop")
	case "d":
		return m, m.deleteSelectedSnippetsCmd(false)
	case "D":
		return m, m.deleteSelectedSnippetsCmd(true)
	}
	return m, nil
}

func (m model) handleLogSearchMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.logSearchActive = false
		m.focus = focusList
		m.logSearch.Blur()
		m.clampLogCursor()
		return m, tea.HideCursor
	case "enter":
		m.logSearchActive = false
		m.focus = focusList
		m.logSearch.Blur()
		m.clampLogCursor()
		return m, tea.HideCursor
	case "y":
		selected := m.logSelectionText()
		if selected == "" {
			m.status = "no selected logs to copy"
			return m, nil
		}
		if err := writeClipboardText(selected); err != nil {
			m.status = fmt.Sprintf("clipboard copy failed: %v", err)
		} else {
			m.status = fmt.Sprintf("copied %d chars from logs to clipboard", runeWidth(selected))
		}
		return m, nil
	case "up":
		m.followLogs = false
		if m.logCursor > 0 {
			m.logCursor--
		}
		m.clampLogCursor()
		return m, nil
	case "down":
		m.followLogs = false
		m.logCursor++
		m.clampLogCursor()
		return m, nil
	case "ctrl+d", "pgdown":
		m.followLogs = false
		m.logCursor += max(1, m.logsViewportLines()/2)
		m.clampLogCursor()
		return m, nil
	case "ctrl+u", "pgup":
		m.followLogs = false
		m.logCursor -= max(1, m.logsViewportLines()/2)
		m.clampLogCursor()
		return m, nil
	}
	var cmd tea.Cmd
	m.logSearch, cmd = m.logSearch.Update(msg)
	m.followLogs = false
	m.logCursor = 0
	m.clearLogSelection()
	m.clampLogCursor()
	m.clampLogXOffset()
	return m, cmd
}

func (m model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.mode != viewLogs {
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.followLogs = false
		if m.logCursor > 0 {
			m.logCursor -= 3
			if m.logCursor < 0 {
				m.logCursor = 0
			}
		}
		m.clampLogCursor()
		return m, nil
	case tea.MouseButtonWheelDown:
		m.followLogs = false
		m.logCursor += 3
		m.clampLogCursor()
		return m, nil
	case tea.MouseButtonLeft:
		switch msg.Action {
		case tea.MouseActionPress:
			if line, col, ok := m.mouseToLogPosition(msg.X, msg.Y); ok {
				m.logSelecting = true
				m.logSelectionOn = true
				m.logSelStartLine, m.logSelStartCol = line, col
				m.logSelEndLine, m.logSelEndCol = line, col
				m.status = "selecting logs..."
				return m, nil
			}
			m.clearLogSelection()
			return m, nil
		case tea.MouseActionMotion:
			if m.logSelecting {
				if line, col, ok := m.mouseToLogPosition(msg.X, msg.Y); ok {
					m.logSelEndLine, m.logSelEndCol = line, col
				}
				return m, nil
			}
		case tea.MouseActionRelease:
			if m.logSelecting {
				m.logSelecting = false
				if line, col, ok := m.mouseToLogPosition(msg.X, msg.Y); ok {
					m.logSelEndLine, m.logSelEndCol = line, col
				}
				selected := m.logSelectionText()
				if selected == "" {
					m.clearLogSelection()
					m.status = "log selection cleared"
				} else {
					m.status = fmt.Sprintf("selected %d chars • press y to copy", runeWidth(selected))
				}
				return m, nil
			}
		}
	}
	return m, nil
}

func (m *model) applyFilter() {
	query := strings.TrimSpace(strings.ToLower(m.search.Value()))
	out := make([]Container, 0, len(m.containers))
	for _, c := range m.containers {
		if m.filter == filterRunning && c.State != "running" {
			continue
		}
		hay := strings.ToLower(strings.Join([]string{c.ID, c.Names, c.Image, c.State, c.Status}, " "))
		if query == "" || fuzzyMatch(query, hay) {
			out = append(out, c)
		}
	}
	m.filtered = out

	vols := make([]Volume, 0, len(m.volumes))
	for _, v := range m.volumes {
		hay := strings.ToLower(strings.Join([]string{v.Name, v.Driver, v.Mountpoint, v.Scope}, " "))
		if query == "" || fuzzyMatch(query, hay) {
			vols = append(vols, v)
		}
	}
	m.filteredVols = vols
}

func fuzzyMatch(needle, haystack string) bool {
	if needle == "" {
		return true
	}
	j := 0
	for i := 0; i < len(haystack) && j < len(needle); i++ {
		if haystack[i] == needle[j] {
			j++
		}
	}
	return j == len(needle)
}

func splitLogLines(content string) []string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	if content == "" {
		return []string{}
	}
	return strings.Split(content, "\n")
}

func (m model) filteredLogLines() []string {
	lines := splitLogLines(m.logContent)
	query := strings.TrimSpace(strings.ToLower(m.logSearch.Value()))
	if query == "" {
		return lines
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, query) || fuzzyMatch(query, lower) {
			out = append(out, line)
		}
	}
	return out
}

func runeWidth(s string) int {
	return len([]rune(s))
}

func sliceRunes(s string, start, width int) string {
	r := []rune(s)
	if start < 0 {
		start = 0
	}
	if start >= len(r) {
		return ""
	}
	end := len(r)
	if width >= 0 && start+width < end {
		end = start + width
	}
	return string(r[start:end])
}

func minMax(a, b int) (int, int) {
	if a <= b {
		return a, b
	}
	return b, a
}

func (m *model) clearLogSelection() {
	m.logSelecting = false
	m.logSelectionOn = false
	m.logSelStartLine = 0
	m.logSelStartCol = 0
	m.logSelEndLine = 0
	m.logSelEndCol = 0
}

func (m model) normalizedLogSelection() (startLine, startCol, endLine, endCol int, ok bool) {
	if !m.logSelectionOn {
		return 0, 0, 0, 0, false
	}
	startLine, endLine = minMax(m.logSelStartLine, m.logSelEndLine)
	if m.logSelStartLine < m.logSelEndLine {
		startCol, endCol = m.logSelStartCol, m.logSelEndCol
	} else if m.logSelStartLine > m.logSelEndLine {
		startCol, endCol = m.logSelEndCol, m.logSelStartCol
	} else {
		startCol, endCol = minMax(m.logSelStartCol, m.logSelEndCol)
	}
	return startLine, startCol, endLine, endCol, true
}

func (m model) logSelectionText() string {
	startLine, startCol, endLine, endCol, ok := m.normalizedLogSelection()
	if !ok {
		return ""
	}
	lines := m.filteredLogLines()
	if len(lines) == 0 || startLine >= len(lines) {
		return ""
	}
	if endLine >= len(lines) {
		endLine = len(lines) - 1
	}
	out := make([]string, 0, endLine-startLine+1)
	for i := startLine; i <= endLine; i++ {
		line := strings.ReplaceAll(lines[i], "\t", "    ")
		r := []rune(line)
		lineStart := 0
		lineEnd := len(r)
		if i == startLine {
			lineStart = min(max(startCol, 0), len(r))
		}
		if i == endLine {
			lineEnd = min(max(endCol, 0), len(r))
		}
		if startLine == endLine && endCol < startCol {
			lineStart, lineEnd = lineEnd, lineStart
		}
		if lineStart > lineEnd {
			lineStart, lineEnd = lineEnd, lineStart
		}
		out = append(out, string(r[lineStart:lineEnd]))
	}
	return strings.Join(out, "\n")
}

func (m model) logLineSelectedRange(lineIndex int, visibleStart, visibleEnd int) (int, int, bool) {
	startLine, startCol, endLine, endCol, ok := m.normalizedLogSelection()
	if !ok || lineIndex < startLine || lineIndex > endLine {
		return 0, 0, false
	}
	lineSelStart := 0
	lineSelEnd := 1 << 30
	if lineIndex == startLine {
		lineSelStart = startCol
	}
	if lineIndex == endLine {
		lineSelEnd = endCol
	}
	if lineSelEnd < lineSelStart {
		lineSelStart, lineSelEnd = lineSelEnd, lineSelStart
	}
	start := max(visibleStart, lineSelStart) - visibleStart
	end := min(visibleEnd, lineSelEnd) - visibleStart
	if end <= start {
		return 0, 0, false
	}
	return start, end, true
}

func (m model) logsViewportLines() int {
	return max(1, m.shellPaneHeight()-6)
}

func (m model) logsPanelWidth() int {
	if m.logsFullscreen {
		return max(60, m.width-6)
	}
	return max(36, m.width/3-8)
}

func (m model) logsContentWidth() int {
	return max(10, m.logsPanelWidth()-6)
}

func (m model) maxLogWidth() int {
	maxWidth := 0
	for _, line := range m.filteredLogLines() {
		w := runeWidth(strings.ReplaceAll(line, "\t", "    "))
		if w > maxWidth {
			maxWidth = w
		}
	}
	return maxWidth
}

func (m model) maxLogXOffset() int {
	return max(0, m.maxLogWidth()-m.logsContentWidth())
}

func (m model) maxLogStart() int {
	lines := m.filteredLogLines()
	return max(0, len(lines)-m.logsViewportLines())
}

func (m *model) clampLogCursor() {
	maxStart := m.maxLogStart()
	if m.logCursor < 0 {
		m.logCursor = 0
	}
	if m.logCursor > maxStart {
		m.logCursor = maxStart
	}
}

func (m *model) clampLogXOffset() {
	maxOffset := m.maxLogXOffset()
	if m.logXOffset < 0 {
		m.logXOffset = 0
	}
	if m.logXOffset > maxOffset {
		m.logXOffset = maxOffset
	}
}

func (m model) logPanelContentRect() (x, y, width, height int) {
	appLeft := 2
	appTop := 1
	headerLines := 2
	contentTop := appTop + headerLines
	panelX := appLeft
	if !m.logsFullscreen {
		panelX += max(60, m.width*2/3)
	}
	panelY := contentTop
	contentX := panelX + 2
	contentY := panelY + 5
	return contentX, contentY, m.logsContentWidth(), m.logsViewportLines()
}

func (m model) mouseToLogPosition(mouseX, mouseY int) (lineIndex, col int, ok bool) {
	x, y, width, height := m.logPanelContentRect()
	if mouseX < x || mouseY < y || mouseX >= x+width || mouseY >= y+height {
		return 0, 0, false
	}
	lineIndex = m.logCursor + (mouseY - y)
	col = m.logXOffset + (mouseX - x)
	lines := m.filteredLogLines()
	if lineIndex < 0 || lineIndex >= len(lines) {
		return 0, 0, false
	}
	line := strings.ReplaceAll(lines[lineIndex], "\t", "    ")
	maxCol := runeWidth(line)
	if col > maxCol {
		col = maxCol
	}
	if col < 0 {
		col = 0
	}
	return lineIndex, col, true
}

func writeClipboardText(text string) error {
	if text == "" {
		return errors.New("nothing selected")
	}
	if err := clipboard.WriteAll(text); err == nil {
		return nil
	}
	commands := [][]string{}
	if isWSL() {
		commands = append(commands, []string{"clip.exe"})
	}
	commands = append(commands,
		[]string{"pbcopy"},
		[]string{"wl-copy"},
		[]string{"xclip", "-selection", "clipboard"},
		[]string{"xsel", "--clipboard", "--input"},
	)
	var lastErr error
	for _, c := range commands {
		path, err := exec.LookPath(c[0])
		if err != nil {
			lastErr = err
			continue
		}
		cmd := exec.Command(path, c[1:]...)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return errors.New("clipboard unavailable")
}

func isWSL() bool {
	if os.Getenv("WSL_DISTRO_NAME") != "" || os.Getenv("WSL_INTEROP") != "" {
		return true
	}
	b, err := os.ReadFile("/proc/version")
	if err == nil {
		lower := strings.ToLower(string(b))
		if strings.Contains(lower, "microsoft") || strings.Contains(lower, "wsl") {
			return true
		}
	}
	return false
}

func highlightLogLine(line, query string) string {
	query = strings.TrimSpace(query)
	if query == "" {
		return line
	}
	lowerLine := strings.ToLower(line)
	lowerQuery := strings.ToLower(query)
	idx := strings.Index(lowerLine, lowerQuery)
	if idx < 0 {
		return line
	}
	end := idx + len(lowerQuery)
	if idx > len(line) {
		return line
	}
	if end > len(line) {
		end = len(line)
	}
	return line[:idx] + matchStyle.Render(line[idx:end]) + line[end:]
}

func colorLogLine(raw, rendered string) string {
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, "fatal") || strings.Contains(lower, "panic") || strings.Contains(lower, "error"):
		return logErrorStyle.Render(rendered)
	case strings.Contains(lower, "warn"):
		return logWarnStyle.Render(rendered)
	case strings.Contains(lower, "info"):
		return logInfoStyle.Render(rendered)
	case strings.Contains(lower, "debug"):
		return logDebugStyle.Render(rendered)
	case strings.Contains(lower, "trace"):
		return logTraceStyle.Render(rendered)
	default:
		return rendered
	}
}

func renderLogLine(line, query string, xOffset, width, lineIndex int, m model) string {
	rawLine := strings.ReplaceAll(line, "\t", "    ")
	visible := sliceRunes(rawLine, xOffset, width)
	visibleStart := xOffset
	visibleEnd := xOffset + runeWidth(visible)
	query = strings.TrimSpace(query)
	queryLower := strings.ToLower(query)
	visibleRunes := []rune(visible)
	queryStart, queryEnd := -1, -1
	if query != "" {
		lowerVisible := strings.ToLower(visible)
		if idx := strings.Index(lowerVisible, queryLower); idx >= 0 {
			queryStart = runeWidth(visible[:idx])
			queryEnd = queryStart + runeWidth(query)
		}
	}
	selStart, selEnd, hasSel := m.logLineSelectedRange(lineIndex, visibleStart, visibleEnd)
	var b strings.Builder
	for i, r := range visibleRunes {
		piece := string(r)
		switch {
		case hasSel && i >= selStart && i < selEnd:
			piece = logSelectionStyle.Render(piece)
		case queryStart >= 0 && i >= queryStart && i < queryEnd:
			piece = matchStyle.Render(piece)
		default:
			piece = colorLogLine(rawLine, piece)
		}
		b.WriteString(piece)
	}
	return b.String()
}

func (m *model) clampCursor() {
	count := m.itemCount()
	if count == 0 {
		m.cursor = 0
		return
	}
	if m.cursor >= count {
		m.cursor = count - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m model) itemCount() int {
	if m.resource == resourceVolumes {
		return len(m.filteredVols)
	}
	return len(m.filtered)
}

func (m model) currentContainer() (Container, bool) {
	if m.cursor < 0 || m.cursor >= len(m.filtered) {
		return Container{}, false
	}
	return m.filtered[m.cursor], true
}

func (m model) currentVolume() (Volume, bool) {
	if m.cursor < 0 || m.cursor >= len(m.filteredVols) {
		return Volume{}, false
	}
	return m.filteredVols[m.cursor], true
}

func (m model) currentSnippetName() (string, bool) {
	if m.snippetCursor < 0 || m.snippetCursor >= len(m.filteredSnips) {
		return "", false
	}
	return m.filteredSnips[m.snippetCursor], true
}

func (m *model) applySnippetFilter() {
	m.filteredSnips = snippetNames(m.snippets, strings.TrimSpace(strings.ToLower(m.snippetSearch.Value())))
	if m.snippetCursor >= len(m.filteredSnips) && len(m.filteredSnips) > 0 {
		m.snippetCursor = len(m.filteredSnips) - 1
	}
	if len(m.filteredSnips) == 0 {
		m.snippetCursor = 0
	}
}

func snippetNames(snippets map[string][]string, query string) []string {
	names := make([]string, 0, len(snippets))
	for name, containers := range snippets {
		hay := strings.ToLower(name + " " + strings.Join(containers, " "))
		if query == "" || fuzzyMatch(query, hay) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func (m *model) toggleAllSnippetMarked() {
	allSelected := len(m.filteredSnips) > 0
	for _, name := range m.filteredSnips {
		if !m.snippetMarked[name] {
			allSelected = false
			break
		}
	}
	for _, name := range m.filteredSnips {
		if allSelected {
			delete(m.snippetMarked, name)
		} else {
			m.snippetMarked[name] = true
		}
	}
}

func (m model) selectedSnippetNames() []string {
	names := []string{}
	for _, name := range snippetNames(m.snippets, "") {
		if m.snippetMarked[name] {
			names = append(names, name)
		}
	}
	return names
}

func (m model) snippetTargets(names []string) []string {
	set := map[string]bool{}
	out := []string{}
	for _, name := range names {
		for _, containerName := range m.snippets[name] {
			if containerName != "" && !set[containerName] {
				set[containerName] = true
				out = append(out, containerName)
			}
		}
	}
	return out
}

func (m model) runSnippetActionCmd(action string) tea.Cmd {
	names := m.selectedSnippetNames()
	if len(names) == 0 {
		if current, ok := m.currentSnippetName(); ok {
			names = []string{current}
		}
	}
	if len(names) == 0 {
		return nil
	}
	targets := m.snippetTargets(names)
	if len(targets) == 0 {
		return nil
	}
	return runCommandCmd("docker " + action + " " + strings.Join(targets, " "))
}

func (m *model) deleteSelectedSnippetsCmd(deleteAll bool) tea.Cmd {
	if deleteAll {
		m.snippets = map[string][]string{}
		m.snippetMarked = map[string]bool{}
		m.applySnippetFilter()
		m.status = "all snippets deleted"
		_ = saveSnippets(m.snippets)
		return nil
	}
	names := m.selectedSnippetNames()
	if len(names) == 0 {
		if current, ok := m.currentSnippetName(); ok {
			names = []string{current}
		}
	}
	if len(names) == 0 {
		m.status = "no snippets selected"
		return nil
	}
	for _, name := range names {
		delete(m.snippets, name)
		delete(m.snippetMarked, name)
	}
	m.applySnippetFilter()
	m.status = fmt.Sprintf("deleted %d snippet(s)", len(names))
	_ = saveSnippets(m.snippets)
	return nil
}

func (m model) selectedIDs() []string {
	ids := []string{}
	for _, c := range m.containers {
		if m.marked[c.ID] {
			ids = append(ids, c.ID)
		}
	}
	return ids
}

func (m model) selectedNames() []string {
	names := []string{}
	for _, c := range m.containers {
		if m.marked[c.ID] {
			names = append(names, c.Names)
		}
	}
	return names
}

func (m model) runningIDs() []string {
	ids := []string{}
	for _, c := range m.containers {
		if c.State == "running" {
			ids = append(ids, c.ID)
		}
	}
	return ids
}

func (m model) allIDs() []string {
	ids := []string{}
	for _, c := range m.containers {
		ids = append(ids, c.ID)
	}
	return ids
}

func (m *model) toggleAllMarked(ids []string) {
	allSelected := true
	for _, id := range ids {
		if !m.marked[id] {
			allSelected = false
			break
		}
	}
	for _, id := range ids {
		if allSelected {
			delete(m.marked, id)
		} else {
			m.marked[id] = true
		}
	}
	if allSelected {
		m.status = "selection cleared"
	} else {
		m.status = "visible containers selected"
	}
}

func (m model) commandSuggestions() []string {
	base := strings.TrimSpace(m.cmdInput.Value())
	if base == "" {
		base = "docker "
	}
	containers := m.containers
	names := []string{}
	for _, c := range containers {
		names = append(names, c.Names)
		names = append(names, c.ID)
	}
	set := map[string]bool{}
	out := []string{}
	add := func(s string) {
		if s == "" || set[s] {
			return
		}
		if strings.HasPrefix(s, base) || fuzzyMatch(strings.ToLower(strings.TrimPrefix(base, "docker ")), strings.ToLower(strings.TrimPrefix(s, "docker "))) {
			set[s] = true
			out = append(out, s)
		}
	}
	for _, s := range []string{"docker ps -a", "docker volume ls", "docker volume inspect ", "docker volume rm ", "docker start ", "docker stop ", "docker logs --tail 200 ", "docker exec -it  sh", "docker exec -it  bash", "docker inspect ", "docker rm "} {
		add(s)
	}
	for _, v := range m.volumes {
		add("docker volume inspect " + v.Name)
		add("docker volume rm " + v.Name)
	}
	for _, name := range names {
		add("docker start " + name)
		add("docker stop " + name)
		add("docker logs --tail 200 " + name)
		add("docker exec -it " + name + " sh")
		add("docker exec -it " + name + " bash")
		add("docker inspect " + name)
	}
	sort.Strings(out)
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

func (m model) View() string {
	if !m.ready {
		return "loading..."
	}
	if !m.dockerChecked {
		body := titleStyle.Render("Docker TUI") + "\n\n" + mutedStyle.Render("checking docker status...")
		if m.status != "" {
			body += "\n" + mutedStyle.Render(m.status)
		}
		body += "\n\n" + helpStyle.Render("please wait • q to quit")
		return appStyle.Render(body)
	}
	if !m.dockerOK {
		body := titleStyle.Render("Docker TUI") + "\n\n" + errorStyle.Render("docker service is not running")
		if m.dockerErr != "" {
			body += "\n" + mutedStyle.Render(m.dockerErr)
		}
		body += "\n\n" + helpStyle.Render("press r to retry • q to quit")
		return appStyle.Render(body)
	}

	header := m.renderHeader()
	left := m.renderMainPanel()
	right := m.renderSidePanel()
	content := lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	if m.mode == viewLogs && m.logsFullscreen {
		content = right
	}
	footer := m.renderFooter()

	parts := []string{header, content, footer}
	if m.commandMode {
		parts = append(parts, m.renderCommandPalette())
	}
	if m.snippetMode {
		parts = append(parts, m.renderSnippetPrompt())
	}
	if m.snippetBrowse {
		parts = append(parts, m.renderSnippetBrowser())
	}
	if m.showHelp {
		parts = append(parts, m.renderHelp())
	}
	return appStyle.Render(strings.Join(parts, "\n"))
}

func (m model) renderHeader() string {
	filter := "all"
	if m.filter == filterRunning {
		filter = "running"
	}
	resource := "containers"
	shown := len(m.filtered)
	modeLabel := resource
	if m.resource == resourceVolumes {
		resource = "volumes"
		shown = len(m.filteredVols)
		modeLabel = resource
	}
	if m.mode == viewLogs {
		modeLabel = "logs"
		shown = len(m.filteredLogLines())
	}
	if m.mode == viewShell {
		modeLabel = "shell"
	}
	title := titleStyle.Render("Docker TUI")
	stats := fmt.Sprintf("containers:%d  volumes:%d  shown:%d  selected:%d  filter:%s  view:%s", len(m.containers), len(m.volumes), shown, len(m.selectedIDs()), filter, modeLabel)
	searchLabel := "search"
	if m.focus == focusSearch {
		searchLabel = "search*"
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.JoinHorizontal(lipgloss.Center, title, "   ", mutedStyle.Render(stats)),
		fmt.Sprintf("%s: %s", searchLabel, m.search.View()),
	)
}

func (m model) renderMainPanel() string {
	if m.resource == resourceVolumes {
		return m.renderVolumes()
	}
	return m.renderContainers()
}

func (m model) renderContainers() string {
	width := max(60, m.width*2/3)
	height := max(12, m.height-12)
	lines := []string{titleStyle.Render("Containers")}
	lines = append(lines, mutedStyle.Render("mark with <space>, all-visible with a, start all with x, stop running with s • enter opens in-TUI shell • l shows logs • [x] means selected • press v for volumes"))
	lines = append(lines, fmt.Sprintf("%-4s %-12s %-20s %-10s %-24s %s", "SEL", "ID", "NAME", "STATE", "IMAGE", "STATUS"))
	for i, c := range m.visibleRows(height - 4) {
		mark := "[ ]"
		if m.marked[c.ID] {
			mark = "[x]"
		}
		state := c.State
		if c.State == "running" {
			state = runningStyle.Render(c.State)
		} else {
			state = stoppedStyle.Render(c.State)
		}
		row := fmt.Sprintf("%-4s %-12s %-20s %-18s %-24s %s", mark, trim(c.ID, 12), trim(c.Names, 20), state, trim(c.Image, 24), trim(c.Status, 28))
		actualIndex := m.rowIndexFromVisible(i, height-4)
		if actualIndex == m.cursor && (m.mode == viewMain || m.mode == viewShell || m.mode == viewLogs) {
			row = selectedStyle.Width(width - 4).Render(row)
		}
		lines = append(lines, row)
	}
	if len(m.filtered) == 0 {
		lines = append(lines, mutedStyle.Render("no containers match current filter"))
	}
	return sectionStyle.Width(width).Height(height).Render(strings.Join(lines, "\n"))
}

func (m model) renderVolumes() string {
	width := max(60, m.width*2/3)
	height := max(12, m.height-12)
	lines := []string{titleStyle.Render("Volumes")}
	lines = append(lines, mutedStyle.Render("fuzzy search volumes • press v to go back to containers"))
	lines = append(lines, fmt.Sprintf("%-24s %-12s %-10s %s", "NAME", "DRIVER", "SCOPE", "MOUNTPOINT"))
	for i, v := range m.visibleVolumeRows(height - 4) {
		row := fmt.Sprintf("%-24s %-12s %-10s %s", trim(v.Name, 24), trim(v.Driver, 12), trim(v.Scope, 10), trim(v.Mountpoint, 40))
		actualIndex := m.rowIndexFromVisible(i, height-4)
		if actualIndex == m.cursor && m.mode == viewMain {
			row = selectedStyle.Width(width - 4).Render(row)
		}
		lines = append(lines, row)
	}
	if len(m.filteredVols) == 0 {
		lines = append(lines, mutedStyle.Render("no volumes match current filter"))
	}
	return sectionStyle.Width(width).Height(height).Render(strings.Join(lines, "\n"))
}

func (m model) renderSidePanel() string {
	width := max(36, m.width/3-8)
	height := max(12, m.height-12)
	if m.mode == viewLogs {
		width = m.logsPanelWidth()
		query := strings.TrimSpace(m.logSearch.Value())
		allLines := splitLogLines(m.logContent)
		logLines := m.filteredLogLines()
		start := m.logCursor
		maxLines := m.logsViewportLines()
		if len(logLines) > maxLines {
			if start > len(logLines)-maxLines {
				start = len(logLines) - maxLines
			}
		} else {
			start = 0
		}
		end := min(len(logLines), start+maxLines)
		status := "focused • j/k or mouse wheel scroll • h/l horiz • drag select • y copy • ctrl+u/d page • g/G top/bottom • / search • n/N next/prev • z fullscreen • f follow • enter/esc back"
		if m.followLogs {
			status = "focused • following latest logs • j/k or mouse wheel scroll • h/l horiz • drag select • y copy • ctrl+u/d page • / search • z fullscreen • f follow • enter/esc back"
		}
		rangeLabel := "rows 0/0"
		if len(logLines) > 0 {
			rangeLabel = fmt.Sprintf("rows %d-%d/%d", start+1, end, len(logLines))
		}
		colLabel := "cols 0/0"
		if m.maxLogWidth() > 0 {
			colEnd := min(m.maxLogWidth(), m.logXOffset+m.logsContentWidth())
			colLabel = fmt.Sprintf("cols %d-%d/%d", m.logXOffset+1, colEnd, m.maxLogWidth())
		}
		lines := []string{
			titleStyle.Render("Logs: " + m.activeLogName),
			mutedStyle.Render(status),
			"search: " + m.logSearch.View(),
			mutedStyle.Render(fmt.Sprintf("matches: %d/%d • %s • %s", len(logLines), len(allLines), rangeLabel, colLabel)),
		}
		contentWidth := m.logsContentWidth()
		for i, line := range logLines[start:end] {
			lines = append(lines, renderLogLine(line, query, m.logXOffset, contentWidth, start+i, m))
		}
		if len(logLines) == 0 {
			if query != "" {
				lines = append(lines, mutedStyle.Render("no logs match current search"))
			} else {
				lines = append(lines, mutedStyle.Render("no logs available"))
			}
		}
		return activeSectionStyle.Width(width).Height(height).Render(strings.Join(lines, "\n"))
	}
	if m.mode == viewShell {
		lines := []string{titleStyle.Render("Shell: " + m.activeLogName), mutedStyle.Render("embedded terminal • ctrl+q close • esc passes through • ctrl+l clear inside shell")}
		if m.shellTerm == nil {
			lines = append(lines, mutedStyle.Render("starting shell..."))
		} else {
			lines = append(lines, m.shellTerm.viewLines()...)
		}
		return sectionStyle.Width(width).Height(height).Render(strings.Join(lines, "\n"))
	}

	lines := []string{titleStyle.Render("Selection & snippets")}
	if m.resource == resourceContainers {
		if c, ok := m.currentContainer(); ok {
			lines = append(lines,
				fmt.Sprintf("current: %s", c.Names),
				fmt.Sprintf("id: %s", c.ID),
				fmt.Sprintf("image: %s", c.Image),
				fmt.Sprintf("status: %s", c.Status),
				"",
				mutedStyle.Render("enter = open shell inside TUI"),
				mutedStyle.Render("l = view logs"),
				mutedStyle.Render("p = run saved snippet"),
			)
		}
	} else if v, ok := m.currentVolume(); ok {
		lines = append(lines,
			fmt.Sprintf("volume: %s", v.Name),
			fmt.Sprintf("driver: %s", v.Driver),
			fmt.Sprintf("scope: %s", v.Scope),
			fmt.Sprintf("mount: %s", trim(v.Mountpoint, 28)),
			"",
			mutedStyle.Render(": autocomplete supports volume commands"),
		)
	}
	lines = append(lines, "", snippetStyle.Render("saved snippets:"))
	names := make([]string, 0, len(m.snippets))
	for name := range m.snippets {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		lines = append(lines, mutedStyle.Render("none yet"))
	}
	for _, name := range names {
		containers := m.snippets[name]
		lines = append(lines, fmt.Sprintf("- %s (%d)", name, len(containers)))
	}
	lines = append(lines, "", titleStyle.Render("Command palette"), mutedStyle.Render(": opens docker command mode with autocomplete"), mutedStyle.Render("p opens snippet browser"))
	return sectionStyle.Width(width).Height(height).Render(strings.Join(lines, "\n"))
}

func (m model) shellPaneWidth() int {
	return max(36, m.width/3-8)
}

func (m model) shellPaneHeight() int {
	return max(12, m.height-12)
}

func (m model) shellTerminalCols() int {
	return max(20, m.shellPaneWidth()-4)
}

func (m model) shellTerminalRows() int {
	return max(4, m.shellPaneHeight()-4)
}

func (m model) renderFooter() string {
	keys := "j/k move • / search • v containers/volumes • tab running/all • space mark • a mark visible • x start all • s stop running • S save snippet • p snippet browser • : command • ? help • r refresh • q quit"
	if m.mode == viewLogs {
		keys = "logs: j/k scroll • h/l horiz • mouse wheel scroll • drag select • y copy • c clear selection • ctrl+u/d page • g/G top/bottom • / search • n/N next/prev • z fullscreen • f follow latest • enter/esc back"
	}
	status := m.status
	if strings.TrimSpace(m.lastOutput) != "" {
		status += " | " + trim(strings.ReplaceAll(m.lastOutput, "\n", " | "), max(20, m.width-10))
	}
	return helpStyle.Render(keys + "\n" + status)
}

func (m model) renderCommandPalette() string {
	lines := []string{titleStyle.Render("Docker command"), m.cmdInput.View(), mutedStyle.Render("tab autocomplete • enter run • esc cancel")}
	for _, s := range m.commandSuggestions() {
		lines = append(lines, "  "+s)
	}
	return sectionStyle.Width(max(60, m.width-6)).Render(strings.Join(lines, "\n"))
}

func (m model) renderSnippetPrompt() string {
	title := "Save snippet for selected containers"
	hint := "enter save • esc cancel"
	if m.snippetRun {
		title = "Run snippet"
		hint = "enter run • esc cancel • format: snippet-name start|stop"
	}
	lines := []string{titleStyle.Render(title), m.snippetInput.View(), mutedStyle.Render(hint)}
	return sectionStyle.Width(60).Render(strings.Join(lines, "\n"))
}

func (m model) renderSnippetBrowser() string {
	lines := []string{
		titleStyle.Render("Snippets"),
		"search: " + m.snippetSearch.View(),
		mutedStyle.Render("/ search • j/k move • space mark • a mark visible • x start • s stop • d delete selected/current • D delete all • esc close"),
	}
	if len(m.filteredSnips) == 0 {
		lines = append(lines, mutedStyle.Render("no snippets match"))
	} else {
		for i, name := range m.filteredSnips {
			mark := "[ ]"
			if m.snippetMarked[name] {
				mark = "[x]"
			}
			containers := strings.Join(m.snippets[name], ", ")
			row := fmt.Sprintf("%-4s %-20s %s", mark, trim(name, 20), trim(containers, max(20, m.width-40)))
			if i == m.snippetCursor {
				row = selectedStyle.Render(row)
			}
			lines = append(lines, row)
		}
	}
	return sectionStyle.Width(max(80, m.width-6)).Render(strings.Join(lines, "\n"))
}

func (m model) renderHelp() string {
	lines := []string{
		titleStyle.Render("Keyboard shortcuts"),
		"q                quit",
		"r                refresh containers and volumes",
		"?                toggle this help",
		"/                focus fuzzy search",
		"esc              leave search/logs/shell/popup",
		"j/k, ↑/↓         move",
		"g / G            top / bottom",
		"v                switch containers and volumes",
		"tab              toggle all/running filter (containers)",
		"space            mark current container",
		"a                mark/unmark all visible containers",
		"x                start selected containers or all containers",
		"s                stop selected containers or all running containers",
		"S                save selected containers as snippet (stores container names)",
		"p                open snippet browser with fuzzy search",
		"snippet browser   x=start, s=stop, d=delete selected/current, D=delete all",
		"enter            open PTY shell for selected running container / close logs",
		"l                open logs for selected container",
		"logs             j/k + mouse wheel scroll, h/l horizontal, ctrl+u/d page",
		"logs             g/G top/bottom, / search, n/N next/prev, z fullscreen",
		"logs             f follow latest",
		"in shell         true interactive shell, ctrl+q close, esc passes through",
		":                docker command palette with autocomplete",
		"                  includes container ids/names and volume names",
	}
	return sectionStyle.Width(max(72, m.width-6)).Render(strings.Join(lines, "\n"))
}

func (m model) visibleRows(maxRows int) []Container {
	if len(m.filtered) <= maxRows {
		return m.filtered
	}
	start := m.cursor - maxRows/2
	if start < 0 {
		start = 0
	}
	if start > len(m.filtered)-maxRows {
		start = len(m.filtered) - maxRows
	}
	return m.filtered[start : start+maxRows]
}

func (m model) visibleVolumeRows(maxRows int) []Volume {
	if len(m.filteredVols) <= maxRows {
		return m.filteredVols
	}
	start := m.cursor - maxRows/2
	if start < 0 {
		start = 0
	}
	if start > len(m.filteredVols)-maxRows {
		start = len(m.filteredVols) - maxRows
	}
	return m.filteredVols[start : start+maxRows]
}

func (m model) rowIndexFromVisible(visibleIndex, maxRows int) int {
	count := len(m.filtered)
	if m.resource == resourceVolumes {
		count = len(m.filteredVols)
	}
	if count <= maxRows {
		return visibleIndex
	}
	start := m.cursor - maxRows/2
	if start < 0 {
		start = 0
	}
	if start > count-maxRows {
		start = count - maxRows
	}
	return start + visibleIndex
}

func loadSnippets() map[string][]string {
	path := snippetFilePath()
	b, err := os.ReadFile(path)
	if err != nil {
		return map[string][]string{}
	}
	var store SnippetStore
	if err := json.Unmarshal(b, &store); err != nil || store.Snippets == nil {
		return map[string][]string{}
	}
	return store.Snippets
}

func saveSnippets(snippets map[string][]string) error {
	path := snippetFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(SnippetStore{Snippets: snippets}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func snippetFilePath() string {
	base, err := os.UserConfigDir()
	if err != nil {
		base = "."
	}
	return filepath.Join(base, "docker-tui", "snippets.json")
}

func shellEscape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			j := i + 1
			if j < len(s) && s[j] == '[' {
				j++
				for j < len(s) {
					c := s[j]
					if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
						break
					}
					j++
				}
				i = j
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func trim(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
