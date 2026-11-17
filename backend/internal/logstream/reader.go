package logstream

import (
	"bufio"
	"io"
	"os"
	"strings"
)

const defaultTailLimit = 1000

// ReadTailEntries 读取文件末尾最近 limit 行，并返回文件末尾 cursor。
func ReadTailEntries(path string, limit int) ([]Entry, int64, error) {
	if limit <= 0 {
		limit = defaultTailLimit
	}
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []Entry{}, 0, nil
		}
		return nil, 0, err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return nil, 0, err
	}
	cursor := stat.Size()
	lines := make([]string, 0, limit)
	reader := bufio.NewReader(io.LimitReader(file, cursor))
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			lines = append(lines, strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"))
			if len(lines) > limit {
				lines = lines[1:]
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, err
		}
	}

	entries := make([]Entry, 0, len(lines))
	for _, line := range lines {
		entries = append(entries, ParseLine(line))
	}
	return entries, cursor, nil
}

// ReadEndCursor 返回文件末尾 cursor，不读取和解析日志内容。
func ReadEndCursor(path string) (int64, error) {
	stat, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	return stat.Size(), nil
}

// ReadEntriesFromCursor 从 cursor 开始读取完整日志行。
func ReadEntriesFromCursor(path string, cursor int64, limit int) ([]Entry, int64, error) {
	if limit <= 0 {
		limit = defaultTailLimit
	}
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []Entry{}, 0, nil
		}
		return nil, 0, err
	}
	defer file.Close()

	if _, err := file.Seek(cursor, io.SeekStart); err != nil {
		return nil, cursor, err
	}
	reader := bufio.NewReader(file)
	entries := make([]Entry, 0, limit)
	nextCursor := cursor
	for len(entries) < limit {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return nil, cursor, err
		}
		// 补读与实时投递一致，未完成尾行留待后续换行补齐。
		if !strings.HasSuffix(line, "\n") {
			break
		}
		nextCursor += int64(len(line))
		entry := ParseLine(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"))
		entry.Cursor = nextCursor
		entries = append(entries, entry)
	}
	return entries, nextCursor, nil
}
