package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

// transportOf 是测试专用的小工具：抓包代理这类能力必须能被断言，
// 否则「参数传了但没进 Transport」这种错误只能靠 Charles 里有没有记录来发现。
func transportOf(t *testing.T, opt ClientOptions) *http.Transport {
	t.Helper()
	c := New(Target{Name: "t", BaseURL: "https://api.deepseek.com", APIKey: "k"}, opt)
	oc, ok := c.(*openAICompat)
	if !ok {
		t.Fatalf("New 返回类型不是 *openAICompat")
	}
	tr, ok := oc.http.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport 不是 *http.Transport")
	}
	return tr
}

func TestProxyWiring(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://api.deepseek.com/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("显式代理", func(t *testing.T) {
		u, _ := url.Parse("http://127.0.0.1:8888")
		tr := transportOf(t, ClientOptions{Proxy: u})
		got, err := tr.Proxy(req)
		if err != nil {
			t.Fatalf("Proxy 返回错误: %v", err)
		}
		if got == nil || got.String() != "http://127.0.0.1:8888" {
			t.Fatalf("代理 = %v，期望 http://127.0.0.1:8888", got)
		}
		// 走代理时并发复用同一条隧道没意义，顺手把空闲连接上限收紧
		if tr.MaxIdleConnsPerHost != 2 {
			t.Fatalf("走代理时 MaxIdleConnsPerHost = %d，期望 2", tr.MaxIdleConnsPerHost)
		}
	})

	t.Run("空 URL 表示强制直连", func(t *testing.T) {
		tr := transportOf(t, ClientOptions{Proxy: &url.URL{}})
		got, err := tr.Proxy(req)
		if err != nil {
			t.Fatalf("Proxy 返回错误: %v", err)
		}
		if got != nil {
			t.Fatalf("期望直连（nil），实际 %v", got)
		}
	})

	t.Run("默认保留 8 条空闲连接", func(t *testing.T) {
		tr := transportOf(t, ClientOptions{})
		if tr.MaxIdleConnsPerHost != 8 {
			t.Fatalf("不传代理时 MaxIdleConnsPerHost = %d，期望 8", tr.MaxIdleConnsPerHost)
		}
		if tr.TLSClientConfig != nil {
			t.Fatalf("默认不应设置 TLSClientConfig")
		}
	})

	t.Run("跳过 TLS 校验", func(t *testing.T) {
		tr := transportOf(t, ClientOptions{InsecureSkipTLS: true})
		if tr.TLSClientConfig == nil || !tr.TLSClientConfig.InsecureSkipVerify {
			t.Fatalf("InsecureSkipTLS 没有写进 TLSClientConfig")
		}
	})
}

func TestExplicitProxyReceivesModelRequest(t *testing.T) {
	var calls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Host != "model.example.com" {
			t.Errorf("未通过代理转发上游地址：%s", r.URL.Host)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer proxy.Close()
	proxyURL, _ := url.Parse(proxy.URL)
	client := New(Target{BaseURL: "http://model.example.com/v1", APIKey: "fixture-key", UpstreamModel: "fixture-model"}, ClientOptions{Proxy: proxyURL})
	result, err := client.Stream(context.Background(), Request{}, func(Chunk) error { return nil })
	if err != nil || result.Content != "ok" || calls.Load() != 1 {
		t.Fatalf("代理请求失败：%+v calls=%d err=%v", result, calls.Load(), err)
	}
}
