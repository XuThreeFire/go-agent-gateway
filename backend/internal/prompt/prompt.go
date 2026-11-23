// Package prompt 持有当前生效的 system 提示词，支持不重启进程热重载。
//
// 为什么不塞进 config：config 是「启动时读一次」的静态快照，改了必须重启；
// 而重启会把正在跑的 Run 一律打成 interrupted（D-02），改一句话的代价过大。
// 提示词是这套系统里唯一需要在运行中变更的内容，所以单独提出来，
// 用 SIGHUP 换内存里的一个字符串。
package prompt

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"
)

// Store 是当前生效的 system 提示词，并发安全。
//
// 用 atomic.Value 而不是 Mutex：读发生在每个 Run 开头（每次对话至少一次），
// 写只发生在 SIGHUP 时（一天几次）。读多写少，不值得上锁。
type Store struct {
	path string
	cur  atomic.Value // string
}

// New 加载提示词。path 为空表示本次部署不带 system prompt：
// Store 依然可用，只是 Get 恒返回空串，调用方不必到处判 nil。
func New(path string) (*Store, error) {
	s := &Store{path: path}
	if err := s.Reload(); err != nil {
		return nil, err
	}
	return s, nil
}

// Reload 重读文件并原子替换。
//
// 失败时保留旧值：一个写了一半的文件不该让线上从「有人设」掉到「没人设」。
// 调用方拿到 error 应当记日志但继续服务，不要退出进程。
func (s *Store) Reload() error {
	if s.path == "" {
		s.cur.Store("")
		return nil
	}

	raw, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("读 system 提示词 %s: %w", s.path, err)
	}

	text := strings.TrimSpace(string(raw))
	if text == "" {
		return fmt.Errorf("system 提示词 %s 内容为空", s.path)
	}

	s.cur.Store(text)
	return nil
}

// Path 返回来源文件路径，日志与排障用。
func (s *Store) Path() string { return s.path }

// Get 返回当前生效的提示词，未配置或尚未加载成功时为空串。
func (s *Store) Get() string {
	v, _ := s.cur.Load().(string)
	return v
}
