package logstream

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadLongEntriesPreservesTailAndCursor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	longLine := "头部" + strings.Repeat("中", 360000) + "尾部"
	first := "2025/11/29 12:33:09.000001 [INFO] first\n"
	second := "2025/11/29 12:33:10.000001 [DEBUG] " + longLine + "\n"
	last := "2025/11/29 12:33:11.000001 [INFO] last"
	content := first + second + last
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, cursor, err := ReadTailEntries(path, 2)
	if err != nil || len(entries) != 2 || cursor != int64(len(content)) {
		t.Fatalf("tail: entries=%d cursor=%d err=%v", len(entries), cursor, err)
	}
	if entries[0].Message != longLine || entries[1].Message != "last" {
		t.Fatal("tail lost long-line content or EOF line")
	}

	entries, cursor, err = ReadEntriesFromCursor(path, 0, 1)
	if err != nil || len(entries) != 1 || entries[0].Message != "first" || cursor != int64(len(first)) {
		t.Fatalf("first page: entries=%d cursor=%d err=%v", len(entries), cursor, err)
	}
	entries, cursor, err = ReadEntriesFromCursor(path, cursor, 1)
	if err != nil || len(entries) != 1 || entries[0].Message != longLine || cursor != int64(len(first+second)) || entries[0].Cursor != cursor {
		t.Fatalf("long page: entries=%d cursor=%d err=%v", len(entries), cursor, err)
	}
	entries, nextCursor, err := ReadEntriesFromCursor(path, cursor, 1)
	if err != nil || len(entries) != 0 || nextCursor != cursor {
		t.Fatalf("partial EOF: entries=%d cursor=%d err=%v", len(entries), nextCursor, err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	entries, nextCursor, err = ReadEntriesFromCursor(path, cursor, 1)
	if err != nil || len(entries) != 1 || entries[0].Message != "last" || nextCursor != int64(len(content)+1) {
		t.Fatalf("completed EOF: entries=%d cursor=%d err=%v", len(entries), nextCursor, err)
	}
}

func TestReadTailEntriesReturnsCursorAtFileEnd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	content := "2025/11/29 12:33:09.000001 [INFO] one\n2025/11/29 12:33:10.000001 [ERROR] two\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, cursor, err := ReadTailEntries(path, 10)
	if err != nil {
		t.Fatalf("读取日志失败：%v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries length = %d，期望 2", len(entries))
	}
	if cursor != int64(len(content)) {
		t.Fatalf("cursor = %d，期望 %d", cursor, len(content))
	}
}

func TestReadEndCursorReturnsFileSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	content := "2025/11/29 12:33:09.000001 [INFO] one\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cursor, err := ReadEndCursor(path)
	if err != nil {
		t.Fatalf("读取 EOF cursor 失败：%v", err)
	}
	if cursor != int64(len(content)) {
		t.Fatalf("cursor = %d，期望 %d", cursor, len(content))
	}
}
