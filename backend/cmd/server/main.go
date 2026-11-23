package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/XuThreeFire/go-agent-gateway/backend/internal/api"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/config"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/exec"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/hub"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/llm"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/store"
	"github.com/XuThreeFire/go-agent-gateway/backend/internal/tool"
)

// version 由发布脚本用 -ldflags "-X main.version=<commit>-<时间>" 注入。
// 线上出问题时第一句话就是「跑的是哪个版本」，从日志能直接读到，不用登机器看文件时间。
var version = "dev"

func main() {
	configPath := flag.String("config", envOr("CONFIG_PATH", "backend/configs/dev/config.yaml"), "配置文件路径")
	check := flag.Bool("check", false, "只检查配置和数据库连接，不启动服务")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "加载配置失败:", err)
		os.Exit(1)
	}

	logger := newLogger(cfg.Log)
	slog.SetDefault(logger)

	dsn, err := cfg.Database.DSN()
	if err != nil {
		logger.Error("数据库配置有误", "err", err)
		os.Exit(1)
	}

	st, err := store.Open(dsn, store.Options{
		MaxOpenConns:    cfg.Database.MaxOpenConns,
		MaxIdleConns:    cfg.Database.MaxIdleConns,
		ConnMaxLifetime: cfg.Database.ConnMaxLifetime.Std(),
		ConnMaxIdleTime: cfg.Database.ConnMaxIdleTime.Std(),
		SlowThreshold:   cfg.Database.SlowThreshold.Std(),
		Debug:           cfg.Database.Debug,
	})
	if err != nil {
		logger.Error("初始化存储失败", "err", err)
		os.Exit(1)
	}
	defer st.Close()
	if *check {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := st.Ping(ctx); err != nil {
			logger.Error("数据库不可达")
			os.Exit(1)
		}
		fmt.Println("配置与数据库连接正常；尚未验证真实模型可用性")
		return
	}
	secret, err := cfg.SessionSecret()
	if err != nil {
		logger.Error("初始化会话密钥失败")
		os.Exit(1)
	}

	// root 是全局生命周期：HTTP 请求结束后执行器还要继续跑（D-01），
	// 所以执行器拿的是它，不是 request context。
	root, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	eventHub := hub.New(st, hub.Options{
		BatchSize:       cfg.Hub.BatchSize,
		FlushInterval:   cfg.Hub.FlushInterval.Std(),
		SubscriberBuf:   cfg.Hub.SubscriberBuf,
		HistoryPageSize: cfg.Hub.HistoryPageSize,
	}, logger)
	hubCtx, stopHub := context.WithCancel(context.Background())
	hubDone := make(chan struct{})
	go func() { defer close(hubDone); eventHub.Run(hubCtx) }()

	models := llm.NewRegistry(buildTargets(cfg, logger), llm.ClientOptions{
		RequestTimeout:    cfg.LLM.RequestTimeout.Std(),
		FirstTokenTimeout: cfg.LLM.FirstTokenTimeout.Std(),
		MaxRetries:        cfg.LLM.MaxRetries,
	})

	// 工具在编译期注册，配置只能按名字关掉。写错名字不会让模型拿到新能力，
	// 只会让工具变少——所以这里要显式告警，否则「为什么模型不调工具」很难查。
	tools := tool.New(tool.Options{
		Enabled:        cfg.Tools.Enabled,
		MaxResultBytes: cfg.Tools.MaxResultBytes,
	}, tool.Builtins()...)
	if unknown := tools.Unknown(cfg.Tools.Enabled); len(unknown) > 0 {
		logger.Warn("配置里的工具名不存在，已忽略", "unknown", strings.Join(unknown, ","))
	}
	if tools.Len() > 0 {
		logger.Info("已注册工具", "tools", strings.Join(tools.Names(), ","))
	}

	executor := exec.New(root, st, eventHub, models, exec.Options{
		SystemPrompt:         cfg.Executor.SystemPrompt,
		MaxOutputTokens:      cfg.Executor.MaxOutputTokens,
		MaxContextBytes:      cfg.Executor.MaxContextBytes,
		MaxRunTokens:         cfg.Executor.MaxRunTokens,
		MaxConcurrentRuns:    cfg.Executor.MaxConcurrentRuns,
		MaxSteps:             cfg.Executor.MaxSteps,
		StepTimeout:          cfg.Executor.StepTimeout.Std(),
		ToolTimeout:          cfg.Tools.Timeout.Std(),
		DailyTokensPerClient: cfg.Quota.DailyTokensPerClient,
		Tools:                tools,
	}, logger)

	// 启动扫描必须在开始接请求之前：否则客户端会先看到一条永远不动的 running，
	// 而这条 Run 也不会有人再执行它。扫完把残留置 interrupted 并补 run.terminal，
	// 客户端重连时能读到「中断了」，再由用户决定要不要重试。
	recoverCtx, cancelRecover := context.WithTimeout(context.Background(), 15*time.Second)
	interrupted, err := executor.Recover(recoverCtx)
	cancelRecover()
	if err != nil {
		logger.Error("启动扫描失败，拒绝接收请求")
		os.Exit(1)
	} else if interrupted > 0 {
		logger.Warn("启动扫描：残留执行已置为 interrupted", "count", interrupted)
	}

	server := api.New(st, eventHub, models, executor, api.Options{
		SessionSecret:  secret,
		AccessPassword: os.Getenv(cfg.Security.AccessPasswordEnv),
		SecureCookies:  cfg.Env == "prod",
		DefaultModel:   cfg.DefaultModel(),
		Models:         cfg.PublicModels(),
		Tools:          tools.Specs(),
		PingInterval:   cfg.SSE.PingInterval.Std(),
	}, logger)

	httpServer := &http.Server{
		Addr:              cfg.HTTP.Addr,
		Handler:           server.Routes(),
		ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout.Std(),
		// WriteTimeout 必须为 0：SSE 是长连接，任何写超时都会把流掐断。
		WriteTimeout: 0,
		IdleTimeout:  cfg.HTTP.IdleTimeout.Std(),
	}

	logger.Info("gateway 启动",
		"env", cfg.Env,
		"version", version,
		"addr", cfg.HTTP.Addr,
		"models", strings.Join(modelNames(cfg), ","),
		"defaultModel", cfg.DefaultModel(),
		"maxConcurrentRuns", cfg.Executor.MaxConcurrentRuns,
	)

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP 服务异常退出", "err", err)
			stop()
		}
	}()

	<-root.Done()
	logger.Info("收到退出信号，开始优雅关闭")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout.Std())
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("HTTP 关闭不完整", "err", err)
	}
	// 执行器在 root 取消后会把状态收尾为 aborted，hub 退出前也会补一次落库。
	if err := executor.Shutdown(shutdownCtx); err != nil {
		logger.Error("执行器未在期限内退出")
	}
	stopHub()
	select {
	case <-hubDone:
	case <-shutdownCtx.Done():
		logger.Error("事件刷新超时")
	}
	logger.Info("已退出")
}

func buildTargets(cfg config.Config, logger *slog.Logger) []llm.Target {
	out := make([]llm.Target, 0, len(cfg.LLM.Models))
	for _, m := range cfg.LLM.Models {
		if !m.Enabled {
			continue
		}
		key := m.ResolveKey()
		if key == "" && m.Provider != "mock" {
			// 不阻止启动：/api/models 与补发链路仍可演示，真实调用会返回 401 并落成 protocol 错误。
			logger.Warn("模型未配置密钥，调用将失败", "model", m.Name, "envHint", m.APIKeyEnv)
		}
		out = append(out, llm.Target{
			Name:             m.Name,
			Provider:         m.Provider,
			BaseURL:          m.BaseURL,
			APIKey:           key,
			KeyEnv:           m.APIKeyEnv,
			UpstreamModel:    m.UpstreamModel,
			SupportsToolCall: m.SupportsToolCall,
			ContextWindow:    m.ContextWindow,
		})
	}
	return out
}

func modelNames(cfg config.Config) []string {
	names := make([]string, 0, len(cfg.LLM.Models))
	for _, m := range cfg.LLM.Models {
		if m.Enabled {
			names = append(names, m.Name)
		}
	}
	return names
}

func newLogger(cfg config.LogConfig) *slog.Logger {
	var level slog.Level
	switch strings.ToLower(cfg.Level) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opt := &slog.HandlerOptions{Level: level}
	if strings.ToLower(cfg.Format) == "text" {
		return slog.New(slog.NewTextHandler(os.Stdout, opt))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opt))
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
