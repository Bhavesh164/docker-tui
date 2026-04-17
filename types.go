package main

import (
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/lipgloss"
)

type focusArea int

type viewMode int

type resourceMode int

type filterMode int

type confirmAction int

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
	viewVolume
)

const (
	resourceContainers resourceMode = iota
	resourceVolumes
)

const (
	filterAll filterMode = iota
	filterRunning
)

const (
	confirmNone confirmAction = iota
	confirmDeleteSnippets
	confirmDeleteAllSnippets
	confirmDeleteContainers
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
	ID          string   `json:"ID"`
	Image       string   `json:"Image"`
	Names       string   `json:"Names"`
	State       string   `json:"State"`
	Status      string   `json:"Status"`
	Ports       string   `json:"Ports"`
	Memory      string   `json:"-"`
	VolumeNames []string `json:"-"`
}

type Volume struct {
	Driver     string   `json:"Driver"`
	Name       string   `json:"Name"`
	Mountpoint string   `json:"Mountpoint"`
	Scope      string   `json:"Scope"`
	Containers []string `json:"-"`
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

type containerStatsMsg struct {
	stats map[string]string
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

type volumeEntry struct {
	Name  string
	Type  string
	Size  string
	Path  string
	Mode  string
	IsDir bool
}

type volumeDetailsMsg struct {
	name         string
	mountpoint   string
	entries      []volumeEntry
	totalEntries int
	err          error
}

type volumeBrowseMsg struct {
	path    string
	entries []volumeEntry
	err     error
}

type confirmDialog struct {
	active  bool
	yes     bool
	title   string
	message string
	preview string
	action  confirmAction
	values  []string
	force   bool
}

type volumeDetailsState struct {
	name         string
	mountpoint   string
	entries      []volumeEntry
	totalEntries int
	loading      bool
	loaded       bool
	err          string
}

type snippetEditorOption struct {
	Name      string
	Available bool
}

type snippetEditorState struct {
	active       bool
	nameFocused  bool
	originalName string
	nameInput    textinput.Model
	options      []snippetEditorOption
	cursor       int
	marked       map[string]bool
}

type volumeBrowser struct {
	active       bool
	path         string
	entries      []volumeEntry
	cursor       int
	loading      bool
	err          string
	selectedFile string
	fileContent  string
	fileLoading  bool
}

type volumeFileContentMsg struct {
	path    string
	content string
	err     error
}

type tickMsg time.Time

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
	snippetEditor   snippetEditorState
	showHelp        bool
	markMode        bool
	marked          map[string]bool
	snippets        map[string][]string
	filteredSnips   []string
	snippetCursor   int
	snippetMarked   map[string]bool
	confirm         confirmDialog
	volumeDetails   volumeDetailsState
	volumeBrowser   volumeBrowser
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
