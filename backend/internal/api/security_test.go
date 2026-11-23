package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/XuThreeFire/go-agent-gateway/backend/internal/domain"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/store"
)

type ownershipStore struct {
	Store
	owner string
}

func (s ownershipStore) GetTask(ctx context.Context, id int64) (domain.Task, error) {
	return domain.Task{ID: id, ClientID: s.owner}, nil
}
func (s ownershipStore) GetRun(ctx context.Context, id int64) (domain.Run, error) {
	return domain.Run{ID: id, TaskID: 1}, nil
}
func (s ownershipStore) GetTaskDetail(ctx context.Context, id int64) (store.TaskDetail, error) {
	return store.TaskDetail{Task: domain.Task{ID: id, ClientID: s.owner}}, nil
}
func secureTestServer(owner string) *Server {
	return &Server{store: ownershipStore{owner: owner}, opt: Options{SessionSecret: strings.Repeat("s", 32)}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}
func sessionRequest(s *Server, method, path, cookie string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	}
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, r)
	return w
}
func TestIdentityIgnoresSpoofedClientID(t *testing.T) {
	s := secureTestServer("owner")
	r := httptest.NewRequest("GET", "/api/session?clientId=owner", nil)
	r.Header.Set("X-Client-Id", "owner")
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, r)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("应签发安全会话 Cookie")
	}
	id := sessionID(s.opt.SessionSecret, cookies[0].Value)
	if id == "" || id == "owner" {
		t.Fatal("调用者伪造了身份")
	}
	if sessionID(s.opt.SessionSecret, cookies[0].Value+"x") != "" {
		t.Fatal("篡改签名不得通过")
	}
	if sessionID(strings.Repeat("x", 32), cookies[0].Value) != "" {
		t.Fatal("错误密钥不得通过")
	}
}
func TestAllResourceRoutesRejectOtherOwner(t *testing.T) {
	s := secureTestServer("owner")
	for _, route := range []struct{ method, path string }{{"GET", "/api/tasks/1"}, {"POST", "/api/tasks/1/turns"}, {"GET", "/api/runs/1/events"}, {"GET", "/api/runs/1/events?format=json"}, {"POST", "/api/runs/1/cancel"}, {"POST", "/api/runs/1/retry"}} {
		t.Run(route.method+route.path, func(t *testing.T) {
			if w := sessionRequest(s, route.method, route.path, ""); w.Code != 404 {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}
func TestSignedIdentitySurvivesServerRecreation(t *testing.T) {
	id := strings.Repeat("a", 32)
	s := secureTestServer(id)
	w := sessionRequest(s, "GET", "/api/tasks/1", signedSession(s.opt.SessionSecret, id))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
}
func TestBasicAccessAndCrossSiteProtection(t *testing.T) {
	s := secureTestServer("owner")
	s.opt.AccessPassword = "local-test-access-password"
	w := sessionRequest(s, "GET", "/api/session", "")
	if w.Code != 401 || w.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("缺密码必须拒绝")
	}
	r := httptest.NewRequest("POST", "/api/tasks", strings.NewReader(`{"input":"hi"}`))
	r.SetBasicAuth("gateway", s.opt.AccessPassword)
	r.Header.Set("Origin", "https://another.example.com")
	w = httptest.NewRecorder()
	s.Routes().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("跨站写入必须拒绝")
	}
}
