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

	entries, _, cursor, _, err := ReadHistoryPage(path, -1, 2, "")
	if err != nil || len(entries) != 2 || cursor != int64(len(content)) {
		t.Fatalf("tail: entries=%d cursor=%d err=%v", len(entries), cursor, err)
	}
	if entries[0].Message != longLine || entries[1].Message != "last" {
		t.Fatal("tail lost long-line content or EOF line")
	}

	entries, cursor, _, err = ReadEntriesFromCursor(path, 0, 1, "")
	if err != nil || len(entries) != 1 || entries[0].Message != "first" || cursor != int64(len(first)) {
		t.Fatalf("first page: entries=%d cursor=%d err=%v", len(entries), cursor, err)
	}
	entries, cursor, _, err = ReadEntriesFromCursor(path, cursor, 1, "")
	if err != nil || len(entries) != 1 || entries[0].Message != longLine || cursor != int64(len(first+second)) || entries[0].Cursor != cursor {
		t.Fatalf("long page: entries=%d cursor=%d err=%v", len(entries), cursor, err)
	}
	entries, nextCursor, _, err := ReadEntriesFromCursor(path, cursor, 1, "")
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
	entries, nextCursor, _, err = ReadEntriesFromCursor(path, cursor, 1, "")
	if err != nil || len(entries) != 1 || entries[0].Message != "last" || nextCursor != int64(len(content)+1) {
		t.Fatalf("completed EOF: entries=%d cursor=%d err=%v", len(entries), nextCursor, err)
	}
}

func TestHistoryPageReturnsFixedReadEnd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	content := "2025/11/29 12:33:09.000001 [INFO] one\n2025/11/29 12:33:10.000001 [ERROR] two\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, _, cursor, _, err := ReadHistoryPage(path, -1, 10, "")
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

func TestTailPageKeepsBlankLinesAndByteBoundaries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blank.log")
	content := "first\r\n\r\nlast\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, start, end, _, err := ReadHistoryPage(path, -1, 2, "")
	if err != nil || start != 7 || end != int64(len(content)) || len(entries) != 2 || entries[0].Message != "" || entries[1].Message != "last" || entries[0].Cursor != 9 {
		t.Fatalf("entries=%+v start=%d end=%d err=%v", entries, start, end, err)
	}
}

func TestHistoryByteBudgetKeepsLongLineAndPreviousPage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.log")
	long := strings.Repeat("中", 800000)
	if err := os.WriteFile(path, []byte("old\n"+long+"\nnew\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, start, _, _, err := ReadHistoryPage(path, -1, 1000, "")
	if err != nil || len(entries) != 2 || entries[0].Message != long || entries[1].Message != "new" || start != 4 {
		t.Fatalf("超长行分页错误: rows=%d start=%d err=%v", len(entries), start, err)
	}
	entries, start, _, _, err = ReadHistoryPage(path, start, 1000, "")
	if err != nil || len(entries) != 1 || entries[0].Message != "old" || start != 0 {
		t.Fatalf("前一页丢失: entries=%+v start=%d err=%v", entries, start, err)
	}
}

func TestFileIdentityRemainsValidAfterAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, cursor, fileID, err := ReadHistoryPage(path, -1, 10, "")
	if err != nil || fileID == "" {
		t.Fatalf("快照未取得文件身份: %q %v", fileID, err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("new\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	entries, next, end, err := ReadEntriesFromCursor(path, cursor, 10, fileID)
	if err != nil || len(entries) != 1 || entries[0].Message != "new" || next != 8 || end != 8 {
		t.Fatalf("追加使旧位置失效: entries=%v next=%d end=%d err=%v", entries, next, end, err)
	}
	_, _, _, currentID, err := ReadHistoryPage(path, -1, 10, fileID)
	if err != nil || currentID != fileID {
		t.Fatalf("普通追加改变了文件身份: %q %q %v", fileID, currentID, err)
	}
}
