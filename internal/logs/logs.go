// Package logs keeps the runtime log in three places at once: an in-memory
// ring the panels read, a size-rotated file, and (optionally) stdout.
//
// The proxy path must never block on logging, so ring inserts are a single
// mutex-guarded append and everything slow happens on one writer goroutine
// that drops entries rather than back-pressuring a connection.
package logs

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Level is a log severity.
type Level int

// Severities, ordered so that a configured level filters everything below it.
const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarning
	LevelError
	LevelNone
)

// ParseLevel maps the config spelling onto a Level.
func ParseLevel(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return LevelDebug
	case "info", "":
		return LevelInfo
	case "warning", "warn":
		return LevelWarning
	case "error":
		return LevelError
	case "none", "off", "silent":
		return LevelNone
	default:
		return LevelInfo
	}
}

func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "debug"
	case LevelInfo:
		return "info"
	case LevelWarning:
		return "warning"
	case LevelError:
		return "error"
	default:
		return "none"
	}
}

// Entry is one log line as the panels see it.
type Entry struct {
	Seq    uint64    `json:"seq"`
	Time   time.Time `json:"time"`
	Level  string    `json:"level"`
	Source string    `json:"source"` // v2h | xray | access
	User   string    `json:"user,omitempty"`
	Msg    string    `json:"msg"`
}

// Options configures a Logger.
type Options struct {
	Level      Level
	AccessLog  bool
	Dir        string
	MaxSizeMB  int
	MaxBackups int
	RingSize   int
	Console    bool
}

// Logger is the process-wide log sink.
type Logger struct {
	mu    sync.RWMutex
	ring  []Entry
	next  uint64
	opts  Options
	subs  map[int]chan Entry
	subID int

	file    *os.File
	fileSz  int64
	writeCh chan Entry
	done    chan struct{}
	once    sync.Once

	dropped uint64
}

// New creates the logger and opens the log file. A failure to open the file is
// reported but not fatal: the ring buffer still works.
func New(opts Options) (*Logger, error) {
	if opts.RingSize <= 0 {
		opts.RingSize = 2000
	}
	l := &Logger{
		opts:    opts,
		ring:    make([]Entry, 0, opts.RingSize),
		subs:    map[int]chan Entry{},
		writeCh: make(chan Entry, 4096),
		done:    make(chan struct{}),
	}
	var err error
	if opts.Dir != "" {
		if err = l.openFile(); err != nil {
			err = fmt.Errorf("打开日志文件失败：%w", err)
		}
	}
	go l.run()
	return l, err
}

// Options returns the active configuration.
func (l *Logger) Options() Options {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.opts
}

// SetOptions applies new settings, reopening the file when the path changed.
func (l *Logger) SetOptions(opts Options) error {
	if opts.RingSize <= 0 {
		opts.RingSize = 2000
	}
	l.mu.Lock()
	needReopen := opts.Dir != l.opts.Dir
	l.opts = opts
	l.mu.Unlock()

	if needReopen {
		l.mu.Lock()
		if l.file != nil {
			l.file.Close()
			l.file = nil
		}
		l.mu.Unlock()
		if opts.Dir != "" {
			return l.openFile()
		}
	}
	return nil
}

func (l *Logger) openFile() error {
	dir := l.opts.Dir
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "v2h.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	st, _ := f.Stat()
	l.mu.Lock()
	l.file = f
	if st != nil {
		l.fileSz = st.Size()
	}
	l.mu.Unlock()
	return nil
}

// Close flushes and stops the writer goroutine.
func (l *Logger) Close() {
	l.once.Do(func() {
		close(l.done)
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.file != nil {
			l.file.Sync()
			l.file.Close()
			l.file = nil
		}
	})
}

// Debugf, Infof, Warnf and Errorf write a v2h-sourced line.
func (l *Logger) Debugf(format string, args ...any) { l.log(LevelDebug, "v2h", "", format, args...) }
func (l *Logger) Infof(format string, args ...any)  { l.log(LevelInfo, "v2h", "", format, args...) }
func (l *Logger) Warnf(format string, args ...any)  { l.log(LevelWarning, "v2h", "", format, args...) }
func (l *Logger) Errorf(format string, args ...any) { l.log(LevelError, "v2h", "", format, args...) }

// Access records a proxy connection. Access lines are emitted regardless of
// the configured severity as long as access logging is on.
func (l *Logger) Access(user, format string, args ...any) {
	l.mu.RLock()
	enabled := l.opts.AccessLog
	l.mu.RUnlock()
	if !enabled {
		return
	}
	l.emit(Entry{
		Time:   time.Now(),
		Level:  "info",
		Source: "access",
		User:   user,
		Msg:    fmt.Sprintf(format, args...),
	})
}

// FromXray records a line that came out of the Xray core.
func (l *Logger) FromXray(level Level, msg string) {
	l.log(level, "xray", "", "%s", msg)
}

func (l *Logger) log(level Level, source, user, format string, args ...any) {
	l.mu.RLock()
	min := l.opts.Level
	l.mu.RUnlock()
	if level < min || min == LevelNone {
		return
	}
	l.emit(Entry{
		Time:   time.Now(),
		Level:  level.String(),
		Source: source,
		User:   user,
		Msg:    fmt.Sprintf(format, args...),
	})
}

func (l *Logger) emit(e Entry) {
	l.mu.Lock()
	l.next++
	e.Seq = l.next
	l.ring = append(l.ring, e)
	if len(l.ring) > l.opts.RingSize {
		l.ring = append(l.ring[:0], l.ring[len(l.ring)-l.opts.RingSize:]...)
	}
	subs := make([]chan Entry, 0, len(l.subs))
	for _, ch := range l.subs {
		subs = append(subs, ch)
	}
	console := l.opts.Console
	l.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- e:
		default:
			// A slow reader must not hold up the proxy.
		}
	}

	select {
	case l.writeCh <- e:
	default:
		l.mu.Lock()
		l.dropped++
		l.mu.Unlock()
	}

	if console {
		fmt.Fprintf(os.Stdout, "%s %-7s %-6s %s\n",
			e.Time.Format("2006-01-02 15:04:05.000"), e.Level, e.Source, e.Msg)
	}
}

// Dropped reports how many entries were skipped because the writer fell behind.
func (l *Logger) Dropped() uint64 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.dropped
}

func (l *Logger) run() {
	for {
		select {
		case <-l.done:
			return
		case e := <-l.writeCh:
			l.writeFile(e)
		}
	}
}

func (l *Logger) writeFile(e Entry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return
	}
	line := fmt.Sprintf("%s %-7s %-6s %s\n",
		e.Time.Format("2006-01-02 15:04:05.000"), e.Level, e.Source, e.Msg)
	n, err := l.file.WriteString(line)
	if err != nil {
		return
	}
	l.fileSz += int64(n)
	if l.opts.MaxSizeMB > 0 && l.fileSz >= int64(l.opts.MaxSizeMB)<<20 {
		l.rotateLocked()
	}
}

// rotateLocked shifts v2h.log → v2h.log.1 → … and drops the oldest.
func (l *Logger) rotateLocked() {
	if l.file != nil {
		l.file.Close()
		l.file = nil
	}
	base := filepath.Join(l.opts.Dir, "v2h.log")
	backups := l.opts.MaxBackups
	if backups < 1 {
		backups = 1
	}
	os.Remove(fmt.Sprintf("%s.%d", base, backups))
	for i := backups - 1; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", base, i)
		if _, err := os.Stat(src); err == nil {
			os.Rename(src, fmt.Sprintf("%s.%d", base, i+1))
		}
	}
	if _, err := os.Stat(base); err == nil {
		os.Rename(base, base+".1")
	}
	if f, err := os.OpenFile(base, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		l.file = f
		l.fileSz = 0
	}
}

// Filter narrows a Tail query.
type Filter struct {
	Since uint64
	Level Level
	User  string
	Query string
	Limit int
}

// Tail returns the most recent matching entries, oldest first.
func (l *Logger) Tail(f Filter) []Entry {
	l.mu.RLock()
	ring := l.ring
	level := l.opts.Level
	l.mu.RUnlock()

	if f.Level == 0 {
		f.Level = level
	}
	query := strings.ToLower(f.Query)

	out := make([]Entry, 0, 64)
	for i := len(ring) - 1; i >= 0; i-- {
		e := ring[i]
		if e.Seq <= f.Since {
			break
		}
		if f.User != "" && !strings.EqualFold(e.User, f.User) {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(e.Msg), query) {
			continue
		}
		if level != LevelNone && ParseLevel(e.Level) < level {
			continue
		}
		out = append(out, e)
		if f.Limit > 0 && len(out) >= f.Limit {
			break
		}
	}

	// Collected newest-first; hand back oldest-first.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// Subscribe returns a channel of live entries plus an unsubscribe function.
func (l *Logger) Subscribe() (<-chan Entry, func()) {
	ch := make(chan Entry, 256)
	l.mu.Lock()
	id := l.subID
	l.subID++
	l.subs[id] = ch
	l.mu.Unlock()

	cancel := func() {
		l.mu.Lock()
		if existing, ok := l.subs[id]; ok {
			delete(l.subs, id)
			close(existing)
		}
		l.mu.Unlock()
	}
	return ch, cancel
}

// Levels lists the severities the panel can filter on.
func Levels() []string { return []string{"debug", "info", "warning", "error"} }

// SortBySeq orders entries by sequence; used by tests and the TUI.
func SortBySeq(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Seq < entries[j].Seq })
}
