// CLI 是独立 HTTP/SSE 客户端，不 import 后端包。
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type gateway struct {
	base, password, session, path string
	http                          *http.Client
}

func main() {
	base := flag.String("base", envOr("GATEWAY_BASE", "http://127.0.0.1:8080"), "服务地址")
	model := flag.String("model", "", "模型别名，留空用默认值")
	file := flag.String("session-file", "data/cli-session", "本机身份文件，请勿分享或提交")
	after := flag.Int64("after", 0, "tail 从此事件序号之后补发")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "用法：cli [选项] health|models|list|new <输入>|continue <taskId> <输入>|tail <runId>|cancel <runId>|retry <runId>\n访问密码使用 GATEWAY_ACCESS_PASSWORD；选项必须放在命令前。")
	}
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	raw, _ := os.ReadFile(*file)
	g := &gateway{base: strings.TrimRight(*base, "/"), password: os.Getenv("GATEWAY_ACCESS_PASSWORD"), session: strings.TrimSpace(string(raw)), path: *file, http: &http.Client{}}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := g.run(ctx, flag.Args(), *model, *after, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "错误：", err)
		os.Exit(1)
	}
}
func (g *gateway) run(ctx context.Context, args []string, model string, after int64, out io.Writer) error {
	if args[0] == "health" {
		var body any
		if err := g.json(ctx, "GET", "/healthz", nil, &body); err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(body)
	}
	if err := g.json(ctx, "GET", "/api/session", nil, nil); err != nil {
		return err
	}
	switch args[0] {
	case "models", "list":
		path := "/api/models"
		if args[0] == "list" {
			path = "/api/tasks"
		}
		var body any
		if err := g.json(ctx, "GET", path, nil, &body); err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(body)
	case "new", "continue":
		path, input := "/api/tasks", ""
		if args[0] == "new" {
			if len(args) < 2 {
				return errors.New("缺少输入")
			}
			input = strings.Join(args[1:], " ")
		} else {
			if len(args) < 3 {
				return errors.New("需要 taskId 和输入")
			}
			if !validID(args[1]) {
				return errors.New("ID 必须是正整数")
			}
			path = "/api/tasks/" + args[1] + "/turns"
			input = strings.Join(args[2:], " ")
		}
		var body struct {
			TaskID int64 `json:"taskId"`
			RunID  int64 `json:"runId"`
		}
		if err := g.json(ctx, "POST", path, map[string]string{"input": input, "model": model}, &body); err != nil {
			return err
		}
		fmt.Fprintf(out, "taskId=%d runId=%d\n", body.TaskID, body.RunID)
		return g.tail(ctx, strconv.FormatInt(body.RunID, 10), 0, out)
	case "tail", "cancel", "retry":
		if len(args) != 2 || !validID(args[1]) {
			return errors.New("需要一个正整数 runId")
		}
		if args[0] == "tail" {
			return g.tail(ctx, args[1], after, out)
		}
		var body struct {
			RunID  int64  `json:"runId"`
			Status string `json:"status"`
		}
		if err := g.json(ctx, "POST", "/api/runs/"+args[1]+"/"+args[0], nil, &body); err != nil {
			return err
		}
		if args[0] == "retry" {
			return g.tail(ctx, strconv.FormatInt(body.RunID, 10), 0, out)
		}
		fmt.Fprintln(out, body.Status)
		return nil
	default:
		return errors.New("未知命令")
	}
}
func validID(id string) bool { n, err := strconv.ParseInt(id, 10, 64); return err == nil && n > 0 }
func (g *gateway) request(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, g.base+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if g.session != "" {
		req.AddCookie(&http.Cookie{Name: "gateway_session", Value: g.session})
	}
	if g.password != "" {
		req.SetBasicAuth("gateway", g.password)
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return nil, errors.New("无法连接网关，请检查地址和服务状态")
	}
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "gateway_session" {
			g.session = cookie.Value
			if g.path != "" {
				if err := os.MkdirAll(filepath.Dir(g.path), 0700); err != nil {
					resp.Body.Close()
					return nil, err
				}
				if err := os.WriteFile(g.path, []byte(g.session), 0600); err != nil {
					resp.Body.Close()
					return nil, err
				}
				if err := os.Chmod(g.path, 0600); err != nil {
					resp.Body.Close()
					return nil, err
				}
			}
		}
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d：请求失败（401 请设置访问密码；404 请检查 ID 与本机身份）", resp.StatusCode)
	}
	return resp, nil
}
func (g *gateway) json(ctx context.Context, method, path string, in, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	resp, err := g.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return err
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}

// readEvents 按空行组帧，支持多 data 行；lastEventId 只在完整帧处理后推进。
func readEvents(r io.Reader, runID string, after *int64, out io.Writer) (bool, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	event, id := "", ""
	var data []string
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if len(data) > 0 {
				parts := strings.SplitN(id, ":", 2)
				seq := int64(0)
				if len(parts) == 2 && parts[0] == runID {
					seq, _ = strconv.ParseInt(parts[1], 10, 64)
				}
				if seq > *after {
					var payload struct {
						Delta  string `json:"delta"`
						Status string `json:"status"`
					}
					if err := json.Unmarshal([]byte(strings.Join(data, "\n")), &payload); err != nil {
						return false, err
					}
					if event == "turn.delta" {
						fmt.Fprint(out, payload.Delta)
					}
					if event == "tool.result" {
						fmt.Fprintln(out, "\n[工具执行完成]")
					}
					*after = seq
					if event == "run.terminal" {
						fmt.Fprintf(out, "\n[%s]\n", payload.Status)
						return true, nil
					}
				}
			}
			event, id = "", ""
			data = nil
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			event = value
		case "id":
			id = value
		case "data":
			data = append(data, value)
		}
	}
	return false, scanner.Err()
}
func (g *gateway) tail(ctx context.Context, id string, after int64, out io.Writer) error {
	if after < 0 {
		return errors.New("after 不能为负数")
	}
	for attempt := 0; attempt < 6; attempt++ {
		path := fmt.Sprintf("/api/runs/%s/events?afterSeq=%d", id, after)
		resp, err := g.request(ctx, "GET", path, nil)
		if err != nil {
			return err
		}
		terminal, err := readEvents(resp.Body, id, &after, out)
		resp.Body.Close()
		if terminal {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			fmt.Fprintln(out, "\n[连接中断，尝试补发]")
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return fmt.Errorf("重连次数用尽；可使用 -after %d tail %s 继续", after, id)
}
func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
