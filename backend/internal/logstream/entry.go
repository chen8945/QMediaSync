package logstream

import (
	"regexp"
	"strings"
	"time"
)

// Entry 是前后端共享的日志条目结构。
type Entry struct {
	Level     string `json:"level"`
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
	Cursor    int64  `json:"cursor"`
}

var logLinePattern = regexp.MustCompile(`^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}\.\d{6}) \[(\w+)\] (.+)$`)

// ParseLine 解析日志行。
func ParseLine(line string) Entry {
	entry := Entry{
		Level:     "info",
		Message:   line,
		Timestamp: time.Now().Format("2006-01-02 15:04:05.000000"),
	}
	matches := logLinePattern.FindStringSubmatch(line)
	if len(matches) != 4 {
		return entry
	}

	entry.Timestamp = matches[1]
	switch strings.ToLower(matches[2]) {
	case "warn", "warning":
		entry.Level = "warn"
	case "error", "err":
		entry.Level = "error"
	case "debug":
		entry.Level = "debug"
	default:
		entry.Level = "info"
	}
	entry.Message = matches[3]
	return entry
}
