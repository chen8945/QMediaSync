package logstream

import (
	"bufio"
	"bytes"
	"io"
	"strings"
)

const defaultTailLimit = 1000

// ReadHistoryPage 反向读取完整历史页，返回翻页起点和本次读取终点。
func ReadHistoryPage(path string, position int64, limit int, expectedFileID string) ([]Entry, int64, int64, string, error) {
	if limit <= 0 {
		limit = 1000
	}
	file, stat, fileID, err := OpenFile(path, expectedFileID)
	if err != nil {
		return nil, 0, 0, "", err
	}
	defer file.Close()
	if expectedFileID != "" && position > stat.Size() {
		return nil, 0, 0, "", ErrCursorInvalid
	}
	eof := stat.Size()
	if position >= 0 {
		eof = min(eof, position)
	}
	if eof == 0 {
		return []Entry{}, 0, 0, fileID, nil
	}
	end := eof
	var last [1]byte
	if _, err = file.ReadAt(last[:], eof-1); err != nil {
		return nil, 0, 0, "", err
	}
	if last[0] == '\n' {
		end--
	}
	buffer := make([]byte, 64*1024)
	pos, start, needed := end, int64(0), limit
	found := false
	for pos > 0 && !found {
		offset := max(int64(0), pos-int64(len(buffer)))
		n, readErr := file.ReadAt(buffer[:pos-offset], offset)
		if readErr != nil && readErr != io.EOF {
			return nil, 0, 0, "", readErr
		}
		for i := n - 1; i >= 0; i-- {
			if buffer[i] == '\n' {
				needed--
				// 页预算为软限制，跨越预算的单行仍保持完整。
				if needed == 0 || eof-(offset+int64(i)+1) >= 2*1024*1024 {
					start = offset + int64(i) + 1
					found = true
					break
				}
			}
		}
		pos = offset
	}
	data := make([]byte, eof-start)
	if _, err = file.ReadAt(data, start); err != nil {
		return nil, 0, 0, "", err
	}
	lines := bytes.Split(data, []byte{'\n'})
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	entries := make([]Entry, 0, len(lines))
	cursor := start
	for _, line := range lines {
		cursor = min(cursor+int64(len(line))+1, eof)
		entry := ParseLine(strings.TrimSuffix(string(line), "\r"))
		entry.Cursor = cursor
		entries = append(entries, entry)
	}
	if last[0] != '\n' {
		entries[len(entries)-1].Cursor = 0
	}
	return entries, start, eof, fileID, nil
}

// ReadEntriesFromCursor 从 cursor 开始读取完整日志行。
func ReadEntriesFromCursor(path string, cursor int64, limit int, expectedFileID string) ([]Entry, int64, int64, error) {
	if limit <= 0 {
		limit = defaultTailLimit
	}
	file, stat, _, err := OpenFile(path, expectedFileID)
	if err != nil {
		return nil, 0, 0, err
	}
	defer file.Close()
	end := stat.Size()
	if cursor < 0 || cursor > end {
		return nil, cursor, end, ErrCursorInvalid
	}

	if _, err := file.Seek(cursor, io.SeekStart); err != nil {
		return nil, cursor, end, err
	}
	reader := bufio.NewReader(io.LimitReader(file, end-cursor))
	entries := make([]Entry, 0, limit)
	nextCursor := cursor
	for len(entries) < limit {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return nil, cursor, end, err
		}
		// 补读与实时投递一致，未完成尾行留待后续换行补齐。
		if !strings.HasSuffix(line, "\n") {
			break
		}
		nextCursor += int64(len(line))
		entry := ParseLine(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"))
		entry.Cursor = nextCursor
		entries = append(entries, entry)
		if nextCursor-cursor >= 64*1024 {
			break
		}
	}
	return entries, nextCursor, end, nil
}
