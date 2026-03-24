package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

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
		m.volumes = linkVolumeUsage(m.volumes, m.containers)
		m.applyFilter()
		m.clampCursor()
		m.status = fmt.Sprintf("loaded %d containers", len(m.containers))
		return m, fetchContainerStatsCmd(m.containers)
	case containerStatsMsg:
		for i, c := range m.containers {
			if mem, ok := msg.stats[c.ID]; ok {
				m.containers[i].Memory = mem
			} else if mem, ok := msg.stats[c.Names]; ok {
				m.containers[i].Memory = mem
			}
		}
		m.applyFilter()
		return m, nil
	case volumesMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("volume load failed: %v", msg.err)
			return m, nil
		}
		m.dockerChecked = true
		m.dockerOK = true
		m.dockerErr = ""
		m.volumes = linkVolumeUsage(msg.items, m.containers)
		m.applyFilter()
		m.clampCursor()
		m.status = fmt.Sprintf("loaded %d containers • %d volumes", len(m.containers), len(m.volumes))
		return m, m.currentVolumeDetailsCmd()
	case volumeDetailsMsg:
		if m.volumeDetails.name != msg.name || m.volumeDetails.mountpoint != msg.mountpoint {
			return m, nil
		}
		m.volumeDetails.loading = false
		m.volumeDetails.loaded = true
		m.volumeDetails.entries = msg.entries
		m.volumeDetails.totalEntries = msg.totalEntries
		if msg.err != nil {
			m.volumeDetails.err = msg.err.Error()
			return m, nil
		}
		m.volumeDetails.err = ""
		return m, nil
	case volumeBrowseMsg:
		m.volumeBrowser.loading = false
		if msg.err != nil {
			m.volumeBrowser.err = msg.err.Error()
			return m, nil
		}
		m.volumeBrowser.entries = msg.entries
		m.volumeBrowser.err = ""
		m.volumeBrowser.path = msg.path
		m.clampVolumeCursor()
		return m, nil
	case volumeFileContentMsg:
		if m.volumeBrowser.selectedFile == msg.path {
			m.volumeBrowser.fileLoading = false
			if msg.err != nil {
				m.volumeBrowser.fileContent = msg.err.Error()
			} else {
				m.volumeBrowser.fileContent = msg.content
			}
		}
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
		if m.confirm.active {
			return m, m.handleConfirmKey(msg)
		}
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
		if m.mode == viewVolume {
			return m.handleVolumeBrowserKey(msg)
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
				if m.activeLogID == "FILE" {
					m.mode = viewVolume
				} else {
					m.mode = viewMain
				}
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
			return m, m.currentVolumeDetailsCmd()
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
			return m, m.currentVolumeDetailsCmd()
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
			return m, m.currentVolumeDetailsCmd()
		case "g":
			m.cursor = 0
			if m.mode == viewLogs {
				m.followLogs = false
				m.logCursor = 0
				return m, nil
			}
			m.logCursor = 0
			return m, m.currentVolumeDetailsCmd()
		case "G":
			if m.mode == viewLogs {
				m.followLogs = false
				m.logCursor = m.maxLogStart()
				return m, nil
			}
			if m.itemCount() > 0 {
				m.cursor = m.itemCount() - 1
			}
			return m, m.currentVolumeDetailsCmd()
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
		case "d":
			if m.resource != resourceContainers || m.mode != viewMain {
				return m, nil
			}
			ids := m.selectedIDs()
			if len(ids) == 0 {
				if c, ok := m.currentContainer(); ok {
					ids = []string{c.ID}
				}
			}
			if len(ids) == 0 {
				m.status = "no containers selected"
				return m, nil
			}
			m.openContainerDeleteConfirm(ids)
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
				if m.activeLogID == "FILE" {
					m.mode = viewVolume
				} else {
					m.mode = viewMain
				}
				return m, tea.ShowCursor
			}
			if m.mode == viewVolume {
				return m.handleVolumeBrowserEnter()
			}
			if m.resource == resourceVolumes {
				return m.handleEnterVolumes()
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
		case "P":
			if m.resource == resourceContainers {
				m.status = "docker does not support changing published ports on an existing container; recreate the container with new -p/-P settings"
			}
			return m, nil
		}

		if m.focus == focusSearch {
			var cmd tea.Cmd
			m.search, cmd = m.search.Update(msg)
			m.applyFilter()
			m.clampCursor()
			return m, tea.Batch(cmd, m.currentVolumeDetailsCmd())
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
		names := m.selectedSnippetNames()
		if len(names) == 0 {
			if current, ok := m.currentSnippetName(); ok {
				names = []string{current}
			}
		}
		if len(names) == 0 {
			m.status = "no snippets selected"
			return m, nil
		}
		m.openSnippetDeleteConfirm(names, false)
		return m, nil
	case "D":
		if len(m.snippets) == 0 {
			m.status = "no snippets to delete"
			return m, nil
		}
		m.openSnippetDeleteConfirm(nil, true)
		return m, nil
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
