package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Level aliases slog.Level so callers do not need to import log/slog just to
// configure the logger.
type Level = slog.Level

const (
	LevelDebug = slog.LevelDebug
	LevelInfo  = slog.LevelInfo
	LevelWarn  = slog.LevelWarn
	LevelError = slog.LevelError
)

const timeLayout = "2006-01-02 15:04:05"

// levelIcons matches back/loaders/logger.ts so the syslog files stay visually
// consistent with the Node panel's output.
var levelIcons = map[slog.Level]string{
	slog.LevelDebug: "🐛",
	slog.LevelInfo:  "ℹ️",
	slog.LevelWarn:  "⚠️",
	slog.LevelError: "❌",
}

// Options configures the shared logger.
type Options struct {
	Dir        string
	Level      Level
	MaxBytes   int64
	MaxAgeDays int
	Console    io.Writer
	// ASCIIOnly replaces the emoji in startup narration with ASCII stand-ins.
	// Traditional cmd.exe cannot render emoji: its font has no glyphs for
	// them, so they degrade to "?" or an empty box.
	ASCIIOnly bool
}

// Logger bundles the slog logger with the rotating file it writes to.
type Logger struct {
	*slog.Logger
	file      *rotatingFile
	console   io.Writer
	mu        *sync.Mutex
	asciiOnly bool
}

// New creates a logger that writes to the console and to a daily rotated file
// under data/syslog.
func New(opts Options) (*Logger, error) {
	if opts.Console == nil {
		opts.Console = os.Stdout
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = 20 << 20
	}
	if opts.MaxAgeDays <= 0 {
		opts.MaxAgeDays = 7
	}

	file, err := newRotatingFile(opts.Dir, opts.MaxBytes, time.Duration(opts.MaxAgeDays)*24*time.Hour)
	if err != nil {
		return nil, err
	}

	handler := &handler{
		mu:      &sync.Mutex{},
		level:   opts.Level,
		console: opts.Console,
		file:    file,
	}
	return &Logger{
		Logger:    slog.New(handler),
		file:      file,
		console:   opts.Console,
		mu:        handler.mu,
		asciiOnly: opts.ASCIIOnly,
	}, nil
}

// Close flushes and closes the log file.
func (l *Logger) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	return l.file.Close()
}

// handler renders lines as:
//
//	[ℹ️info 2026-11-07 11:42:46]:message key=value
//
// The console copy omits the emoji, like winston's colorized transport.
type handler struct {
	mu      *sync.Mutex
	level   slog.Level
	console io.Writer
	file    io.Writer
	attrs   []slog.Attr
	groups  []string
}

func (h *handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *handler) Handle(_ context.Context, record slog.Record) error {
	if !h.Enabled(context.Background(), record.Level) {
		return nil
	}

	timestamp := record.Time
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	message := record.Message
	if message == "" {
		message = "(empty)"
	}

	var b strings.Builder
	b.WriteString(message)
	for _, attr := range h.attrs {
		writeAttr(&b, h.groups, attr)
	}
	record.Attrs(func(attr slog.Attr) bool {
		writeAttr(&b, h.groups, attr)
		return true
	})
	body := b.String()

	icon := levelIcons[record.Level]
	levelName := strings.ToLower(record.Level.String())
	fileLine := fmt.Sprintf("[%s%s %s]:%s\n", icon, levelName, timestamp.Format(timeLayout), body)
	consoleLine := fmt.Sprintf("[%s %s]:%s\n", levelName, timestamp.Format(timeLayout), body)

	h.mu.Lock()
	defer h.mu.Unlock()
	var firstErr error
	if h.console != nil {
		if _, err := io.WriteString(h.console, consoleLine); err != nil {
			firstErr = err
		}
	}
	if h.file != nil {
		if _, err := io.WriteString(h.file, fileLine); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	clone := *h
	clone.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &clone
}

func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	clone := *h
	clone.groups = append(append([]string{}, h.groups...), name)
	return &clone
}

func writeAttr(b *strings.Builder, groups []string, attr slog.Attr) {
	attr.Value = attr.Value.Resolve()
	if attr.Equal(slog.Attr{}) {
		return
	}

	key := attr.Key
	if len(groups) > 0 {
		key = strings.Join(groups, ".") + "." + key
	}
	if key == "" {
		key = "attr"
	}

	b.WriteByte(' ')
	b.WriteString(key)
	b.WriteByte('=')

	if attr.Value.Kind() == slog.KindString {
		b.WriteString(quoteIfNeeded(attr.Value.String()))
		return
	}
	b.WriteString(quoteIfNeeded(attr.Value.String()))
}

func quoteIfNeeded(value string) string {
	if value == "" {
		return `""`
	}
	if strings.ContainsAny(value, " \t\n\"=") {
		return strconv.Quote(value)
	}
	return value
}

// ---------------------------------------------------------------------------
// Startup narration
//
// Booting the panel is a handful of steps an operator watches in the console.
// These helpers render them exactly like the container image does, so a
// Windows user sees the familiar progression instead of raw structured logs:
//
//	[2026-11-08 00:05:00] [   INFO]  🚀  1. 检测配置文件...
//	[2026-11-08 00:05:00] [   INFO]  配置文件  D:\qinglong\data\config\config.sh
//	[2026-11-08 00:05:06] [SUCCESS]  🎉  青龙面板启动成功!
//
// They bypass slog on purpose: structured records stay machine readable in
// data/syslog for the running panel, while the console gets the narration.
// ---------------------------------------------------------------------------

// stepLabel pads a level name to the seven characters the panel uses
// ("   INFO", "  ERROR", "SUCCESS") so the columns line up.
func stepLabel(name string) string {
	for len(name) < 7 {
		name = " " + name
	}
	return name
}

// asciiIcons maps the emoji used by the startup narration to stand-ins that
// every console font can draw.
var asciiIcons = map[string]string{
	"🚀": ">",
	"📦": ">",
	"🔍": ">",
	"⚙️": ">",
	"🌐": ">",
	"⏳": ">",
	"🎉": "OK",
	"👋": "OK",
	"🛑": "--",
	"❌": "!!",
	"⚠️": "!",
}

// icon returns the emoji when the console can render it, an ASCII stand-in
// otherwise.
func (l *Logger) icon(emoji string) string {
	if !l.asciiOnly {
		return emoji
	}
	if alt, ok := asciiIcons[emoji]; ok {
		return alt
	}
	return "*"
}

// raw writes one already-formatted line to the console and the syslog file.
func (l *Logger) raw(line string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.console != nil {
		_, _ = io.WriteString(l.console, line)
	}
	if l.file != nil {
		_, _ = io.WriteString(l.file, line)
	}
}

// Banner prints the two-line header shown before the first step. It is plain
// text (no timestamp) because it describes the process, not an event.
func (l *Logger) Banner(title string, lines ...string) {
	var b strings.Builder
	b.WriteString("\n  ")
	b.WriteString(title)
	b.WriteString("\n")
	for _, line := range lines {
		b.WriteString("  ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	l.raw(b.String())
}

// Step prints a numbered startup step, e.g. "🚀  1. 检测配置文件...". Steps
// after the first are preceded by a blank line so the console reads as groups
// instead of one dense block.
func (l *Logger) Step(icon string, index int, format string, args ...any) {
	prefix := ""
	if index > 1 {
		prefix = "\n"
	}
	l.raw(fmt.Sprintf("%s[%s] [%s]  %s  %d. %s\n", prefix,
		time.Now().Format(timeLayout), stepLabel("INFO"), l.icon(icon), index,
		fmt.Sprintf(format, args...)))
}

// Detail prints an indented line under the current step.
func (l *Logger) Detail(label, format string, args ...any) {
	l.raw(fmt.Sprintf("[%s] [%s]  %s  %s\n",
		time.Now().Format(timeLayout), stepLabel("INFO"), label,
		fmt.Sprintf(format, args...)))
}

// DetailWarn is Detail at WARN level, for missing optional tooling.
func (l *Logger) DetailWarn(label, format string, args ...any) {
	l.raw(fmt.Sprintf("[%s] [%s]  %s  %s\n",
		time.Now().Format(timeLayout), stepLabel("WARN"), label,
		fmt.Sprintf(format, args...)))
}

// Success prints the final line of a startup sequence.
func (l *Logger) Success(icon, format string, args ...any) {
	l.raw(fmt.Sprintf("[%s] [%s]  %s  %s\n",
		time.Now().Format(timeLayout), stepLabel("SUCCESS"), l.icon(icon),
		fmt.Sprintf(format, args...)))
}
