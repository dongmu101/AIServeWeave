package arkvideo

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Fetch opens a result URL Ark returned (a video or a last frame) and returns
// its body for the caller to stream and close, with the size the server
// announced (-1 when unknown) and its media type. The URL must be https.
// client should refuse internal addresses (objectstore.GuardedHTTPClient):
// the URL comes from a third party. Nothing here bounds the size; the caller
// streams it.
//
// Fetch 打开方舟返回的结果地址（视频或尾帧），返回其响应体由调用方流式读取并关闭，同时返回
// 服务端声明的大小（未知时为 -1）与媒体类型。地址必须是 https。client 应当拒绝内网地址
// （objectstore.GuardedHTTPClient）：该地址来自第三方。这里不限制大小，由调用方流式读取。
func Fetch(ctx context.Context, client *http.Client, raw string) (io.ReadCloser, int64, string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return nil, 0, "", &Error{Kind: KindInvalid}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, 0, "", &Error{Kind: KindInvalid}
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, 0, "", ctxErr
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, 0, "", err
		}
		return nil, 0, "", &Error{Kind: KindNetwork}
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		// A signed URL that expired answers 403; either way the result is gone.
		// 过期的签名地址返回 403；无论哪种，结果都已不可取。
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
			return nil, 0, "", &Error{Kind: KindNotFound, Status: resp.StatusCode}
		}
		return nil, 0, "", &Error{Kind: KindUpstream, Status: resp.StatusCode}
	}
	ct := resp.Header.Get("Content-Type")
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return resp.Body, resp.ContentLength, strings.TrimSpace(strings.ToLower(ct)), nil
}
