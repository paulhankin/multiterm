// Package multiterm provides a console-based display of log messages
// from multiple workers, processes, goroutines, etc.
package multiterm

import (
	"fmt"
	"sync"

	termbox "github.com/nsf/termbox-go"
)

// Logger provides worker-specific log-based messages.
type Logger interface {
	Close() // Should be called to close down the terminal.

	// These methods provide log messages to the terminal.

	// A neutral message from the given worker.
	Infof(worker int, fmts string, args ...interface{})

	// A positive message from the given worker (displayed in green).
	Goodf(worker int, fmts string, args ...interface{})

	// A negative message from the given worker (displayed in red).
	Badf(worker int, fmts string, args ...interface{})

	// A message that the given worker has finished.
	Exitf(worker int, fmts string, args ...interface{})

	// A global progress update. It can be sent from any worker, and
	// is displayed at the bottom of the console.
	Progressf(worker int, fmts string, args ...interface{})
}

// NewLogger provides N windows on the console showing log messages
// from N workers.
// The function ctrlc is called if Control-C is pressed, since
// in raw terminal mode ctrl-c will be captured.
// Typically, this will be something like func() { log.Fatalf("ctrl-c pressed") }
func NewLogger(N int, ctrlc func()) (Logger, error) {
	if err := termbox.Init(); err != nil {
		return nil, err
	}
	termbox.HideCursor()
	mode := termbox.SetOutputMode(termbox.Output256)
	w, h := termbox.Size()
	t := &terminal{
		w:      w,
		h:      h,
		exit:   ctrlc,
		input:  make(chan workerMsg, 10),
		worker: make([]*reporter, N+1),
		quit:   make(chan struct{}),
	}
	if mode == termbox.Output256 {
		t.col256 = true
	}
	for i := 0; i < N+1; i++ {
		t.worker[i] = new(reporter)
		t.worker[i].dirty = true
		fg := termbox.ColorWhite | termbox.AttrBold
		if t.col256 {
			fg = 0x10
		}
		t.worker[i].title = coloredLine{0x0, fg, []rune(fmt.Sprintf("---===WORKER %d/%d===---", i+1, N))}
		if i == N {
			t.worker[i].center = true
		}
	}
	if err := t.init(); err != nil {
		return nil, err
	}
	return t, nil
}

// NewBasicLogger is a plain-text colorized console logger.
// It interleaves messages from the workers on stdout.
func NewBasicLogger(N int) Logger {
	return plainTerminal{}
}

// terminal is the termbox-based ui
type terminal struct {
	w, h   int  // The current width and height of the terminal
	col256 bool // If we're in a 256-color terminal
	input  chan workerMsg
	worker []*reporter // One per worker, and one for reporting progress at the bottom of the screen
	exit   func()      // A function that's called when ctrl-c is pressed.

	quit     chan struct{}  // closed when terminal.Close() is called.
	waitDone sync.WaitGroup // used to signal when worker goroutines are closing down
}

// coloredLine is one line of output on one worker's terminal
type coloredLine struct {
	bg, fg termbox.Attribute
	line   []rune
}

// reporter records state for one window.
type reporter struct {
	m       sync.Mutex
	dirty   bool          // whether this reporter needs redrawing
	center  bool          // whether to center the lines in the window
	title   coloredLine   // what to show in the top line (if there's space)
	history []coloredLine // the last so-many lines of terminal output
}

// workerMsg is a log line from a particular worker
type workerMsg struct {
	worker int
	line   coloredLine
}

func (w *reporter) setDirty() {
	w.m.Lock()
	defer w.m.Unlock()
	w.dirty = true
}

// readInput reads worker messages, and funnels them to the appropriate
// reporter.
func (t *terminal) readInput() {
	for msg := range t.input {
		w := t.worker[msg.worker]
		func() {
			w.m.Lock()
			defer w.m.Unlock()
			w.dirty = true
			w.history = append(w.history, msg.line)
			for len(w.history) > 200 {
				w.history = w.history[1:]
			}
		}()
		// We use Interrupt() to signal the redrawing code to take another look
		termbox.Interrupt()
	}
	fmt.Println("readInput Done")
	t.waitDone.Done()
}

// Close shuts down the terminal, returning the original underlying terminal.
func (t *terminal) Close() {
	t.waitDone.Add(1)
	close(t.input) // signal readInput to close
	t.waitDone.Wait()

	t.waitDone.Add(1)
	close(t.quit)       // signal workers to close
	termbox.Interrupt() // make sure run() gets triggered.
	t.waitDone.Wait()

	termbox.Close()
}

// redrawIfDirty draws, if the dirty bit is set, the last few lines
// of history. It's drawn at screen location x,y with the given lines
// and cols as the size of the pseudo-window.
// It reports whether anything was redrawn.
func (w *reporter) redrawIfDirty(t *terminal, x, y, lines, cols int, bg termbox.Attribute) bool {
	w.m.Lock()
	defer w.m.Unlock()
	if !w.dirty {
		return false
	}
	w.dirty = false
	line0 := len(w.history) - lines + 1
	if line0 > 0 {
		line0 = 0
	}
	for j := 0; j < lines; j++ {
		lidx := len(w.history) - lines + j - line0
		line := []rune{}
		fg := termbox.ColorWhite
		center := w.center
		if lidx >= 0 && lidx < len(w.history) {
			line = w.history[lidx].line
			fg = w.history[lidx].fg
		}
		if j == 0 && lines > 1 {
			line = w.title.line
			fg = w.title.fg
			center = true
		}
		for i := 0; i < cols; i++ {
			var ch rune = ' '
			ioff := 0
			if center {
				ioff = -(cols / 2) + (len(line) / 2)
			}
			if i+ioff >= 0 && i+ioff < len(line) {
				ch = line[i+ioff]
			}
			termbox.SetCell(i+x, j+y, ch, fg, bg)
		}
	}
	return true
}

func wastedSpace(N, w, h, c, r int) float64 {
	W := float64(w) / float64(c)
	H := float64(h) / float64(r)
	wastedSquares := float64(c*r - N)
	ww := W
	wastedPerLine := 0.0
	for _, subs := range []float64{60, 80} {
		ww -= subs
		if ww > 0 {
			wastedPerLine += ww * 0.3
		}
	}
	return wastedSquares*W*H + wastedPerLine*(H-1)*float64(N)
}

func betterShape(N int, w, h int, c1, r1 int, c2, r2 int) bool {
	w1 := wastedSpace(N, w, h, c1, r1)
	w2 := wastedSpace(N, w, h, c2, r2)
	return w1 < w2
}

// run is the event-handling loop for the terminal.
func (t *terminal) run() {
	setBG := true
	for {
		evt := termbox.PollEvent()
		switch evt.Type {
		case termbox.EventKey:
			if evt.Key == termbox.KeyCtrlC {
				termbox.Close()
				t.exit()
			}
			if evt.Key == termbox.KeyCtrlL {
				termbox.Sync()
			}
		case termbox.EventResize:
			t.w = evt.Width
			t.h = evt.Height
			for i := range t.worker {
				t.worker[i].setDirty()
			}
			setBG = true
			fallthrough
		case termbox.EventInterrupt:
			select {
			case <-t.quit:
				fmt.Printf("run() done")
				t.waitDone.Done()
				return
			default:
			}
			cols, rows := 1, len(t.worker)-1
			for c := 1; c < len(t.worker)-1; c++ {
				if t.w/c < 50 {
					continue
				}
				r := (len(t.worker) - 1) / c
				if c*r < len(t.worker)-1 {
					r++
				}
				if betterShape(len(t.worker)-1, t.w, t.h, c, r, cols, rows) {
					cols, rows = c, r
				}
			}
			dirty := false
			for i := 0; i < rows*cols+1; i++ {
				col := i % cols
				row := i / cols
				bg := termbox.ColorBlack
				H := (t.h-1)*(row+1)/rows - (t.h-1)*row/rows
				W := (t.w)*(col+1)/cols - (t.w)*col/cols
				x, y := (t.w)*col/cols, (t.h-1)*row/rows
				hh, ww := H, W
				var worker *reporter
				if i == rows*cols {
					x = 0
					y = t.h - 1
					hh = 1
					ww = t.w
					worker = t.worker[len(t.worker)-1]
				} else if i < len(t.worker)-1 {
					worker = t.worker[i]
					if (row^col)&1 == 1 && t.col256 {
						bg = 0xe9 + 3
					}
				}
				if worker != nil {
					if worker.redrawIfDirty(t, x, y, hh, ww, bg) {
						dirty = true
					}
				} else if setBG {
					dirty = true
					for c := 0; c < W; c++ {
						for r := 0; r < H; r++ {
							ch := rune(' ')
							if (c^r)&1 == 1 {
								ch = '.'
							}
							termbox.SetCell(x+c, y+r, ch, termbox.ColorBlue, termbox.ColorBlack)
						}
					}
				}
			}
			if setBG {
				setBG = false
			}
			if dirty {
				termbox.SetCursor(0, 0)
				termbox.Flush()
			}
		}
	}
}

// init stars up the processes for the terminal.
func (t *terminal) init() error {
	if err := termbox.Clear(termbox.ColorWhite, termbox.ColorBlack); err != nil {
		return err
	}
	go t.readInput()
	go t.run()
	return nil
}

// level is the choice of reporting levels.
type level int

const (
	infoLev     level = 0
	okLev       level = 1
	failLev     level = 2
	exitLev     level = 3
	progressLev level = 4
)

func attrs(l level) (bg, fg termbox.Attribute) {
	fg = termbox.ColorMagenta
	switch l {
	case infoLev:
		fg = termbox.ColorWhite
	case okLev:
		fg = termbox.ColorGreen
	case failLev:
		fg = termbox.ColorRed
	case exitLev:
		fg = termbox.ColorCyan
	case progressLev:
		fg = termbox.ColorMagenta
	}
	return termbox.ColorBlack, fg
}

func (t *terminal) levPrintf(worker int, level level, fmts string, args ...interface{}) {
	bg, fg := attrs(level)
	msg := fmt.Sprintf(fmts, args...)
	if worker < len(t.worker)-1 {
		msg = "- " + msg
	}
	t.input <- workerMsg{worker, coloredLine{bg, fg, []rune(msg)}}
}

func (t *terminal) Infof(worker int, fmts string, args ...interface{}) {
	t.levPrintf(worker, infoLev, fmts, args...)
}
func (t *terminal) Goodf(worker int, fmts string, args ...interface{}) {
	t.levPrintf(worker, okLev, fmts, args...)
}
func (t *terminal) Badf(worker int, fmts string, args ...interface{}) {
	t.levPrintf(worker, failLev, fmts, args...)
}
func (t *terminal) Exitf(worker int, fmts string, args ...interface{}) {
	t.levPrintf(worker, exitLev, fmts, args...)
}
func (t *terminal) Progressf(worker int, fmts string, args ...interface{}) {
	// Progress messages always go to the last, special, worker
	t.levPrintf(len(t.worker)-1, progressLev, fmts, args...)
}

// plainTerminal is an alternate logger to the termbox-based UI. It simply
// prints colored messages to stdout.
type plainTerminal struct{}

func col(cc string) func(string) string {
	return func(s string) string {
		return "\033[0;" + cc + "m" + s + "\033[0m"
	}
}

var (
	colRed     = col("31")
	colGreen   = col("32")
	colCyan    = col("36")
	colMagenta = col("35")
)

func colorize(l level, s string) string {
	switch l {
	case okLev:
		return colGreen(s)
	case failLev:
		return colRed(s)
	case exitLev:
		return colCyan(s)
	case progressLev:
		return colMagenta(s)
	}
	return s
}

func plainPrintf(worker int, level level, fmts string, args ...interface{}) {
	msg := fmt.Sprintf(fmts, args...)
	msg = fmt.Sprintf("[%d]: ", worker) + msg

	fmt.Printf("%s\n", colorize(level, msg))
}

func (plainTerminal) Infof(worker int, fmts string, args ...interface{}) {
	plainPrintf(worker, infoLev, fmts, args...)
}
func (plainTerminal) Goodf(worker int, fmts string, args ...interface{}) {
	plainPrintf(worker, okLev, fmts, args...)
}
func (plainTerminal) Badf(worker int, fmts string, args ...interface{}) {
	plainPrintf(worker, failLev, fmts, args...)
}
func (plainTerminal) Exitf(worker int, fmts string, args ...interface{}) {
	plainPrintf(worker, exitLev, fmts, args...)
}
func (plainTerminal) Progressf(worker int, fmts string, args ...interface{}) {
	plainPrintf(worker, progressLev, fmts, args...)
}
func (plainTerminal) Close() {}
