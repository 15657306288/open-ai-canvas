package videoparser

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/url"
	"os/exec"
)

// ProviderKind 区分供应商来源，便于按合规与成本做优先级。
type ProviderKind string

const (
	ProviderSelfHosted ProviderKind = "self_hosted" // 自建：公开分享页解析、自部署的开源解析器（yt-dlp）
	ProviderOfficial   ProviderKind = "official"    // 平台官方授权接口（需要平台开放平台资质）
	ProviderThirdParty ProviderKind = "third_party" // 合规第三方解析 API（按次计费，单价写入成本统计）
)

// Provider 是 ParserProvider 抽象：一个具体的解析实现。同一平台可以挂多个供应商，按优先级降级。
type Provider interface {
	Name() string
	Kind() ProviderKind
	// Available 返回当前部署能否使用该供应商（例如缺少 yt-dlp 可执行文件）。
	Available() error
	Resolve(ctx context.Context, req Request) (*Result, error)
}

// Request 是交给供应商的解析请求；URL 已经展开短链并通过域名与地址校验。
type Request struct {
	Platform  PlatformAdapter
	URL       *url.URL
	ID        string
	Canonical string
}

// CommandRunner 执行外部解析器；测试用假实现替换，不依赖真实平台。
type CommandRunner interface {
	Run(ctx context.Context, name string, args []string, env []string, stdoutLimit int64) (stdout []byte, stderr []byte, err error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args []string, env []string, stdoutLimit int64) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	var stderr bytes.Buffer
	stdout := &limitedBuffer{limit: stdoutLimit}
	cmd.Stdout, cmd.Stderr = stdout, &limitedBuffer{limit: 64 << 10, buf: &stderr}
	err := cmd.Run()
	if stdout.overflow {
		return nil, stderr.Bytes(), errors.New("parser output too large")
	}
	return stdout.Bytes(), stderr.Bytes(), err
}

// limitedBuffer 只保留前 limit 字节，避免外部进程输出撑爆内存。
type limitedBuffer struct {
	limit    int64
	buf      *bytes.Buffer
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.buf == nil {
		b.buf = &bytes.Buffer{}
	}
	room := b.limit - int64(b.buf.Len())
	if room <= 0 {
		b.overflow = true
		return len(p), nil
	}
	if int64(len(p)) > room {
		b.overflow = true
		b.buf.Write(p[:room])
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) Bytes() []byte {
	if b.buf == nil {
		return nil
	}
	return b.buf.Bytes()
}

var _ io.Writer = (*limitedBuffer)(nil)
