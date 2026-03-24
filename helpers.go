package main

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

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
	return m.deleteSnippetsByNameCmd(names)
}

func (m *model) deleteSnippetsByNameCmd(names []string) tea.Cmd {
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

func (m *model) openConfirm(title, message, preview string, action confirmAction, values []string, force bool) {
	m.confirm = confirmDialog{
		active:  true,
		yes:     true,
		title:   title,
		message: message,
		preview: preview,
		action:  action,
		values:  append([]string(nil), values...),
		force:   force,
	}
}

func (m *model) closeConfirm(status string) {
	m.confirm = confirmDialog{}
	if status != "" {
		m.status = status
	}
}

func (m *model) openSnippetDeleteConfirm(names []string, deleteAll bool) {
	if deleteAll {
		m.openConfirm(
			"Confirm snippet delete",
			"Delete ALL snippets?",
			fmt.Sprintf("all snippets (%d)", len(m.snippets)),
			confirmDeleteAllSnippets,
			nil,
			false,
		)
		m.status = "confirm delete all snippets"
		return
	}
	preview := strings.Join(names, ", ")
	m.openConfirm(
		"Confirm snippet delete",
		fmt.Sprintf("Delete %d snippet(s)?", len(names)),
		preview,
		confirmDeleteSnippets,
		names,
		false,
	)
	m.status = fmt.Sprintf("confirm delete %d snippet(s)", len(names))
}

func (m *model) openContainerDeleteConfirm(ids []string) {
	names := m.containerNamesByID(ids)
	force := false
	for _, id := range ids {
		if c, ok := m.containerByID(id); ok && c.State == "running" {
			force = true
			break
		}
	}
	message := fmt.Sprintf("Delete %d container(s)?", len(ids))
	if force {
		message = fmt.Sprintf("Delete %d container(s)? Running containers will be force removed.", len(ids))
	}
	m.openConfirm(
		"Confirm container delete",
		message,
		strings.Join(names, ", "),
		confirmDeleteContainers,
		ids,
		force,
	)
	m.status = fmt.Sprintf("confirm delete %d container(s)", len(ids))
}

func (m *model) handleConfirmKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "tab", "shift+tab":
		m.confirm.yes = !m.confirm.yes
		return nil
	case "left", "h":
		m.confirm.yes = true
		return nil
	case "right", "l":
		m.confirm.yes = false
		return nil
	case "y", "Y":
		m.confirm.yes = true
		return nil
	case "n", "N":
		m.confirm.yes = false
		return nil
	case "esc":
		m.closeConfirm("action cancelled")
		return nil
	case "enter":
		if !m.confirm.yes {
			m.closeConfirm("action cancelled")
			return nil
		}
		return m.applyConfirmAction()
	default:
		return nil
	}
}

func (m *model) applyConfirmAction() tea.Cmd {
	confirm := m.confirm
	m.confirm = confirmDialog{}
	switch confirm.action {
	case confirmDeleteSnippets:
		return m.deleteSnippetsByNameCmd(confirm.values)
	case confirmDeleteAllSnippets:
		return m.deleteSelectedSnippetsCmd(true)
	case confirmDeleteContainers:
		if len(confirm.values) == 0 {
			m.status = "no containers selected"
			return nil
		}
		args := []string{"docker", "rm"}
		if confirm.force {
			args = append(args, "-f")
		}
		args = append(args, confirm.values...)
		return runCommandCmd(strings.Join(args, " "))
	default:
		return nil
	}
}

func (m model) volumeDetailsForCurrent() (volumeDetailsState, bool) {
	v, ok := m.currentVolume()
	if !ok {
		return volumeDetailsState{}, false
	}
	if m.volumeDetails.name != v.Name || m.volumeDetails.mountpoint != v.Mountpoint {
		return volumeDetailsState{name: v.Name, mountpoint: v.Mountpoint, loading: true}, true
	}
	return m.volumeDetails, true
}

func (m *model) currentVolumeDetailsCmd() tea.Cmd {
	if m.resource != resourceVolumes {
		return nil
	}
	v, ok := m.currentVolume()
	if !ok {
		m.volumeDetails = volumeDetailsState{}
		return nil
	}
	if m.volumeDetails.loading && m.volumeDetails.name == v.Name && m.volumeDetails.mountpoint == v.Mountpoint {
		return nil
	}
	if m.volumeDetails.loaded && m.volumeDetails.name == v.Name && m.volumeDetails.mountpoint == v.Mountpoint {
		return nil
	}
	m.volumeDetails = volumeDetailsState{name: v.Name, mountpoint: v.Mountpoint, loading: true}
	return loadVolumeDetailsCmd(v.Name, v.Mountpoint)
}

func (m model) containerByID(id string) (Container, bool) {
	for _, c := range m.containers {
		if c.ID == id {
			return c, true
		}
	}
	return Container{}, false
}

func (m model) containerNamesByID(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if c, ok := m.containerByID(id); ok {
			if !seen[c.Names] {
				seen[c.Names] = true
				out = append(out, c.Names)
			}
			continue
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
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

func linkVolumeUsage(volumes []Volume, containers []Container) []Volume {
	usage := map[string][]string{}
	seen := map[string]map[string]bool{}
	for _, c := range containers {
		for _, volumeName := range c.VolumeNames {
			if volumeName == "" {
				continue
			}
			if seen[volumeName] == nil {
				seen[volumeName] = map[string]bool{}
			}
			if seen[volumeName][c.Names] {
				continue
			}
			seen[volumeName][c.Names] = true
			usage[volumeName] = append(usage[volumeName], c.Names)
		}
	}
	out := make([]Volume, len(volumes))
	copy(out, volumes)
	for i := range out {
		out[i].Containers = append([]string(nil), usage[out[i].Name]...)
		sort.Strings(out[i].Containers)
	}
	return out
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
	for _, s := range []string{"docker ps -a", "docker stats --no-stream", "docker volume ls", "docker volume inspect ", "docker volume rm ", "docker start ", "docker stop ", "docker logs --tail 200 ", "docker port ", "docker exec -it  sh", "docker exec -it  bash", "docker inspect ", "docker rm "} {
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
		add("docker port " + name)
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
