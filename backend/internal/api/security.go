package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
)

type identityKey struct{}

const sessionCookie = "gateway_session"

func signedSession(secret, id string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(id))
	return id + "." + hex.EncodeToString(mac.Sum(nil))
}
func sessionID(secret, value string) string {
	parts := strings.Split(value, ".")
	if len(parts) != 2 || len(parts[0]) != 32 {
		return ""
	}
	if _, err := hex.DecodeString(parts[0]); err != nil {
		return ""
	}
	if !hmac.Equal([]byte(signedSession(secret, parts[0])), []byte(value)) {
		return ""
	}
	return parts[0]
}
func (s *Server) withSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		if s.opt.AccessPassword != "" {
			user, pass, ok := r.BasicAuth()
			got, want := sha256.Sum256([]byte(pass)), sha256.Sum256([]byte(s.opt.AccessPassword))
			if !ok || user != "gateway" || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="go-agent-gateway", charset="UTF-8"`)
				writeError(w, http.StatusUnauthorized, "需要访问密码")
				return
			}
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				writeError(w, 403, "不允许跨站请求")
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || u.Host != r.Host {
					writeError(w, 403, "不允许跨站请求")
					return
				}
			}
		}
		id := ""
		if cookie, err := r.Cookie(sessionCookie); err == nil {
			id = sessionID(s.opt.SessionSecret, cookie.Value)
		}
		if id == "" {
			buf := make([]byte, 16)
			if _, err := rand.Read(buf); err != nil {
				writeError(w, 500, "无法创建会话")
				return
			}
			id = hex.EncodeToString(buf)
			http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: signedSession(s.opt.SessionSecret, id), Path: "/", HttpOnly: true, Secure: r.TLS != nil || s.opt.SecureCookies, SameSite: http.SameSiteStrictMode, MaxAge: 365 * 24 * 3600})
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, id)))
	})
}

// owned 在调用处理函数前统一检查 Task/Run 归属。未知与他人资源统一 404。
func (s *Server) owned(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := r.PathValue("taskId")
		runResource := raw == ""
		if runResource {
			raw = r.PathValue("runId")
		}
		id, err := parseID(raw)
		if err != nil {
			writeError(w, 400, "资源 ID 非法")
			return
		}
		taskID := id
		if runResource {
			run, err := s.store.GetRun(r.Context(), id)
			if err != nil {
				s.writeStoreError(w, err)
				return
			}
			taskID = run.TaskID
		}
		task, err := s.store.GetTask(r.Context(), taskID)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		if task.ClientID != clientID(r) {
			writeError(w, 404, "资源不存在")
			return
		}
		next(w, r)
	}
}
