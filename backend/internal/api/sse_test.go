package api

import (
	"net/http/httptest"
	"testing"
)

// Last-Event-ID 必须带 runId：只带 seq 时换到别的 Run 会补错位置。
func TestParseLastEventID(t *testing.T) {
	cases := []struct {
		name   string
		header string
		runID  int64
		want   int64
		ok     bool
	}{
		{"正常", "12:34", 12, 34, true},
		{"runId 不匹配回退到从头补", "12:34", 99, 0, false},
		{"只有 seq", "34", 12, 0, false},
		{"seq 非数字", "12:abc", 12, 0, false},
		{"seq 为 0 合法（从头补）", "12:0", 12, 0, true},
		{"带空格", " 12:34 ", 12, 34, true},
		{"负数 seq", "12:-1", 12, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseLastEventID(c.header, c.runID)
			if ok != c.ok {
				t.Fatalf("ok = %v，期望 %v", ok, c.ok)
			}
			if ok && got != c.want {
				t.Fatalf("seq = %d，期望 %d", got, c.want)
			}
		})
	}
}

// resolveAfterSeq 的三路来源必须都有覆盖：EventSource 首次连接没有 header，
// 只能靠 query；重连时 header 由浏览器自动带。优先级错了会补到错位置或从头刷一遍。
func TestResolveAfterSeq(t *testing.T) {
	cases := []struct {
		name   string
		url    string
		header string
		want   int64
	}{
		{"什么都没带：从头回放", "/api/runs/12/events", "", 0},
		{"header 优先", "/api/runs/12/events?afterSeq=3", "12:34", 34},
		{"query lastEventId", "/api/runs/12/events?lastEventId=12:7", "", 7},
		{"query afterSeq", "/api/runs/12/events?afterSeq=5", "", 5},
		{"afterSeq=0 合法", "/api/runs/12/events?afterSeq=0", "", 0},
		{"header 的 runId 不匹配则回退 query", "/api/runs/12/events?afterSeq=5", "99:34", 5},
		{"query 非法值忽略", "/api/runs/12/events?afterSeq=abc&lastEventId=12:9", "", 9},
		{"runId 不匹配的 lastEventId 也忽略", "/api/runs/12/events?lastEventId=99:9", "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", c.url, nil)
			if c.header != "" {
				r.Header.Set("Last-Event-ID", c.header)
			}
			if got := resolveAfterSeq(r, 12); got != c.want {
				t.Fatalf("afterSeq = %d，期望 %d", got, c.want)
			}
		})
	}
}
