package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

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
