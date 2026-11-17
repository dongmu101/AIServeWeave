package pricecatalog

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// DefaultURL is the upstream price list. Mainland China often cannot reach
// GitHub raw; a deployment there points Billing.PriceCatalog.URL at a mirror
// or at the GitHub API address, which Fetch's Accept header makes return the
// file itself.
//
// DefaultURL 是上游价目表。中国大陆常常连不上 GitHub raw；那里的部署可以把
// Billing.PriceCatalog.URL 指向镜像，或指向 GitHub API 地址——Fetch 带的 Accept 头会让
// 它直接返回文件本身。
const DefaultURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

// Fetch limits. The upstream file is about 4 MB.
//
// 拉取的上限。上游文件约 4 MB。
const (
	FetchTimeout  = 30 * time.Second
	MaxFetchBytes = 20 << 20
)

// acceptHeader makes the GitHub contents API return the raw file; other
// hosts ignore it.
//
// acceptHeader 让 GitHub contents API 返回原始文件；其他主机会忽略它。
const acceptHeader = "application/vnd.github.raw"

// userAgent is a generic client name: the request carries nothing about the
// deployment or its operator.
//
// userAgent 是通用的客户端名称：请求不携带任何关于部署或运营者的信息。
const userAgent = "aiserveweave-price-catalog"

// Source yields one copy of the price list.
//
// Source 提供一份价目表。
type Source interface {
	Fetch(ctx context.Context) ([]byte, error)
	// Label names the source for the sync state and audit, e.g. its URL.
	// Label 为同步状态与审计命名该来源，例如它的 URL。
	Label() string
}

// HTTPSource fetches the price list over HTTP(S). A nil Client uses
// http.DefaultClient.
//
// HTTPSource 经 HTTP(S) 拉取价目表。Client 为 nil 时使用 http.DefaultClient。
type HTTPSource struct {
	URL    string
	Client *http.Client
}

// ErrTooLarge is returned for a download over MaxFetchBytes.
//
// ErrTooLarge 用于超过 MaxFetchBytes 的下载。
var ErrTooLarge = errors.New("pricecatalog: the price list is larger than the download limit")

// Label implements Source.
//
// Label 实现 Source。
func (s HTTPSource) Label() string { return s.URL }

// Fetch implements Source: one GET bounded by FetchTimeout and
// MaxFetchBytes, accepting only a 200 answer.
//
// Fetch 实现 Source：一次受 FetchTimeout 与 MaxFetchBytes 限制的 GET，只接受 200 答复。
func (s HTTPSource) Fetch(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("pricecatalog: building the request: %w", err)
	}
	req.Header.Set("Accept", acceptHeader)
	req.Header.Set("User-Agent", userAgent)
	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pricecatalog: fetching the price list: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pricecatalog: fetching the price list: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxFetchBytes+1))
	if err != nil {
		return nil, fmt.Errorf("pricecatalog: reading the price list: %w", err)
	}
	if len(body) > MaxFetchBytes {
		return nil, ErrTooLarge
	}
	return body, nil
}

//go:embed snapshot.json
var embeddedSnapshot []byte

// EmbeddedLabel is the source label of the built-in snapshot.
//
// EmbeddedLabel 是内置快照的来源标签。
const EmbeddedLabel = "embedded"

// Embedded returns the snapshot built into this binary: the upstream file
// trimmed by Trim. scripts/update-price-catalog.sh refreshes it.
//
// Embedded 返回内置于本二进制的快照：经 Trim 裁剪的上游文件。
// scripts/update-price-catalog.sh 负责更新它。
func Embedded() []byte { return embeddedSnapshot }

// BytesSource serves fixed bytes; the built-in snapshot and tests use it.
//
// BytesSource 提供固定的字节；内置快照与测试使用它。
type BytesSource struct {
	Data []byte
	Name string
}

// Fetch implements Source.
//
// Fetch 实现 Source。
func (s BytesSource) Fetch(context.Context) ([]byte, error) { return s.Data, nil }

// Label implements Source.
//
// Label 实现 Source。
func (s BytesSource) Label() string { return s.Name }
