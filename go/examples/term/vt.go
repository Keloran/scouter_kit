package main

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// color is a terminal colour: 0 the default, 1...256 palette entry c-1,
// direct|rgb a direct colour.
type color uint32

const direct color = 1 << 24

type attr uint8

const (
	attrBold attr = 1 << iota
	attrFaint
	attrUnderline
	attrReverse
	attrInvisible
)

type style struct {
	fg, bg color
	attr   attr
}

// cell is one character cell; r 0 is the right half of a wide character.
type cell struct {
	r  rune
	st style
}

type cursor struct {
	x, y     int
	st       style
	wrapNext bool
	origin   bool
	charsets [4]byte
	gl       int
}

// vt is a VT100/xterm screen: the subset that shells and full-screen
// programs use (cursor movement, erasing, scroll regions, insert and delete,
// SGR attributes and colours, the alternate screen, DEC line drawing,
// UTF-8 with wide characters). There is no scrollback.
type vt struct {
	cols, rows int
	grid       [][]cell
	other      [][]cell // the main screen while the alternate one shows
	alt        bool
	cursor
	saved    [2]cursor // DECSC on the main and the alternate screen
	top, bot int       // the scroll region
	tabs     []bool
	autowrap bool
	insert   bool
	newline  bool
	cursorOn bool
	sync     bool // synchronized output (mode 2026) is on
	scrolled int  // whole-screen scroll-ups since the last take
	last     rune
	reply    func([]byte) // answers to status queries (nil: none)

	state  int
	params [][]int
	marker byte
	inter  []byte
	u      [4]byte
	un     int
}

const (
	sGround = iota
	sEsc
	sCsi
	sOsc
	sString // DCS, SOS, PM, APC: ignored
	sStrEsc
	sOscEsc
	sCharset
)

func newVT(cols, rows int) *vt {
	t := &vt{cols: cols, rows: rows}
	t.reset()
	return t
}

func (t *vt) reset() {
	t.grid, t.other = t.blankGrid(), t.blankGrid()
	t.alt = false
	t.cursor = cursor{charsets: [4]byte{'B', 'B', 'B', 'B'}}
	t.saved = [2]cursor{t.cursor, t.cursor}
	t.top, t.bot = 0, t.rows-1
	t.tabs = make([]bool, t.cols)
	for x := 8; x < t.cols; x += 8 {
		t.tabs[x] = true
	}
	t.autowrap, t.insert, t.newline, t.cursorOn, t.sync = true, false, false, true, false
	t.state = sGround
}

func (t *vt) blankGrid() [][]cell {
	g := make([][]cell, t.rows)
	for y := range g {
		g[y] = make([]cell, t.cols)
		for x := range g[y] {
			g[y][x] = cell{' ', style{}}
		}
	}
	return g
}

// blank is an erased cell: a space in the current background (as xterm).
func (t *vt) blank() cell { return cell{' ', style{bg: t.st.bg}} }

func (t *vt) Write(b []byte) (int, error) {
	for _, c := range b {
		t.byte(c)
	}
	return len(b), nil
}

func (t *vt) byte(c byte) {
	switch t.state {
	case sOsc:
		switch c {
		case 0x07:
			t.state = sGround
		case 0x1b:
			t.state = sOscEsc
		}
		return
	case sOscEsc, sStrEsc:
		t.state = sGround
		if c != '\\' {
			t.state = sEsc
			t.inter = t.inter[:0]
			t.esc(c)
		}
		return
	case sString:
		switch c {
		case 0x1b:
			t.state = sStrEsc
		case 0x07, 0x18, 0x1a:
			t.state = sGround
		}
		return
	}
	if c == 0x1b {
		t.un = 0
		t.state = sEsc
		t.inter = t.inter[:0]
		return
	}
	if c == 0x18 || c == 0x1a {
		t.state = sGround
		return
	}
	if c < 0x20 || c == 0x7f {
		if t.un > 0 {
			t.un = 0
			t.print(utf8.RuneError)
		}
		t.control(c)
		return
	}
	switch t.state {
	case sGround:
		t.text(c)
	case sEsc:
		t.esc(c)
	case sCharset:
		t.charsets[t.inter[0]] = c
		t.inter = t.inter[:0]
		t.state = sGround
	case sCsi:
		t.csiByte(c)
	}
}

// text decodes UTF-8 and prints.
func (t *vt) text(c byte) {
	if t.un > 0 {
		if c&0xc0 == 0x80 {
			t.u[t.un] = c
			t.un++
			if utf8.FullRune(t.u[:t.un]) {
				r, _ := utf8.DecodeRune(t.u[:t.un])
				t.un = 0
				t.print(r)
			}
			return
		}
		t.un = 0
		t.print(utf8.RuneError)
	}
	if c < 0x80 {
		t.print(rune(c))
		return
	}
	if c >= 0xc2 && c <= 0xf4 {
		t.u[0] = c
		t.un = 1
		return
	}
	t.print(utf8.RuneError)
}

func (t *vt) control(c byte) {
	switch c {
	case 0x08:
		if t.x > 0 {
			t.x--
		}
		t.wrapNext = false
	case 0x09:
		t.tab(1)
	case 0x0a, 0x0b, 0x0c:
		t.index()
		if t.newline {
			t.x = 0
		}
	case 0x0d:
		t.x = 0
		t.wrapNext = false
	case 0x0e:
		t.gl = 1
	case 0x0f:
		t.gl = 0
	}
}

func (t *vt) esc(c byte) {
	if c >= 0x20 && c <= 0x2f {
		if g := strings.IndexByte("()*+", c); g >= 0 && len(t.inter) == 0 {
			t.inter = append(t.inter, byte(g))
			t.state = sCharset
			return
		}
		t.inter = append(t.inter, c)
		return
	}
	t.state = sGround
	if len(t.inter) > 0 {
		if t.inter[0] == '#' && c == '8' {
			for y := range t.grid {
				for x := range t.grid[y] {
					t.grid[y][x] = cell{'E', style{}}
				}
			}
		}
		return
	}
	switch c {
	case '[':
		t.state = sCsi
		t.params = t.params[:0]
		t.marker = 0
	case ']':
		t.state = sOsc
	case 'P', 'X', '^', '_':
		t.state = sString
	case '7':
		t.saveCursor()
	case '8':
		t.restoreCursor()
	case 'D':
		t.index()
	case 'E':
		t.x = 0
		t.index()
	case 'M':
		t.reverseIndex()
	case 'H':
		t.tabs[t.x] = true
	case 'c':
		t.reset()
	}
}

func (t *vt) csiByte(c byte) {
	switch {
	case c >= '0' && c <= '9':
		if len(t.params) == 0 {
			t.params = append(t.params, []int{-1})
		}
		p := t.params[len(t.params)-1]
		v := &p[len(p)-1]
		if *v < 0 {
			*v = 0
		}
		*v = min(*v*10+int(c-'0'), 65535)
	case c == ';':
		if len(t.params) == 0 {
			t.params = append(t.params, []int{-1})
		}
		t.params = append(t.params, []int{-1})
	case c == ':':
		if len(t.params) == 0 {
			t.params = append(t.params, []int{-1})
		}
		t.params[len(t.params)-1] = append(t.params[len(t.params)-1], -1)
	case c >= '<' && c <= '?':
		t.marker = c
	case c >= 0x20 && c <= 0x2f:
		t.inter = append(t.inter, c)
	case c >= 0x40 && c <= 0x7e:
		t.state = sGround
		t.csi(c)
	}
}

// arg is parameter i, def if it is missing or 0.
func (t *vt) arg(i, def int) int {
	if i < len(t.params) && t.params[i][0] > 0 {
		return t.params[i][0]
	}
	return def
}

func (t *vt) csi(c byte) {
	if len(t.inter) > 0 {
		if t.inter[0] == '!' && c == 'p' {
			t.softReset()
		}
		return
	}
	n := t.arg(0, 1)
	switch t.marker {
	case '?':
		switch c {
		case 'h', 'l':
			for i := range t.params {
				t.privateMode(t.params[i][0], c == 'h')
			}
		case 'n':
			if t.arg(0, 0) == 6 {
				t.answer(fmt.Sprintf("\x1b[?%d;%d;1R", t.y+1, t.x+1))
			}
		case 'J':
			t.eraseDisplay(t.arg(0, 0))
		case 'K':
			t.eraseLine(t.arg(0, 0))
		}
		return
	case '>':
		if c == 'c' {
			t.answer("\x1b[>0;0;0c")
		}
		return
	case 0:
	default:
		return
	}
	switch c {
	case '@':
		t.insertCells(n)
	case 'A':
		t.moveRel(0, -n)
	case 'B', 'e':
		t.moveRel(0, n)
	case 'C', 'a':
		t.moveRel(n, 0)
	case 'D':
		t.moveRel(-n, 0)
	case 'E':
		t.moveRel(0, n)
		t.x = 0
	case 'F':
		t.moveRel(0, -n)
		t.x = 0
	case 'G', '`':
		t.moveTo(n-1, t.y)
	case 'H', 'f':
		y := t.arg(0, 1) - 1
		if t.origin {
			y += t.top
		}
		t.moveTo(t.arg(1, 1)-1, y)
	case 'I':
		t.tab(n)
	case 'J':
		t.eraseDisplay(t.arg(0, 0))
	case 'K':
		t.eraseLine(t.arg(0, 0))
	case 'L':
		t.insertLines(n)
	case 'M':
		t.deleteLines(n)
	case 'P':
		t.deleteCells(n)
	case 'S':
		t.scrollUp(t.top, t.bot, n)
	case 'T':
		t.scrollDown(t.top, t.bot, n)
	case 'X':
		for i := 0; i < n && t.x+i < t.cols && !t.wrapNext; i++ {
			t.put(t.y, t.x+i, t.blank())
		}
	case 'Z':
		t.tab(-n)
	case 'b':
		for i := 0; i < n && t.last != 0; i++ {
			t.print(t.last)
		}
	case 'c':
		if t.arg(0, 0) == 0 {
			t.answer("\x1b[?62;22c")
		}
	case 'd':
		y := n - 1
		if t.origin {
			y += t.top
		}
		t.moveTo(t.x, y)
	case 'g':
		switch t.arg(0, 0) {
		case 0:
			t.tabs[t.x] = false
		case 3:
			clear(t.tabs)
		}
	case 'h', 'l':
		for i := range t.params {
			switch t.params[i][0] {
			case 4:
				t.insert = c == 'h'
			case 20:
				t.newline = c == 'h'
			}
		}
	case 'm':
		t.sgr()
	case 'n':
		switch t.arg(0, 0) {
		case 5:
			t.answer("\x1b[0n")
		case 6:
			t.answer(fmt.Sprintf("\x1b[%d;%dR", t.y+1, t.x+1))
		}
	case 'r':
		top, bot := t.arg(0, 1)-1, t.arg(1, t.rows)-1
		bot = min(bot, t.rows-1)
		if top < bot {
			t.top, t.bot = top, bot
			t.home()
		}
	case 's':
		t.saveCursor()
	case 'u':
		t.restoreCursor()
	}
}

func (t *vt) answer(s string) {
	if t.reply != nil {
		t.reply([]byte(s))
	}
}

func (t *vt) privateMode(m int, on bool) {
	switch m {
	case 6:
		t.origin = on
		t.home()
	case 7:
		t.autowrap = on
		if !on {
			t.wrapNext = false
		}
	case 25:
		t.cursorOn = on
	case 47, 1047:
		t.altScreen(on, m == 1047 && !on)
	case 1048:
		if on {
			t.saveCursor()
		} else {
			t.restoreCursor()
		}
	case 1049:
		if on {
			t.saveCursor()
			t.altScreen(true, false)
			t.grid = t.blankGrid()
		} else {
			t.altScreen(false, false)
			t.restoreCursor()
		}
	case 2026:
		t.sync = on
	}
}

// altScreen switches between the screens; clear blanks the alternate one
// as it is left.
func (t *vt) altScreen(on, clear bool) {
	if on == t.alt {
		return
	}
	if clear {
		t.grid = t.blankGrid()
	}
	t.grid, t.other = t.other, t.grid
	t.alt = on
}

func (t *vt) saveCursor() {
	t.saved[t.screenIndex()] = t.cursor
}

func (t *vt) restoreCursor() {
	t.cursor = t.saved[t.screenIndex()]
	t.x, t.y = min(t.x, t.cols-1), min(t.y, t.rows-1)
}

func (t *vt) screenIndex() int {
	if t.alt {
		return 1
	}
	return 0
}

func (t *vt) softReset() {
	t.cursorOn, t.origin, t.autowrap, t.insert = true, false, true, false
	t.top, t.bot = 0, t.rows-1
	t.st = style{}
	t.charsets, t.gl = [4]byte{'B', 'B', 'B', 'B'}, 0
	t.saved[t.screenIndex()] = cursor{charsets: t.charsets}
}

func (t *vt) sgr() {
	if len(t.params) == 0 {
		t.st = style{}
		return
	}
	for i := 0; i < len(t.params); i++ {
		p := t.params[i]
		switch v := max(p[0], 0); {
		case v == 0:
			t.st = style{}
		case v == 1:
			t.st.attr |= attrBold
		case v == 2:
			t.st.attr |= attrFaint
		case v == 4 || v == 21:
			t.st.attr |= attrUnderline
			if len(p) > 1 && p[1] == 0 {
				t.st.attr &^= attrUnderline
			}
		case v == 7:
			t.st.attr |= attrReverse
		case v == 8:
			t.st.attr |= attrInvisible
		case v == 22:
			t.st.attr &^= attrBold | attrFaint
		case v == 24:
			t.st.attr &^= attrUnderline
		case v == 27:
			t.st.attr &^= attrReverse
		case v == 28:
			t.st.attr &^= attrInvisible
		case v >= 30 && v <= 37:
			t.st.fg = color(v - 30 + 1)
		case v == 39:
			t.st.fg = 0
		case v >= 40 && v <= 47:
			t.st.bg = color(v - 40 + 1)
		case v == 49:
			t.st.bg = 0
		case v >= 90 && v <= 97:
			t.st.fg = color(v - 90 + 8 + 1)
		case v >= 100 && v <= 107:
			t.st.bg = color(v - 100 + 8 + 1)
		case v == 38 || v == 48 || v == 58:
			var c color
			var used int
			if len(p) > 1 {
				c, _ = extended(p[1:], true)
			} else {
				c, used = extended(t.flat(i+1), false)
				i += used
			}
			switch v {
			case 38:
				t.st.fg = c
			case 48:
				t.st.bg = c
			}
		}
	}
}

// flat is the parameters from i on, one value each.
func (t *vt) flat(i int) []int {
	var out []int
	for ; i < len(t.params); i++ {
		out = append(out, t.params[i][0])
	}
	return out
}

// extended reads 5;n or 2;r;g;b (with colons also 2:cs:r:g:b) and returns
// the colour and how many values it used.
func extended(v []int, colons bool) (color, int) {
	at := func(i int) int {
		if i < len(v) {
			return min(max(v[i], 0), 255)
		}
		return 0
	}
	switch at(0) {
	case 5:
		return color(at(1) + 1), 2
	case 2:
		if colons && len(v) >= 5 {
			return direct | color(at(2)<<16|at(3)<<8|at(4)), 5
		}
		return direct | color(at(1)<<16|at(2)<<8|at(3)), 4
	}
	return 0, 1
}

func (t *vt) print(r rune) {
	if t.charsets[t.gl] == '0' && r >= 0x5f && r <= 0x7e {
		r = decGraphics[r-0x5f]
	}
	w := runeWidth(r)
	if w == 0 {
		return
	}
	t.last = r
	if t.wrapNext {
		t.x = 0
		t.index()
		t.wrapNext = false
	}
	if w == 2 && t.x == t.cols-1 {
		if !t.autowrap {
			return
		}
		t.put(t.y, t.x, t.blank())
		t.x = 0
		t.index()
	}
	if t.insert {
		t.insertCells(w)
	}
	t.put(t.y, t.x, cell{r, t.st})
	if w == 2 {
		t.put(t.y, t.x+1, cell{0, t.st})
	}
	t.x += w
	if t.x >= t.cols {
		t.x = t.cols - 1
		t.wrapNext = t.autowrap
	}
}

// put sets a cell, blanking the other half of a wide character it cuts.
func (t *vt) put(y, x int, c cell) {
	row := t.grid[y]
	if row[x].r == 0 && x > 0 && c.r != 0 {
		row[x-1] = cell{' ', row[x-1].st}
	}
	if x+1 < t.cols && row[x+1].r == 0 {
		row[x+1] = cell{' ', row[x+1].st}
	}
	row[x] = c
}

func (t *vt) index() {
	if t.y == t.bot {
		t.scrollUp(t.top, t.bot, 1)
	} else if t.y < t.rows-1 {
		t.y++
	}
}

func (t *vt) reverseIndex() {
	if t.y == t.top {
		t.scrollDown(t.top, t.bot, 1)
	} else if t.y > 0 {
		t.y--
	}
}

func (t *vt) blankRow() []cell {
	row := make([]cell, t.cols)
	for x := range row {
		row[x] = t.blank()
	}
	return row
}

func (t *vt) scrollUp(top, bot, n int) {
	n = min(n, bot-top+1)
	copy(t.grid[top:bot+1], t.grid[top+n:bot+1])
	for y := bot - n + 1; y <= bot; y++ {
		t.grid[y] = t.blankRow()
	}
	if top == 0 && bot == t.rows-1 {
		t.scrolled += n
	}
}

func (t *vt) scrollDown(top, bot, n int) {
	n = min(n, bot-top+1)
	copy(t.grid[top+n:bot+1], t.grid[top:bot+1-n])
	for y := top; y < top+n; y++ {
		t.grid[y] = t.blankRow()
	}
}

func (t *vt) insertLines(n int) {
	if t.y < t.top || t.y > t.bot {
		return
	}
	t.scrollDown(t.y, t.bot, n)
	t.x, t.wrapNext = 0, false
}

func (t *vt) deleteLines(n int) {
	if t.y < t.top || t.y > t.bot {
		return
	}
	t.scrollUp(t.y, t.bot, n)
	t.x, t.wrapNext = 0, false
}

func (t *vt) insertCells(n int) {
	row := t.grid[t.y]
	n = min(n, t.cols-t.x)
	copy(row[t.x+n:], row[t.x:t.cols-n])
	for i := 0; i < n; i++ {
		row[t.x+i] = t.blank()
	}
	t.wrapNext = false
}

func (t *vt) deleteCells(n int) {
	row := t.grid[t.y]
	n = min(n, t.cols-t.x)
	copy(row[t.x:], row[t.x+n:])
	for x := t.cols - n; x < t.cols; x++ {
		row[x] = t.blank()
	}
	t.wrapNext = false
}

// eraseLine erases from the cursor (0), up to it (1) or the whole line. As
// in tmux and VTE, a cursor waiting to wrap is past the last column.
func (t *vt) eraseLine(mode int) {
	from, to := t.x, t.cols
	if t.wrapNext {
		from = t.cols
	}
	switch mode {
	case 1:
		from, to = 0, t.x+1
	case 2:
		from = 0
	}
	for x := from; x < to; x++ {
		t.put(t.y, x, t.blank())
	}
}

func (t *vt) eraseDisplay(mode int) {
	switch mode {
	case 0:
		t.eraseLine(0)
		for y := t.y + 1; y < t.rows; y++ {
			t.grid[y] = t.blankRow()
		}
	case 1:
		t.eraseLine(1)
		for y := 0; y < t.y; y++ {
			t.grid[y] = t.blankRow()
		}
	case 2:
		for y := range t.grid {
			t.grid[y] = t.blankRow()
		}
	}
}

func (t *vt) home() {
	t.x, t.y, t.wrapNext = 0, 0, false
	if t.origin {
		t.y = t.top
	}
}

func (t *vt) moveTo(x, y int) {
	lo, hi := 0, t.rows-1
	if t.origin {
		lo, hi = t.top, t.bot
	}
	t.x, t.y, t.wrapNext = max(0, min(x, t.cols-1)), max(lo, min(y, hi)), false
}

// moveRel moves the cursor, stopping at the scroll region's edges when it
// starts inside it.
func (t *vt) moveRel(dx, dy int) {
	lo, hi := 0, t.rows-1
	if t.y >= t.top && t.y <= t.bot {
		lo, hi = t.top, t.bot
	}
	t.x, t.y, t.wrapNext = max(0, min(t.x+dx, t.cols-1)), max(lo, min(t.y+dy, hi)), false
}

// tab moves to the n-th next tab stop (n < 0: previous).
func (t *vt) tab(n int) {
	for ; n > 0 && t.x < t.cols-1; n-- {
		for t.x++; t.x < t.cols-1 && !t.tabs[t.x]; t.x++ {
		}
	}
	for ; n < 0 && t.x > 0; n++ {
		for t.x--; t.x > 0 && !t.tabs[t.x]; t.x-- {
		}
	}
	t.wrapNext = false
}

// decGraphics is the DEC special graphics set for 0x5f...0x7e.
var decGraphics = []rune(" ◆▒␉␌␍␊°±␤␋┘┐┌└┼⎺⎻─⎼⎽├┤┴┬│≤≥π≠£·")

// wide are the ranges of characters two cells wide (East Asian wide and
// full-width characters, emoji), close to what wcwidth says.
var wide = [][2]rune{
	{0x1100, 0x115f}, {0x231a, 0x231b}, {0x2329, 0x232a}, {0x23e9, 0x23ec}, {0x23f0, 0x23f0}, {0x23f3, 0x23f3},
	{0x25fd, 0x25fe}, {0x2614, 0x2615}, {0x2648, 0x2653}, {0x267f, 0x267f}, {0x2693, 0x2693}, {0x26a1, 0x26a1},
	{0x26aa, 0x26ab}, {0x26bd, 0x26be}, {0x26c4, 0x26c5}, {0x26ce, 0x26ce}, {0x26d4, 0x26d4}, {0x26ea, 0x26ea},
	{0x26f2, 0x26f3}, {0x26f5, 0x26f5}, {0x26fa, 0x26fa}, {0x26fd, 0x26fd}, {0x2705, 0x2705}, {0x270a, 0x270b},
	{0x2728, 0x2728}, {0x274c, 0x274c}, {0x274e, 0x274e}, {0x2753, 0x2755}, {0x2757, 0x2757}, {0x2795, 0x2797},
	{0x27b0, 0x27b0}, {0x27bf, 0x27bf}, {0x2b1b, 0x2b1c}, {0x2b50, 0x2b50}, {0x2b55, 0x2b55}, {0x2e80, 0x303e},
	{0x3041, 0x33ff}, {0x3400, 0x4dbf}, {0x4e00, 0x9fff}, {0xa000, 0xa4cf}, {0xa960, 0xa97f}, {0xac00, 0xd7a3},
	{0xf900, 0xfaff}, {0xfe10, 0xfe19}, {0xfe30, 0xfe6f}, {0xff00, 0xff60}, {0xffe0, 0xffe6}, {0x16fe0, 0x16fe4},
	{0x17000, 0x18aff}, {0x1b000, 0x1b2ff}, {0x1f004, 0x1f004}, {0x1f0cf, 0x1f0cf}, {0x1f18e, 0x1f18e},
	{0x1f191, 0x1f19a}, {0x1f200, 0x1f251}, {0x1f300, 0x1f64f}, {0x1f680, 0x1f6ff}, {0x1f7e0, 0x1f7eb},
	{0x1f90c, 0x1f9ff}, {0x1fa70, 0x1faff}, {0x20000, 0x3fffd},
}

func runeWidth(r rune) int {
	if r == 0x200b || r == 0x200c || r == 0x200d || r == 0x2060 || (r >= 0xfe00 && r <= 0xfe0f) ||
		unicode.In(r, unicode.Mn, unicode.Me) {
		return 0
	}
	if r < 0x1100 {
		return 1
	}
	for _, w := range wide {
		if r >= w[0] && r <= w[1] {
			return 2
		}
	}
	return 1
}

// take returns a copy of the screen and the whole-screen scroll-ups since
// the last take.
func (t *vt) take() (grid [][]cell, scrolled int) {
	grid = make([][]cell, t.rows)
	for y := range grid {
		grid[y] = append([]cell(nil), t.grid[y]...)
	}
	scrolled, t.scrolled = t.scrolled, 0
	return grid, scrolled
}
