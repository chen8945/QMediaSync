package helpers

import (
	"os"
	"testing"
)

const (
	testFilePath = "C:\\Users\\qicfa\\Videos\\NarakaBladepoint\\Record\\Naraka-record-20251228-18-13-54.mp4"
)

func TestCalculateFilePartialMD5(t *testing.T) {
	if !PathExists(testFilePath) {
		t.Skipf("测试文件不存在: %s", testFilePath)
	}

	md5, err := CalculateFilePartialMD5(testFilePath)
	if err != nil {
		t.Fatalf("计算文件部分 MD5 失败: %v", err)
	}

	if md5 == "" {
		t.Error("MD5 值为空")
	}

	t.Logf("文件前256KB MD5: %s", md5)
}

func TestCalculateFileChunkMD5_DefaultChunkSize(t *testing.T) {
	if !PathExists(testFilePath) {
		t.Skipf("测试文件不存在: %s", testFilePath)
	}

	result, err := CalculateFileChunkMD5(testFilePath, 0)
	if err != nil {
		t.Fatalf("计算文件分片 MD5 失败: %v", err)
	}

	if result.FileMD5 == "" {
		t.Error("文件 MD5 为空")
	}

	if len(result.ChunkMD5s) == 0 {
		t.Error("分片 MD5 数组为空")
	}

	if result.ChunkSize <= 0 {
		t.Error("分片大小无效")
	}

	if result.ChunkCount <= 0 {
		t.Error("分片数量无效")
	}

	if result.ChunkCount != len(result.ChunkMD5s) {
		t.Errorf("分片数量不匹配: ChunkCount=%d, len(ChunkMD5s)=%d", result.ChunkCount, len(result.ChunkMD5s))
	}

	t.Logf("文件 MD5: %s", result.FileMD5)
	t.Logf("分片大小: %d bytes (%.2f MB)", result.ChunkSize, float64(result.ChunkSize)/1024/1024)
	t.Logf("分片数量: %d", result.ChunkCount)
	t.Logf("分片 MD5 数量: %d", len(result.ChunkMD5s))
}

func TestCalculateFileChunkMD5_CustomChunkSize(t *testing.T) {
	if !PathExists(testFilePath) {
		t.Skipf("测试文件不存在: %s", testFilePath)
	}

	customChunkSize := int64(8 * 1024 * 1024)
	result, err := CalculateFileChunkMD5(testFilePath, customChunkSize)
	if err != nil {
		t.Fatalf("计算文件分片 MD5 失败: %v", err)
	}

	if result.FileMD5 == "" {
		t.Error("文件 MD5 为空")
	}

	if len(result.ChunkMD5s) == 0 {
		t.Error("分片 MD5 数组为空")
	}

	if result.ChunkCount != len(result.ChunkMD5s) {
		t.Errorf("分片数量不匹配: ChunkCount=%d, len(ChunkMD5s)=%d", result.ChunkCount, len(result.ChunkMD5s))
	}

	t.Logf("指定分片大小: %d bytes (%.2f MB)", customChunkSize, float64(customChunkSize)/1024/1024)
	t.Logf("实际分片大小: %d bytes (%.2f MB)", result.ChunkSize, float64(result.ChunkSize)/1024/1024)
	t.Logf("文件 MD5: %s", result.FileMD5)
	t.Logf("分片数量: %d", result.ChunkCount)
}

func TestCalculateFileChunkMD5_SmallFile(t *testing.T) {
	smallFilePath := "F:\\test\\百度\\Media\\电影\\动画电影\\疯狂动物城 (2016)\\poster.jpg"
	if !PathExists(smallFilePath) {
		t.Skipf("小文件不存在: %s", smallFilePath)
	}

	result, err := CalculateFileChunkMD5(smallFilePath, 0)
	if err != nil {
		t.Fatalf("计算文件分片 MD5 失败: %v", err)
	}

	if result.ChunkCount != 1 {
		t.Errorf("小文件应该只有 1 个分片，实际为: %d", result.ChunkCount)
	}

	if len(result.ChunkMD5s) != 1 {
		t.Errorf("小文件应该只有 1 个分片 MD5，实际为: %d", len(result.ChunkMD5s))
	}

	if result.FileMD5 != result.ChunkMD5s[0] {
		t.Error("小文件的文件 MD5 应该等于分片 MD5")
	}

	t.Logf("小文件 MD5: %s", result.FileMD5)
	t.Logf("文件大小: %d bytes", result.ChunkSize)
}

func TestCalculateFileChunkMD5_LargeFile(t *testing.T) {
	largeFilePath := "D:\\path\\to\\large\\file.mkv"
	if !PathExists(largeFilePath) {
		t.Skipf("大文件不存在: %s", largeFilePath)
	}

	fileInfo, err := os.Stat(largeFilePath)
	if err != nil {
		t.Fatalf("获取文件信息失败: %v", err)
	}

	t.Logf("大文件大小: %.2f MB", float64(fileInfo.Size())/1024/1024)

	result, err := CalculateFileChunkMD5(largeFilePath, 0)
	if err != nil {
		t.Fatalf("计算文件分片 MD5 失败: %v", err)
	}

	if result.ChunkCount > MaxChunkCount {
		t.Errorf("分片数量超过最大限制: %d > %d", result.ChunkCount, MaxChunkCount)
	}

	t.Logf("文件 MD5: %s", result.FileMD5)
	t.Logf("分片大小: %.2f MB", float64(result.ChunkSize)/1024/1024)
	t.Logf("分片数量: %d (最大限制: %d)", result.ChunkCount, MaxChunkCount)
}

func TestCalculateFilePartialMD5_SmallFile(t *testing.T) {
	smallFilePath := "D:\\path\\to\\small\\file.txt"
	if !PathExists(smallFilePath) {
		t.Skipf("小文件不存在: %s", smallFilePath)
	}

	fileInfo, err := os.Stat(smallFilePath)
	if err != nil {
		t.Fatalf("获取文件信息失败: %v", err)
	}

	if fileInfo.Size() <= PartialMD5Size {
		t.Logf("文件大小: %d bytes (小于等于256KB)", fileInfo.Size())
	}

	md5, err := CalculateFilePartialMD5(smallFilePath)
	if err != nil {
		t.Fatalf("计算文件部分 MD5 失败: %v", err)
	}

	if md5 == "" {
		t.Error("MD5 值为空")
	}

	t.Logf("小文件 MD5: %s", md5)
}

func TestCalculateFileChunkMD5_Consistency(t *testing.T) {
	if !PathExists(testFilePath) {
		t.Skipf("测试文件不存在: %s", testFilePath)
	}

	result1, err := CalculateFileChunkMD5(testFilePath, 0)
	if err != nil {
		t.Fatalf("第一次计算失败: %v", err)
	}

	result2, err := CalculateFileChunkMD5(testFilePath, 0)
	if err != nil {
		t.Fatalf("第二次计算失败: %v", err)
	}

	if result1.FileMD5 != result2.FileMD5 {
		t.Errorf("文件 MD5 不一致: %s != %s", result1.FileMD5, result2.FileMD5)
	}

	if len(result1.ChunkMD5s) != len(result2.ChunkMD5s) {
		t.Errorf("分片数量不一致: %d != %d", len(result1.ChunkMD5s), len(result2.ChunkMD5s))
	}

	for i := 0; i < len(result1.ChunkMD5s); i++ {
		if result1.ChunkMD5s[i] != result2.ChunkMD5s[i] {
			t.Errorf("分片 %d 的 MD5 不一致: %s != %s", i, result1.ChunkMD5s[i], result2.ChunkMD5s[i])
		}
	}

	t.Logf("一致性测试通过，文件 MD5: %s", result1.FileMD5)
}

func TestExtractFileChunkToTemp(t *testing.T) {
	if !PathExists(testFilePath) {
		t.Skipf("测试文件不存在: %s", testFilePath)
	}

	chunkSize := int64(4 * 1024 * 1024)
	chunkIndex := 0

	tempFile, err := ExtractFileChunkToTemp(testFilePath, chunkSize, chunkIndex)
	if err != nil {
		t.Fatalf("提取文件分片到临时文件失败: %v", err)
	}
	defer os.Remove(tempFile)

	t.Logf("临时文件路径: %s", tempFile)

	if !PathExists(tempFile) {
		t.Error("临时文件不存在")
	}

	fileInfo, err := os.Stat(tempFile)
	if err != nil {
		t.Fatalf("获取临时文件信息失败: %v", err)
	}

	t.Logf("分片大小: %d bytes (%.2f MB)", fileInfo.Size(), float64(fileInfo.Size())/1024/1024)
}

func TestExtractFileChunkToTemp_LastChunk(t *testing.T) {
	if !PathExists(testFilePath) {
		t.Skipf("测试文件不存在: %s", testFilePath)
	}

	result, err := CalculateFileChunkMD5(testFilePath, 0)
	if err != nil {
		t.Fatalf("获取文件分片信息失败: %v", err)
	}

	if result.ChunkCount < 2 {
		t.Skipf("文件分片数量不足，需要至少 2 个分片，当前: %d", result.ChunkCount)
	}

	chunkIndex := result.ChunkCount - 1
	tempFile, err := ExtractFileChunkToTemp(testFilePath, result.ChunkSize, chunkIndex)
	if err != nil {
		t.Fatalf("提取最后一个分片到临时文件失败: %v", err)
	}
	defer os.Remove(tempFile)

	t.Logf("临时文件路径: %s", tempFile)
	t.Logf("最后一个分片序号: %d", chunkIndex)

	fileInfo, err := os.Stat(tempFile)
	if err != nil {
		t.Fatalf("获取临时文件信息失败: %v", err)
	}

	t.Logf("最后一个分片大小: %d bytes (%.2f MB)", fileInfo.Size(), float64(fileInfo.Size())/1024/1024)
}

func TestExtractFileChunkToTemp_InvalidIndex(t *testing.T) {
	if !PathExists(testFilePath) {
		t.Skipf("测试文件不存在: %s", testFilePath)
	}

	testCases := []struct {
		name       string
		chunkIndex int
	}{
		{"负数分片序号", -1},
		{"超出范围的分片序号", 999999},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ExtractFileChunkToTemp(testFilePath, 4*1024*1024, tc.chunkIndex)
			if err == nil {
				t.Error("期望返回错误，但返回成功")
			}
			t.Logf("预期错误: %v", err)
		})
	}
}

func TestExtractFileChunkToTemp_MultipleChunks(t *testing.T) {
	if !PathExists(testFilePath) {
		t.Skipf("测试文件不存在: %s", testFilePath)
	}

	result, err := CalculateFileChunkMD5(testFilePath, 0)
	if err != nil {
		t.Fatalf("获取文件分片信息失败: %v", err)
	}

	maxChunks := min(result.ChunkCount, 5)
	tempFiles := make([]string, 0, maxChunks)

	defer func() {
		for _, tempFile := range tempFiles {
			os.Remove(tempFile)
		}
	}()

	for i := 0; i < maxChunks; i++ {
		tempFile, err := ExtractFileChunkToTemp(testFilePath, result.ChunkSize, i)
		if err != nil {
			t.Fatalf("提取分片%d到临时文件失败: %v", i, err)
		}

		tempFiles = append(tempFiles, tempFile)
		fileInfo, _ := os.Stat(tempFile)
		t.Logf("分片%d: %s, 大小: %.2f MB", i, tempFile, float64(fileInfo.Size())/1024/1024)
	}
}

func TestSanitizePathSegments(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected string
	}{
		{name: "正常多层级", path: "某演员/ABC-123", expected: "某演员/ABC-123"},
		{name: "首层级为空", path: "/ABC-123", expected: "ABC-123"},
		{name: "末层级为空", path: "某演员/", expected: "某演员"},
		{name: "层级只有空白", path: "某演员/   /ABC-123", expected: "某演员/ABC-123"},
		{name: "层级为当前目录", path: "./ABC-123", expected: "ABC-123"},
		{name: "层级为上级目录", path: "某演员/../ABC-123", expected: "某演员/ABC-123"},
		{name: "全部为上级目录", path: "../..", expected: ""},
		{name: "反斜杠上级目录", path: `..\..\outside`, expected: "outside"},
		{name: "反斜杠多层级", path: `某演员\ABC-123`, expected: "某演员/ABC-123"},
		{name: "混合分隔符穿越", path: `../某演员\..\ABC-123`, expected: "某演员/ABC-123"},
		{name: "绝对路径", path: "/etc/passwd", expected: "etc/passwd"},
		{name: "去掉首尾空白", path: "  某演员 / ABC-123  ", expected: "某演员/ABC-123"},
		{name: "全部为空", path: "/", expected: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if result := SanitizePathSegments(tt.path); result != tt.expected {
				t.Errorf("清理 '%s' 失败\n期望: %s\n实际: %s", tt.path, tt.expected, result)
			}
		})
	}
}

func TestEnsureWithinDir(t *testing.T) {
	tests := []struct {
		name        string
		baseDir     string
		fullPath    string
		expectError bool
	}{
		{name: "子目录", baseDir: "/dest", fullPath: "/dest/影片 (2024)", expectError: false},
		{name: "多级子目录", baseDir: "/dest", fullPath: "/dest/华语电影/影片 (2024)", expectError: false},
		{name: "根目录本身", baseDir: "/dest", fullPath: "/dest", expectError: false},
		{name: "根目录末尾带分隔符", baseDir: "/dest/", fullPath: "/dest/影片", expectError: false},
		{name: "跨出根目录", baseDir: "/dest", fullPath: "/dest/../outside", expectError: true},
		{name: "完全不同的根目录", baseDir: "/dest", fullPath: "/outside/影片", expectError: true},
		{name: "同前缀的兄弟目录", baseDir: "/dest", fullPath: "/destination/影片", expectError: true},
		{name: "根目录为空时跳过校验", baseDir: "", fullPath: "/outside/影片", expectError: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := EnsureWithinDir(tt.baseDir, tt.fullPath)
			if tt.expectError && err == nil {
				t.Errorf("期望校验失败，实际通过：base=%s path=%s", tt.baseDir, tt.fullPath)
			}
			if !tt.expectError && err != nil {
				t.Errorf("期望校验通过，实际失败：base=%s path=%s，错误：%v", tt.baseDir, tt.fullPath, err)
			}
		})
	}
}
