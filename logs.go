package main

import (
	"errors"
	"os"
	"os/exec"
	"strings"

	"github.com/atotto/clipboard"
)

func (m *model) applyFilter() {
	query := strings.TrimSpace(strings.ToLower(m.search.Value()))
	out := make([]Container, 0, len(m.containers))
	for _, c := range m.containers {
		if m.filter == filterRunning && c.State != "running" {
			continue
		}
		hay := strings.ToLower(strings.Join([]string{c.ID, c.Names, c.Image, c.State, c.Status, c.Ports, c.Memory, strings.Join(c.VolumeNames, " ")}, " "))
		if query == "" || fuzzyMatch(query, hay) {
			out = append(out, c)
		}
	}
	m.filtered = out

	vols := make([]Volume, 0, len(m.volumes))
	for _, v := range m.volumes {
		hay := strings.ToLower(strings.Join([]string{v.Name, v.Driver, v.Mountpoint, v.Scope, strings.Join(v.Containers, " ")}, " "))
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
