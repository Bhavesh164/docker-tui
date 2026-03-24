package main

import (
	"fmt"
	"os"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

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
