package prompt

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("准备临时文件失败: %v", err)
	}
	return path
}

// 路径为空是合法配置：部署里没有 system prompt 就不该报错，只在 Get 时给空串。
func TestNewWithEmptyPath(t *testing.T) {
	s, err := New("")
	if err != nil {
		t.Fatalf("空路径不该失败: %v", err)
	}
	if got := s.Get(); got != "" {
		t.Fatalf("Get() = %q，期望空串", got)
	}
}

func TestNewTrimsContent(t *testing.T) {
	path := writeFile(t, "system.md", "\n\n  你是助手。\n\n")
	s, err := New(path)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if got := s.Get(); got != "你是助手。" {
		t.Fatalf("Get() = %q，期望去掉首尾空白后的正文", got)
	}
}

// 文件不存在必须在启动时就暴露，而不是安静地不带 prompt。
func TestNewFailsOnMissingFile(t *testing.T) {
	if _, err := New(filepath.Join(t.TempDir(), "nope.md")); err == nil {
		t.Fatal("文件不存在应该返回错误")
	}
}

func TestReloadPicksUpNewContent(t *testing.T) {
	path := writeFile(t, "system.md", "第一版")
	s, err := New(path)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if err := os.WriteFile(path, []byte("第二版"), 0o600); err != nil {
		t.Fatalf("覆写失败: %v", err)
	}
	if err := s.Reload(); err != nil {
		t.Fatalf("重载失败: %v", err)
	}
	if got := s.Get(); got != "第二版" {
		t.Fatalf("Get() = %q，期望重载后的内容", got)
	}
}

// 重载失败必须保留旧版本：写坏文件不能让线上从「有人设」掉到「没人设」。
func TestReloadKeepsOldValueOnFailure(t *testing.T) {
	path := writeFile(t, "system.md", "还能用")
	s, err := New(path)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}

	if err := os.WriteFile(path, []byte("   \n\t\n"), 0o600); err != nil {
		t.Fatalf("覆写失败: %v", err)
	}
	if err := s.Reload(); err == nil {
		t.Fatal("空文件应该返回错误")
	}
	if got := s.Get(); got != "还能用" {
		t.Fatalf("Get() = %q，失败时应保留旧内容", got)
	}
}
