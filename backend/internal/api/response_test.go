package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/XuThreeFire/go-agent-gateway/backend/internal/domain"
)

// 错误码是前端的分支条件，不能随文案改：状态码 → 枚举必须一一对上。
func TestErrorCode(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{http.StatusBadRequest, "invalid_argument"},
		{http.StatusNotFound, "not_found"},
		{http.StatusConflict, "conflict"},
		{http.StatusTooManyRequests, "rate_limited"},
		{http.StatusInternalServerError, "internal"},
		{http.StatusServiceUnavailable, "internal"},
	}
	for _, c := range cases {
		if got := errorCode(c.status); got != c.want {
			t.Fatalf("%d → %q，期望 %q", c.status, got, c.want)
		}
	}
}

// 终态帧的 ttftMs 必须与 first_token_at - started_at 一致；
// 没有首 token 时字段整体消失（不是 0），否则前端无法区分「0ms」与「没测到」。
func TestTerminalPayloadTTFT(t *testing.T) {
	start := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	first := start.Add(850 * time.Millisecond)
	ended := start.Add(3 * time.Second)

	withToken := domain.Run{
		Status:       domain.StatusCompleted,
		StartedAt:    &start,
		FirstTokenAt: &first,
		EndedAt:      &ended,
	}
	got := marshalToMap(t, terminalPayloadOf(withToken))
	if got["ttftMs"] != float64(850) {
		t.Fatalf("ttftMs = %v，期望 850", got["ttftMs"])
	}

	// 首字前取消：run 有 started_at、没有 first_token_at。
	noToken := withToken
	noToken.FirstTokenAt = nil
	got = marshalToMap(t, terminalPayloadOf(noToken))
	if _, ok := got["ttftMs"]; ok {
		t.Fatalf("没有首 token 时不该出现 ttftMs，实际 %v", got["ttftMs"])
	}

	// 连 started_at 都没有（重启扫描的极端情况）也不能算出负数或瞎填。
	noStart := withToken
	noStart.StartedAt = nil
	got = marshalToMap(t, terminalPayloadOf(noStart))
	if _, ok := got["ttftMs"]; ok {
		t.Fatalf("没有 started_at 时不该出现 ttftMs，实际 %v", got["ttftMs"])
	}
}

func marshalToMap(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	return out
}

func TestDecodeJSONRejectsTrailingObjectAndOversize(t *testing.T) {
	for _, raw := range []string{`{"input":"ok"}{"input":"again"}`, `{"input":"ok"}` + strings.Repeat(" ", 1<<20)} {
		request := httptest.NewRequest("POST", "/api/tasks", strings.NewReader(raw))
		var body createTaskRequest
		if err := decodeJSON(request, &body); err == nil {
			t.Fatal("非法请求体不得被接受")
		}
	}
}
