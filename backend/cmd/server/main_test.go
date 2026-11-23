package main

import (
	"context"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/prompt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// 三态语义错一个，本地调试就会朝错误方向排查：
// 「以为走了代理其实直连」（Charles 里没记录）或「以为直连其实走了代理」。
func TestParseLLMProxy(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantNil bool   // 期望返回 nil（不接管，按环境变量）
		wantStr string // 期望的 String()，空串表示强制直连
		wantErr bool
	}{
		{"不传：交给环境变量", "", true, "", false},
		{"空串含空格也算不传", "  ", true, "", false},
		{"off：强制直连", "off", false, "", false},
		{"none 同义", "none", false, "", false},
		{"direct 同义", "direct", false, "", false},
		{"Charles 默认", "http://127.0.0.1:8888", false, "http://127.0.0.1:8888", false},
		{"socks5", "socks5://127.0.0.1:1080", false, "socks5://127.0.0.1:1080", false},
		{"带认证信息", "http://user:pass@127.0.0.1:8888", false, "http://user:pass@127.0.0.1:8888", false},
		{"缺 scheme", "127.0.0.1:8888", false, "", true},
		{"缺 host", "http://", false, "", true},
		{"不支持的协议", "ftp://127.0.0.1:8888", false, "", true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseLLMProxy(c.raw)
			if c.wantErr {
				if err == nil {
					t.Fatalf("%q 应当报错，实际得到 %v", c.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("%q 不该报错: %v", c.raw, err)
			}
			if c.wantNil {
				if got != nil {
					t.Fatalf("期望 nil（按环境变量），实际 %v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("期望非 nil，实际 nil")
			}
			if got.String() != c.wantStr {
				t.Fatalf("String() = %q，期望 %q", got.String(), c.wantStr)
			}
		})
	}
}

// 日志文案要区分「显式直连」和「没设代理」，否则排查时会以为代理开着。
func TestProxyLabel(t *testing.T) {
	direct, err := parseLLMProxy("off")
	if err != nil {
		t.Fatal(err)
	}
	if got := proxyLabel(direct); got != "direct（已忽略 HTTPS_PROXY）" {
		t.Fatalf("显式直连的标签 = %q", got)
	}

	u, err := parseLLMProxy("http://127.0.0.1:8888")
	if err != nil {
		t.Fatal(err)
	}
	if got := proxyLabel(u); got != "http://127.0.0.1:8888" {
		t.Fatalf("代理标签 = %q", got)
	}
}

func TestDebugProxyNeverLogsCredentials(t *testing.T) {
	proxy, err := parseLLMProxy("http://example-user:example-pass@127.0.0.1:8888/private?token=hidden")
	if err != nil {
		t.Fatal(err)
	}
	if got := proxyLabel(proxy); got != "http://127.0.0.1:8888" {
		t.Fatalf("代理标签泄露了非连接信息：%s", got)
	}
}
func TestProductionRejectsInsecureTLSWithoutExplicitProxy(t *testing.T) {
	proxy, _ := parseLLMProxy("http://127.0.0.1:8888")
	direct, _ := parseLLMProxy("off")
	for _, tc := range []struct {
		name              string
		proxy             *url.URL
		insecure, invalid bool
	}{{"默认", nil, false, false}, {"显式直连", direct, false, false}, {"抓包代理", proxy, false, true}, {"仅跳过TLS", nil, true, true}, {"直连跳过TLS", direct, true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateLLMDebug("prod", tc.proxy, tc.insecure)
			if (err != nil) != tc.invalid {
				t.Fatalf("错误策略：%v", err)
			}
		})
	}
}

func TestWatchPromptReloadReceivesSIGHUP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "system.md")
	if err := os.WriteFile(path, []byte("第一版"), 0600); err != nil {
		t.Fatal(err)
	}
	sp, err := prompt.New(path)
	if err != nil {
		t.Fatal(err)
	}
	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	watchPromptReload(root, sp, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := os.WriteFile(path, []byte("第二版"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline:
			t.Fatal("SIGHUP 没有触发重载")
		case <-tick.C:
			if sp.Get() == "第二版" {
				return
			}
		}
	}
}
