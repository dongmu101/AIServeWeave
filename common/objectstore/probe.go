package objectstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
)

// ProbePrefix is where Probe writes its test object.
// ProbePrefix 是 Probe 写入测试对象的位置。
const ProbePrefix = ".aisw-probe"

// Probe checks that b can store, read back and delete an object: it writes 16
// random bytes under ProbePrefix, compares what it reads back, and deletes the
// object again. Its error wraps the failing step's class (errors.go) when the
// backend gave one.
//
// Probe 检查 b 能否存入、读回并删除一个对象：在 ProbePrefix 下写入 16 个随机字节，比对读回
// 的内容，再删除该对象。后端给出了分类时（errors.go），返回的错误包装失败那一步的分类。
func Probe(ctx context.Context, b Backend) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var id [8]byte
	payload := make([]byte, 16)
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	if _, err := rand.Read(payload); err != nil {
		return err
	}
	key := ProbePrefix + "/" + hex.EncodeToString(id[:])
	if err := b.Put(ctx, key, bytes.NewReader(payload), int64(len(payload))); err != nil {
		return err
	}
	// Delete whatever happens next, on a context that outlives a cancelled
	// read, so a failed probe leaves nothing behind when it can help it.
	// 无论之后发生什么都删除，且用不随读取取消而失效的 context，尽量不留下残留。
	defer func() { _ = b.Delete(context.WithoutCancel(ctx), key) }()
	body, _, err := b.Open(ctx, key)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return errors.New("objectstore: the probe object was not readable after writing it")
		}
		return err
	}
	defer body.Close()
	got, err := io.ReadAll(io.LimitReader(body, int64(len(payload))+1))
	if err != nil {
		return err
	}
	if !bytes.Equal(got, payload) {
		return errors.New("objectstore: the probe object read back different bytes")
	}
	if err := b.Delete(ctx, key); err != nil {
		return err
	}
	return nil
}
