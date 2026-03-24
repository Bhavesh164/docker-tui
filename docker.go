package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/creack/pty"
)

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
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
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
		attachContainerInspect(ctx, items)
		attachContainerStats(ctx, items)
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

type containerPortBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

func attachContainerInspect(ctx context.Context, items []Container) {
	if len(items) == 0 {
		return
	}
	args := []string{"inspect"}
	for _, item := range items {
		args = append(args, item.ID)
	}
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.Output()
	if err != nil {
		return
	}
	type inspectMount struct {
		Type string `json:"Type"`
		Name string `json:"Name"`
	}
	type inspectContainer struct {
		ID              string         `json:"Id"`
		Mounts          []inspectMount `json:"Mounts"`
		NetworkSettings struct {
			Ports map[string][]containerPortBinding `json:"Ports"`
		} `json:"NetworkSettings"`
	}
	var inspectItems []inspectContainer
	if err := json.Unmarshal(out, &inspectItems); err != nil {
		return
	}
	for _, inspected := range inspectItems {
		idx := indexOfContainer(items, inspected.ID)
		if idx < 0 {
			continue
		}
		set := map[string]bool{}
		for _, mount := range inspected.Mounts {
			if mount.Type != "volume" || mount.Name == "" || set[mount.Name] {
				continue
			}
			set[mount.Name] = true
			items[idx].VolumeNames = append(items[idx].VolumeNames, mount.Name)
		}
		sort.Strings(items[idx].VolumeNames)
		formattedPorts := formatInspectPorts(inspected.NetworkSettings.Ports)
		if formattedPorts != "" {
			items[idx].Ports = formattedPorts
		}
	}
}

func attachContainerStats(ctx context.Context, items []Container) {
	hasRunning := false
	for _, item := range items {
		if item.State == "running" {
			hasRunning = true
			break
		}
	}
	if !hasRunning {
		return
	}
	cmd := exec.CommandContext(ctx, "docker", "stats", "--no-stream", "--format", `{"ID":"{{.ID}}","Name":"{{.Name}}","MemUsage":"{{.MemUsage}}"}`)
	out, err := cmd.Output()
	if err != nil {
		return
	}
	type statLine struct {
		ID       string `json:"ID"`
		Name     string `json:"Name"`
		MemUsage string `json:"MemUsage"`
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var stat statLine
		if err := json.Unmarshal([]byte(line), &stat); err != nil {
			continue
		}
		idx := indexOfContainerByIDOrName(items, stat.ID, stat.Name)
		if idx >= 0 {
			items[idx].Memory = stat.MemUsage
		}
	}
}

func indexOfContainer(items []Container, id string) int {
	for i := range items {
		if items[i].ID == id || strings.HasPrefix(id, items[i].ID) {
			return i
		}
	}
	return -1
}

func indexOfContainerByIDOrName(items []Container, id, name string) int {
	if idx := indexOfContainer(items, id); idx >= 0 {
		return idx
	}
	for i := range items {
		if items[i].Names == name {
			return i
		}
	}
	return -1
}

func formatInspectPorts(ports map[string][]containerPortBinding) string {
	if len(ports) == 0 {
		return ""
	}
	entries := make([]string, 0, len(ports))
	seen := map[string]bool{}
	for containerPort, bindings := range ports {
		if len(bindings) == 0 {
			if !seen[containerPort] {
				seen[containerPort] = true
				entries = append(entries, containerPort)
			}
			continue
		}
		for _, binding := range bindings {
			host := binding.HostPort
			switch binding.HostIP {
			case "", "0.0.0.0":
				// keep host port only
			default:
				host = binding.HostIP + ":" + binding.HostPort
			}
			label := host + "->" + containerPort
			if seen[label] {
				continue
			}
			seen[label] = true
			entries = append(entries, label)
		}
	}
	sort.Strings(entries)
	return strings.Join(entries, ", ")
}

func loadVolumeDetailsCmd(name, mountpoint string) tea.Cmd {
	return func() tea.Msg {
		msg := volumeDetailsMsg{name: name, mountpoint: mountpoint}
		if strings.TrimSpace(mountpoint) == "" {
			msg.err = errors.New("volume mountpoint is empty")
			return msg
		}
		entries, err := os.ReadDir(mountpoint)
		if err != nil {
			msg.err = err
			return msg
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].IsDir() == entries[j].IsDir() {
				return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
			}
			return entries[i].IsDir()
		})
		msg.totalEntries = len(entries)
		limit := min(32, len(entries))
		msg.entries = make([]volumeEntry, 0, limit)
		for _, entry := range entries[:limit] {
			item := volumeEntry{
				Name:  entry.Name(),
				Path:  mountpoint + string(os.PathSeparator) + entry.Name(),
				IsDir: entry.IsDir(),
				Type:  "file",
				Size:  "—",
			}
			if entry.IsDir() {
				item.Type = "dir"
			}
			if entry.Type()&os.ModeSymlink != 0 {
				item.Type = "link"
			}
			if info, err := entry.Info(); err == nil {
				item.Mode = info.Mode().String()
				if !entry.IsDir() {
					item.Size = humanSize(info.Size())
				}
			}
			msg.entries = append(msg.entries, item)
		}
		return msg
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
