package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

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
	var left, right string
	if m.mode == viewVolume {
		left = m.renderVolumeBrowser()
		right = m.renderVolumeBrowserDetails()
	} else {
		left = m.renderMainPanel()
		right = m.renderSidePanel()
	}
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
	view := appStyle.Render(strings.Join(parts, "\n"))
	if m.confirm.active {
		view = m.overlayCentered(view, m.renderConfirmDialog())
	}
	if m.snippetEditor.active {
		view = m.overlayCentered(view, m.renderSnippetEditor())
	}
	return view
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
	if m.mode == viewVolume {
		modeLabel = "volume"
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

type containerTableWidths struct {
	sel    int
	id     int
	name   int
	state  int
	ports  int
	memory int
	image  int
	status int
}

type volumeTableWidths struct {
	name       int
	driver     int
	scope      int
	usedBy     int
	mountpoint int
}

func wrapToWidth(text string, width int) []string {
	text = strings.ReplaceAll(text, "\t", "    ")
	text = strings.ReplaceAll(text, "\r", "")
	if width <= 0 {
		return []string{""}
	}
	parts := strings.Split(text, "\n")
	out := []string{}
	for _, part := range parts {
		r := []rune(part)
		if len(r) == 0 {
			out = append(out, "")
			continue
		}
		for len(r) > width {
			out = append(out, string(r[:width]))
			r = r[width:]
		}
		out = append(out, string(r))
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

func padCell(text string, width int) string {
	return lipgloss.NewStyle().Width(width).Render(text)
}

func displayValue(text string) string {
	if strings.TrimSpace(text) == "" {
		return "—"
	}
	return text
}

func fitFlexWidths(total int, mins []int, weights []int) []int {
	out := append([]int(nil), mins...)
	minTotal := 0
	for _, m := range mins {
		minTotal += m
	}
	if total <= minTotal {
		return out
	}
	extra := total - minTotal
	weightTotal := 0
	for _, w := range weights {
		weightTotal += w
	}
	if weightTotal == 0 {
		weightTotal = len(weights)
		for i := range weights {
			weights[i] = 1
		}
	}
	used := 0
	for i := range out {
		add := extra * weights[i] / weightTotal
		out[i] += add
		used += add
	}
	for i := 0; used < extra; i = (i + 1) % len(out) {
		out[i]++
		used++
	}
	return out
}

func containerWidths(totalWidth int) containerTableWidths {
	gaps := 7
	base := []int{3, 10, 10, 8, 14, 12, 12, 12}
	weights := []int{0, 0, 18, 8, 22, 12, 18, 22}
	available := max(48, totalWidth-gaps)
	vals := fitFlexWidths(available, base, weights)
	return containerTableWidths{sel: vals[0], id: vals[1], name: vals[2], state: vals[3], ports: vals[4], memory: vals[5], image: vals[6], status: vals[7]}
}

func volumeWidths(totalWidth int) volumeTableWidths {
	gaps := 4
	base := []int{10, 8, 8, 7, 12}
	weights := []int{24, 12, 12, 10, 42}
	available := max(24, totalWidth-gaps)
	vals := fitFlexWidths(available, base, weights)
	return volumeTableWidths{name: vals[0], driver: vals[1], scope: vals[2], usedBy: vals[3], mountpoint: vals[4]}
}

func blocksWindow(blocks [][]string, cursor, maxLines int) []string {
	if len(blocks) == 0 || maxLines <= 0 {
		return nil
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= len(blocks) {
		cursor = len(blocks) - 1
	}
	totalLines := 0
	cursorStart := 0
	for i, block := range blocks {
		if i < cursor {
			cursorStart += len(block)
		}
		totalLines += len(block)
	}
	cursorHeight := len(blocks[cursor])
	center := cursorStart + cursorHeight/2
	startLine := center - maxLines/2
	if startLine < 0 {
		startLine = 0
	}
	if totalLines > maxLines && startLine > totalLines-maxLines {
		startLine = totalLines - maxLines
	}
	endLine := startLine + maxLines
	out := []string{}
	linePos := 0
	for _, block := range blocks {
		blockEnd := linePos + len(block)
		if blockEnd <= startLine {
			linePos = blockEnd
			continue
		}
		if linePos >= endLine {
			break
		}
		from := max(0, startLine-linePos)
		to := min(len(block), endLine-linePos)
		out = append(out, block[from:to]...)
		linePos = blockEnd
	}
	return out
}

func (m model) renderContainerBlock(c Container, widths containerTableWidths, contentWidth int, isSelected bool) []string {
	mark := "[ ]"
	if m.marked[c.ID] {
		mark = "[x]"
	}
	stateLines := wrapToWidth(displayValue(c.State), widths.state)
	if !isSelected {
		for i := range stateLines {
			if c.State == "running" {
				stateLines[i] = runningStyle.Render(stateLines[i])
			} else {
				stateLines[i] = stoppedStyle.Render(stateLines[i])
			}
		}
	}
	cols := [][]string{
		wrapToWidth(mark, widths.sel),
		wrapToWidth(displayValue(c.ID), widths.id),
		wrapToWidth(displayValue(c.Names), widths.name),
		stateLines,
		wrapToWidth(displayValue(c.Ports), widths.ports),
		wrapToWidth(displayValue(c.Memory), widths.memory),
		wrapToWidth(displayValue(c.Image), widths.image),
		wrapToWidth(displayValue(c.Status), widths.status),
	}
	maxH := 1
	for _, col := range cols {
		if len(col) > maxH {
			maxH = len(col)
		}
	}
	out := make([]string, 0, maxH)
	for row := 0; row < maxH; row++ {
		parts := []string{
			padCell(lineAt(cols[0], row), widths.sel),
			padCell(lineAt(cols[1], row), widths.id),
			padCell(lineAt(cols[2], row), widths.name),
			padCell(lineAt(cols[3], row), widths.state),
			padCell(lineAt(cols[4], row), widths.ports),
			padCell(lineAt(cols[5], row), widths.memory),
			padCell(lineAt(cols[6], row), widths.image),
			padCell(lineAt(cols[7], row), widths.status),
		}
		line := strings.Join(parts, " ")
		if isSelected {
			line = selectedStyle.Width(contentWidth).Render(line)
		}
		out = append(out, line)
	}
	return out
}

func (m model) renderVolumeBlock(v Volume, widths volumeTableWidths, contentWidth int, isSelected bool) []string {
	usedBy := fmt.Sprintf("%d", len(v.Containers))
	cols := [][]string{
		wrapToWidth(displayValue(v.Name), widths.name),
		wrapToWidth(displayValue(v.Driver), widths.driver),
		wrapToWidth(displayValue(v.Scope), widths.scope),
		wrapToWidth(usedBy, widths.usedBy),
		wrapToWidth(displayValue(v.Mountpoint), widths.mountpoint),
	}
	maxH := 1
	for _, col := range cols {
		if len(col) > maxH {
			maxH = len(col)
		}
	}
	out := make([]string, 0, maxH)
	for row := 0; row < maxH; row++ {
		line := strings.Join([]string{
			padCell(lineAt(cols[0], row), widths.name),
			padCell(lineAt(cols[1], row), widths.driver),
			padCell(lineAt(cols[2], row), widths.scope),
			padCell(lineAt(cols[3], row), widths.usedBy),
			padCell(lineAt(cols[4], row), widths.mountpoint),
		}, " ")
		if isSelected {
			line = selectedStyle.Width(contentWidth).Render(line)
		}
		out = append(out, line)
	}
	return out
}

func lineAt(lines []string, idx int) string {
	if idx < 0 || idx >= len(lines) {
		return ""
	}
	return lines[idx]
}

func renderVolumeEntry(entry volumeEntry, width int) string {
	prefix := "[f]"
	if entry.IsDir {
		prefix = "[d]"
	} else if entry.Type == "link" {
		prefix = "[l]"
	}
	label := fmt.Sprintf("%s %s", prefix, entry.Name)
	meta := entry.Size
	if entry.IsDir {
		meta = entry.Type
	}
	row := fmt.Sprintf("%-6s %s", meta, label)
	return trim(row, max(12, width))
}

func (m model) renderContainers() string {
	width := max(60, m.width*2/3)
	height := max(12, m.height-12)
	contentWidth := width - 4
	widths := containerWidths(contentWidth)
	lines := []string{titleStyle.Render("Containers")}
	lines = append(lines, mutedStyle.Render("mark with <space>, all-visible with a • x start • s stop • d delete selected/current • enter shell • l logs • P explains port remap limits • [x] means selected"))
	lines = append(lines, strings.Join([]string{
		padCell("SEL", widths.sel),
		padCell("ID", widths.id),
		padCell("NAME", widths.name),
		padCell("STATE", widths.state),
		padCell("PORTS", widths.ports),
		padCell("MEMORY", widths.memory),
		padCell("IMAGE", widths.image),
		padCell("STATUS", widths.status),
	}, " "))
	if len(m.filtered) == 0 {
		lines = append(lines, mutedStyle.Render("no containers match current filter"))
		return sectionStyle.Width(width).Height(height).Render(strings.Join(lines, "\n"))
	}
	blocks := make([][]string, 0, len(m.filtered))
	for i, c := range m.filtered {
		isSelected := i == m.cursor && (m.mode == viewMain || m.mode == viewShell || m.mode == viewLogs)
		blocks = append(blocks, m.renderContainerBlock(c, widths, contentWidth, isSelected))
	}
	lines = append(lines, blocksWindow(blocks, m.cursor, height-4)...)
	return sectionStyle.Width(width).Height(height).Render(strings.Join(lines, "\n"))
}

func (m model) renderVolumes() string {
	width := max(60, m.width*2/3)
	height := max(12, m.height-12)
	contentWidth := width - 4
	widths := volumeWidths(contentWidth)
	lines := []string{titleStyle.Render("Volumes")}
	lines = append(lines, mutedStyle.Render("select a volume to preview where data is stored • enter refreshes preview • press v to go back to containers"))
	lines = append(lines, strings.Join([]string{
		padCell("NAME", widths.name),
		padCell("DRIVER", widths.driver),
		padCell("SCOPE", widths.scope),
		padCell("USED", widths.usedBy),
		padCell("MOUNTPOINT", widths.mountpoint),
	}, " "))
	if len(m.filteredVols) == 0 {
		lines = append(lines, mutedStyle.Render("no volumes match current filter"))
		return sectionStyle.Width(width).Height(height).Render(strings.Join(lines, "\n"))
	}
	blocks := make([][]string, 0, len(m.filteredVols))
	for i, v := range m.filteredVols {
		isSelected := i == m.cursor && m.mode == viewMain
		blocks = append(blocks, m.renderVolumeBlock(v, widths, contentWidth, isSelected))
	}
	lines = append(lines, blocksWindow(blocks, m.cursor, height-4)...)
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

	lines := []string{}
	if m.resource == resourceContainers {
		lines = append(lines, titleStyle.Render("Selection & snippets"))
		if c, ok := m.currentContainer(); ok {
			volumeSummary := "—"
			if len(c.VolumeNames) > 0 {
				volumeSummary = strings.Join(c.VolumeNames, ", ")
			}
			lines = append(lines,
				fmt.Sprintf("current: %s", c.Names),
				fmt.Sprintf("id: %s", c.ID),
				fmt.Sprintf("image: %s", c.Image),
				fmt.Sprintf("status: %s", c.Status),
				fmt.Sprintf("ports: %s", displayValue(c.Ports)),
				fmt.Sprintf("memory: %s", displayValue(c.Memory)),
				fmt.Sprintf("volumes: %s", trim(volumeSummary, max(20, width-12))),
				"",
				mutedStyle.Render("enter = open shell inside TUI"),
				mutedStyle.Render("l = view logs"),
				mutedStyle.Render("d = delete selected/current container(s)"),
				mutedStyle.Render("P = docker port remap info"),
				mutedStyle.Render("p = run saved snippet"),
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

	lines = append(lines, titleStyle.Render("Volume details"))
	if v, ok := m.currentVolume(); ok {
		usedBy := "none"
		if len(v.Containers) > 0 {
			usedBy = strings.Join(v.Containers, ", ")
		}
		lines = append(lines,
			fmt.Sprintf("volume: %s", v.Name),
			fmt.Sprintf("driver: %s", v.Driver),
			fmt.Sprintf("scope: %s", v.Scope),
			fmt.Sprintf("mount: %s", trim(v.Mountpoint, max(20, width-9))),
			fmt.Sprintf("used by: %s", trim(usedBy, max(20, width-11))),
			"",
			mutedStyle.Render("contents preview (enter refreshes):"),
		)
		details, detailsOK := m.volumeDetailsForCurrent()
		switch {
		case !detailsOK:
			lines = append(lines, mutedStyle.Render("select a volume"))
		case details.loading:
			lines = append(lines, mutedStyle.Render("loading volume contents..."))
		case details.err != "":
			lines = append(lines, errorStyle.Render(trim(details.err, max(20, width-6))))
		case len(details.entries) == 0:
			lines = append(lines, mutedStyle.Render("volume is empty or inaccessible"))
		default:
			limit := max(4, height-16)
			for i, entry := range details.entries {
				if i >= limit {
					break
				}
				lines = append(lines, renderVolumeEntry(entry, width-6))
			}
			if details.totalEntries > len(details.entries) {
				lines = append(lines, mutedStyle.Render(fmt.Sprintf("… %d more item(s)", details.totalEntries-len(details.entries))))
			} else {
				lines = append(lines, mutedStyle.Render(fmt.Sprintf("%d item(s)", details.totalEntries)))
			}
		}
		lines = append(lines, "", mutedStyle.Render(": autocomplete supports volume commands"))
	} else {
		lines = append(lines, mutedStyle.Render("no volume selected"))
	}
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
	keys := "j/k move • / search • v containers/volumes • tab running/all • space mark • a mark visible • x start all • s stop running • d delete • S save snippet • p snippet browser • P port remap info • : command • ? help • r refresh • q quit"
	if m.resource == resourceVolumes && m.mode == viewMain {
		keys = "volumes: j/k move • / search • enter refresh preview • v back to containers • : command • ? help • r refresh • q quit"
	}
	if m.mode == viewLogs {
		keys = "logs: j/k scroll • h/l horiz • mouse wheel scroll • drag select • y copy • c clear selection • ctrl+u/d page • g/G top/bottom • / search • n/N next/prev • z fullscreen • f follow latest • enter/esc back"
	}
	if m.mode == viewVolume {
		keys = "volume browser: j/k move • enter into dir • backspace go up • r refresh • esc exit browser"
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
		mutedStyle.Render("/ search • j/k move • space mark • a mark visible • e edit • x start • s stop • d delete selected/current • D delete all • esc close"),
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

func (m model) renderSnippetEditor() string {
	selectedCount := len(m.snippetEditorSelected())
	lines := []string{
		titleStyle.Render("Edit snippet"),
		"name: " + m.snippetEditor.nameInput.View(),
		mutedStyle.Render(fmt.Sprintf("selected containers: %d", selectedCount)),
		"",
	}
	if len(m.snippetEditor.options) == 0 {
		lines = append(lines, mutedStyle.Render("no containers available"))
	} else {
		maxRows := min(12, len(m.snippetEditor.options))
		start := 0
		if len(m.snippetEditor.options) > maxRows {
			start = m.snippetEditor.cursor - maxRows/2
			if start < 0 {
				start = 0
			}
			if start > len(m.snippetEditor.options)-maxRows {
				start = len(m.snippetEditor.options) - maxRows
			}
		}
		end := min(len(m.snippetEditor.options), start+maxRows)
		for i := start; i < end; i++ {
			option := m.snippetEditor.options[i]
			mark := "[ ]"
			if m.snippetEditor.marked[option.Name] {
				mark = "[x]"
			}
			label := option.Name
			if !option.Available {
				label += " (saved, not currently available)"
			}
			row := fmt.Sprintf("%-4s %s", mark, trim(label, max(20, m.width-34)))
			if i == m.snippetEditor.cursor && !m.snippetEditor.nameFocused {
				row = selectedStyle.Render(row)
			}
			lines = append(lines, row)
		}
	}
	hint := "tab switch name/list • space toggle • a toggle all • enter save • esc cancel"
	if m.snippetEditor.nameFocused {
		lines[1] = selectedStyle.Render(lines[1])
	}
	lines = append(lines, "", mutedStyle.Render(hint))
	return activeSectionStyle.Width(min(84, max(52, m.width-10))).Render(strings.Join(lines, "\n"))
}

func (m model) renderConfirmDialog() string {
	buttonBase := lipgloss.NewStyle().Padding(0, 1).Border(lipgloss.RoundedBorder())
	buttonYes := buttonBase.Render("Yes")
	buttonNo := buttonBase.Render("No")
	if m.confirm.yes {
		buttonYes = selectedStyle.Padding(0, 1).Border(lipgloss.RoundedBorder()).Render("Yes")
		buttonNo = mutedStyle.Render(buttonNo)
	} else {
		buttonYes = mutedStyle.Render(buttonYes)
		buttonNo = selectedStyle.Padding(0, 1).Border(lipgloss.RoundedBorder()).Render("No")
	}
	lines := []string{
		titleStyle.Render(displayValue(m.confirm.title)),
		errorStyle.Render(displayValue(m.confirm.message)),
		trim(displayValue(m.confirm.preview), max(20, m.width-28)),
		"",
		lipgloss.JoinHorizontal(lipgloss.Center, buttonYes, "  ", buttonNo),
		mutedStyle.Render("tab/←/→ switch • enter confirm selected option • esc cancel"),
	}
	return activeSectionStyle.Width(min(72, max(46, m.width-12))).Render(strings.Join(lines, "\n"))
}

func (m model) overlayCentered(base, overlay string) string {
	baseLines := strings.Split(base, "\n")
	baseWidth := lipgloss.Width(base)
	baseHeight := len(baseLines)
	overlayLines := strings.Split(overlay, "\n")
	overlayWidth := lipgloss.Width(overlay)
	overlayHeight := len(overlayLines)
	if baseWidth == 0 {
		baseWidth = m.width
	}
	if baseHeight == 0 {
		baseHeight = m.height
	}
	x := max(0, (baseWidth-overlayWidth)/2)
	y := max(0, (baseHeight-overlayHeight)/2)
	for len(baseLines) < baseHeight {
		baseLines = append(baseLines, "")
	}
	for i, line := range baseLines {
		baseLines[i] = lipgloss.NewStyle().Width(baseWidth).Render(line)
	}
	for i, oLine := range overlayLines {
		idx := y + i
		if idx < 0 || idx >= len(baseLines) {
			continue
		}
		left := ansi.Cut(baseLines[idx], 0, x)
		rightStart := x + lipgloss.Width(oLine)
		right := ""
		if rightStart < baseWidth {
			right = ansi.Cut(baseLines[idx], rightStart, baseWidth)
		}
		baseLines[idx] = left + oLine + right
	}
	return strings.Join(baseLines, "\n")
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
		"d                delete selected/current containers with confirmation",
		"S                save selected containers as snippet (stores container names)",
		"p                open snippet browser with fuzzy search",
		"P                explain why docker port remap needs container recreation",
		"snippet browser   e=edit name+containers, x=start, s=stop, d=delete selected/current, D=delete all",
		"enter            open PTY shell for selected running container / close logs",
		"enter (volumes)  refresh selected volume mountpoint preview",
		"l                open logs for selected container",
		"containers        ports + memory usage are shown in the list/details pane",
		"volumes           side pane shows mountpoint, attached containers, preview",
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

func (m model) renderVolumeBrowser() string {
	width := max(60, m.width*2/3)
	height := max(12, m.height-12)
	contentWidth := width - 4
	lines := []string{titleStyle.Render("Volume Browser")}
	pathDisplay := m.volumeBrowser.path
	if len(pathDisplay) > contentWidth-10 {
		pathDisplay = "..." + pathDisplay[len(pathDisplay)-(contentWidth-10):]
	}
	lines = append(lines, mutedStyle.Render(pathDisplay))
	lines = append(lines, strings.Join([]string{
		padCell("TYPE", 6),
		padCell("NAME", 30),
		padCell("SIZE", 10),
		padCell("MODE", 12),
	}, " "))
	if m.volumeBrowser.loading {
		lines = append(lines, mutedStyle.Render("loading..."))
		return sectionStyle.Width(width).Height(height).Render(strings.Join(lines, "\n"))
	}
	if m.volumeBrowser.err != "" {
		lines = append(lines, errorStyle.Render(m.volumeBrowser.err))
		return sectionStyle.Width(width).Height(height).Render(strings.Join(lines, "\n"))
	}
	if len(m.volumeBrowser.entries) == 0 {
		lines = append(lines, mutedStyle.Render("directory is empty"))
		return sectionStyle.Width(width).Height(height).Render(strings.Join(lines, "\n"))
	}
	blocks := make([][]string, 0, len(m.volumeBrowser.entries))
	for i, entry := range m.volumeBrowser.entries {
		isSelected := i == m.volumeBrowser.cursor
		prefix := "[f]"
		if entry.IsDir {
			prefix = "[d]"
		} else if entry.Type == "link" {
			prefix = "[l]"
		}
		cols := [][]string{
			wrapToWidth(prefix, 6),
			wrapToWidth(entry.Name, 30),
			wrapToWidth(entry.Size, 10),
			wrapToWidth(entry.Mode, 12),
		}
		maxH := 1
		for _, col := range cols {
			if len(col) > maxH {
				maxH = len(col)
			}
		}
		block := make([]string, 0, maxH)
		for row := 0; row < maxH; row++ {
			line := strings.Join([]string{
				padCell(lineAt(cols[0], row), 6),
				padCell(lineAt(cols[1], row), 30),
				padCell(lineAt(cols[2], row), 10),
				padCell(lineAt(cols[3], row), 12),
			}, " ")
			if isSelected {
				line = selectedStyle.Width(contentWidth).Render(line)
			}
			block = append(block, line)
		}
		blocks = append(blocks, block)
	}
	lines = append(lines, blocksWindow(blocks, m.volumeBrowser.cursor, height-4)...)
	return sectionStyle.Width(width).Height(height).Render(strings.Join(lines, "\n"))
}

func (m model) renderVolumeBrowserDetails() string {
	width := max(36, m.width/3-8)
	height := max(12, m.height-12)
	lines := []string{titleStyle.Render("Volume Browser")}
	lines = append(lines, mutedStyle.Render("j/k move • enter into dir/file • backspace parent • r refresh • esc exit"))
	if m.volumeBrowser.path != "" {
		lines = append(lines, "", mutedStyle.Render("path:"), mutedStyle.Render(trim(m.volumeBrowser.path, width-8)))
	}
	if entry, ok := m.currentVolumeBrowserEntry(); ok {
		lines = append(lines, "",
			fmt.Sprintf("name: %s", entry.Name),
			fmt.Sprintf("type: %s", entry.Type),
			fmt.Sprintf("size: %s", entry.Size),
			fmt.Sprintf("mode: %s", entry.Mode),
			fmt.Sprintf("path: %s", trim(entry.Path, width-8)),
		)

		if m.volumeBrowser.selectedFile == entry.Path {
			lines = append(lines, "", mutedStyle.Render("preview:"))
			if m.volumeBrowser.fileLoading {
				lines = append(lines, mutedStyle.Render("loading..."))
			} else {
				contentDisplay := m.volumeBrowser.fileContent
				if contentDisplay == "" {
					contentDisplay = "(empty file)"
				}
				wrapped := wrapToWidth(contentDisplay, width-4)
				previewLimit := max(5, height-18)
				if len(wrapped) > previewLimit {
					lines = append(lines, wrapped[:previewLimit]...)
					lines = append(lines, mutedStyle.Render("... (truncated)"))
				} else {
					lines = append(lines, wrapped...)
				}
			}
		}
	}
	lines = append(lines, "", mutedStyle.Render(fmt.Sprintf("items: %d", len(m.volumeBrowser.entries))))
	return sectionStyle.Width(width).Height(height).Render(strings.Join(lines, "\n"))
}
