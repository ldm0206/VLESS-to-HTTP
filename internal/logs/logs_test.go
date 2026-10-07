package logs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRingBufferKeepsTheNewestEntries(t *testing.T) {
	logger, err := New(Options{Level: LevelDebug, RingSize: 5})
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}
	defer logger.Close()

	for i := 1; i <= 20; i++ {
		logger.Infof("第 %d 条", i)
	}

	entries := logger.Tail(Filter{Limit: 100})
	if len(entries) != 5 {
		t.Fatalf("ring kept %d entries, want 5", len(entries))
	}
	if !strings.Contains(entries[0].Msg, "16") || !strings.Contains(entries[4].Msg, "20") {
		t.Fatalf("wrong entries kept: %+v", entries)
	}
}

func TestLevelFiltering(t *testing.T) {
	logger, err := New(Options{Level: LevelWarning, RingSize: 100})
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}
	defer logger.Close()

	logger.Debugf("debug 不该出现")
	logger.Infof("info 不该出现")
	logger.Warnf("warning 应该出现")
	logger.Errorf("error 应该出现")

	all := logger.Tail(Filter{Limit: 100})
	if len(all) != 2 {
		t.Fatalf("expected 2 entries at warning level, got %d: %+v", len(all), all)
	}
	for _, e := range all {
		if e.Level == "info" || e.Level == "debug" {
			t.Fatalf("a filtered entry leaked: %+v", e)
		}
	}
}

func TestAccessLogCanBeDisabled(t *testing.T) {
	logger, err := New(Options{Level: LevelDebug, RingSize: 100, AccessLog: false})
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}
	defer logger.Close()

	logger.Access("alice", "from 1.2.3.4 accepted tcp:example.com:443")
	if entries := logger.Tail(Filter{Limit: 10}); len(entries) != 0 {
		t.Fatalf("access logging is off but entries appeared: %+v", entries)
	}

	if err := logger.SetOptions(Options{Level: LevelDebug, RingSize: 100, AccessLog: true}); err != nil {
		t.Fatalf("set options: %v", err)
	}
	logger.Access("alice", "from 1.2.3.4 accepted tcp:example.com:443")
	entries := logger.Tail(Filter{Limit: 10})
	if len(entries) != 1 || entries[0].User != "alice" {
		t.Fatalf("access entry missing: %+v", entries)
	}
}

func TestTailFiltersByUserAndQuery(t *testing.T) {
	logger, err := New(Options{Level: LevelDebug, RingSize: 100, AccessLog: true})
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}
	defer logger.Close()

	logger.Access("alice", "from 1.1.1.1 accepted tcp:a.example.com:443")
	logger.Access("bob", "from 2.2.2.2 accepted tcp:b.example.com:443")
	logger.Access("alice", "from 1.1.1.1 accepted tcp:c.example.com:80")

	if got := logger.Tail(Filter{User: "alice", Limit: 10}); len(got) != 2 {
		t.Fatalf("user filter returned %d entries", len(got))
	}
	if got := logger.Tail(Filter{Query: "b.example", Limit: 10}); len(got) != 1 {
		t.Fatalf("query filter returned %d entries", len(got))
	}
	if got := logger.Tail(Filter{Query: "B.EXAMPLE", Limit: 10}); len(got) != 1 {
		t.Fatalf("query filter should be case-insensitive, returned %d", len(got))
	}
	if got := logger.Tail(Filter{Since: 2, Limit: 10}); len(got) != 1 {
		t.Fatalf("since filter returned %d entries", len(got))
	}
}

func TestLogFileRotatesAtTheConfiguredSize(t *testing.T) {
	dir := t.TempDir()
	logger, err := New(Options{
		Level:      LevelDebug,
		Dir:        dir,
		MaxSizeMB:  1,
		MaxBackups: 2,
		RingSize:   50,
	})
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}

	// ~1.2 MB of lines, enough to roll over at least once.
	line := strings.Repeat("x", 200)
	for i := 0; i < 6000; i++ {
		logger.Infof("%04d %s", i, line)
	}

	// Give the writer goroutine a moment to drain.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, "v2h.log.1")); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	logger.Close()

	base := filepath.Join(dir, "v2h.log")
	rotated := base + ".1"
	info, err := os.Stat(rotated)
	if err != nil {
		t.Fatalf("expected a rotated file: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("the rotated file is empty")
	}

	current, err := os.Stat(base)
	if err != nil {
		t.Fatalf("stat current log: %v", err)
	}
	if current.Size() > 1<<20+64<<10 {
		t.Fatalf("current log grew past the limit: %d bytes", current.Size())
	}

	// MaxBackups=2 means .3 must never exist.
	if _, err := os.Stat(base + ".3"); err == nil {
		t.Fatal("more backups were kept than configured")
	}
}

func TestSubscribeReceivesLiveEntries(t *testing.T) {
	logger, err := New(Options{Level: LevelDebug, RingSize: 100})
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}
	defer logger.Close()

	ch, cancel := logger.Subscribe()
	defer cancel()

	logger.Infof("实时一条")

	select {
	case entry := <-ch:
		if !strings.Contains(entry.Msg, "实时一条") {
			t.Fatalf("unexpected entry: %+v", entry)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subscriber never received the entry")
	}
}

// A slow subscriber must not block the proxy path.
func TestSlowSubscriberIsDropped(t *testing.T) {
	logger, err := New(Options{Level: LevelDebug, RingSize: 10})
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}
	defer logger.Close()

	_, cancel := logger.Subscribe()
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 2000; i++ {
			logger.Infof("泛洪 %d", i)
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("logging blocked on a full subscriber channel")
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]Level{
		"debug": LevelDebug, "info": LevelInfo, "": LevelInfo,
		"warning": LevelWarning, "warn": LevelWarning,
		"error": LevelError, "none": LevelNone, "off": LevelNone,
		"unknown": LevelInfo,
	}
	for input, want := range cases {
		if got := ParseLevel(input); got != want {
			t.Fatalf("ParseLevel(%q) = %v, want %v", input, got, want)
		}
	}
}
