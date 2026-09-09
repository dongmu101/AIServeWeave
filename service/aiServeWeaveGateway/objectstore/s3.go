package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"
	"github.com/aws/smithy-go/middleware"

	"AIServeWeave/common/runtime"
)

// defaultS3Region is sent on every request when S3Config.Region is empty. S3-
// compatible servers that are not AWS itself (MinIO, Ceph RGW, most NAS
// firmware) generally ignore the region entirely, but the SDK's request
// signing requires some non-empty value to sign with, so this exists purely
// to satisfy that requirement rather than to name a real region.
//
// defaultS3Region 在 S3Config.Region 为空时用于每个请求。不是 AWS 本身的
// S3-compatible 服务端（MinIO、Ceph RGW、大多数 NAS 固件）通常完全忽略 region，
// 但 SDK 的请求签名要求一个非空值参与签名计算，这个常量的存在纯粹是为了满足
// 这个要求，而不是在指代一个真实的区域。
const defaultS3Region = "us-east-1"

// S3Config configures S3. Endpoint overrides the default AWS endpoint so the
// same backend serves AWS S3 and any S3-compatible server (MinIO, Ceph RGW,
// a NAS's S3 gateway); leave it empty to talk to AWS itself. UsePathStyle
// must be true for most non-AWS S3-compatible servers, which do not support
// virtual-hosted-style ("bucket.host") addressing.
//
// S3Config 配置 S3。Endpoint 覆盖默认的 AWS endpoint，使同一个后端既能对接
// AWS S3 也能对接任何 S3-compatible 服务端（MinIO、Ceph RGW、NAS 的 S3
// 网关）；留空则对接 AWS 本身。绝大多数非 AWS 的 S3-compatible 服务端不支持
// virtual-hosted-style（"bucket.host"）寻址，因此 UsePathStyle 通常需要设为
// true。
type S3Config struct {
	Endpoint        string
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	UsePathStyle    bool
	// Prefix is prepended to every key, with a "/" inserted if Prefix is
	// non-empty and does not already end in one — so one bucket can be
	// shared by multiple purposes or environments without key collisions.
	//
	// Prefix 被加在每个 key 前面，如果 Prefix 非空且不以 "/" 结尾会自动插入
	// 一个——这样一个 bucket 可以被多个用途或环境共用而不撞 key。
	Prefix string
}

// S3 stores objects in an S3 or S3-compatible bucket. See S3Config and the
// package doc comment.
//
// S3 把对象存进一个 S3 或 S3-compatible 的 bucket。见 S3Config 与包的文档
// 注释。
type S3 struct {
	client *s3.Client
	bucket string
	prefix string
	// redact scrubs the configured secret out of any error text before it
	// can reach a log or an HTTP response, per AGENTS.md's credential
	// redaction rule.
	//
	// redact 在错误文本抵达日志或 HTTP 响应之前，把配置的密钥从中清除，对应
	// AGENTS.md 的凭据脱敏规则。
	redact func(string) string
}

// NewS3 validates cfg and constructs an S3 backend. It does not make a
// network call: connectivity and credential problems surface on the first
// Put, Open or Delete, the same way a misconfigured Local dir would only
// have been caught earlier because that check is local and free — an S3
// equivalent would mean either blocking startup on network reachability or
// picking an arbitrary timeout for it, neither of which this constructor
// decides on the caller's behalf.
//
// NewS3 校验 cfg 并构造一个 S3 后端。它不发起网络调用：连通性与凭据问题在
// 第一次 Put、Open 或 Delete 时才会暴露——Local 的目录检查之所以能提前做，
// 是因为那个检查是本地且免费的；S3 的等价检查要么会让启动阻塞在网络可达性上，
// 要么要替调用方随意选一个超时时间，这两者都不该由这个构造函数替调用方决定。
func NewS3(cfg S3Config) (*S3, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("objectstore: s3 bucket must not be empty")
	}
	region := cfg.Region
	if region == "" {
		region = defaultS3Region
	}
	opts := s3.Options{
		Region:       region,
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		UsePathStyle: cfg.UsePathStyle,
		// The SDK's default checksum behavior wraps a streaming upload in
		// aws-chunked framing with a trailing checksum, which is an
		// AWS-specific extension that not every S3-compatible NAS or
		// on-prem gateway implements correctly. "WhenRequired" keeps
		// checksums off unless an operation mandates them (PutObject does
		// not), trading AWS's extra integrity check for broader
		// compatibility with the non-AWS servers this backend targets.
		//
		// SDK 默认的校验和行为会把一次流式上传包进 aws-chunked 分帧并附带
		// 结尾校验和，这是一个 AWS 专有扩展，不是每个 S3-compatible 的
		// NAS 或自建网关都能正确实现。"WhenRequired" 让校验和保持关闭，
		// 除非某个操作强制要求（PutObject 不要求），用 AWS 那份额外的
		// 完整性校验换取对本后端目标场景——非 AWS 服务端——更广泛的兼容性。
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
		// PutObject's body here is whatever the caller is streaming through
		// (an artifact pulled from the tunnel, an input upload from an HTTP
		// request) — not a seekable buffer. The SDK's default SigV4 payload
		// signing needs to hash the whole body and then seek back to its
		// start to send it, which a one-pass stream cannot do; swapping in
		// UNSIGNED-PAYLOAD skips that hash instead of forcing this package
		// to buffer the object first. TLS (required for any endpoint
		// carrying real credentials) still protects the payload in transit;
		// what is given up is S3 additionally binding the payload bytes
		// into the request signature, the same trade-off the SDK's own
		// streaming multipart uploader makes.
		//
		// PutObject 这里的 body 是调用方正在流式传输的东西（从隧道拉取的
		// 产物、一次 HTTP 输入上传）——不是一个可寻址的缓冲区。SDK 默认的
		// SigV4 载荷签名需要先哈希整个 body 再把它 seek 回起点发送出去，
		// 而一次性的流做不到这件事；换成 UNSIGNED-PAYLOAD 跳过这次哈希，
		// 而不是逼这个包先把整个对象缓冲下来。TLS（任何携带真实凭据的
		// endpoint 都要求它）仍然在传输过程中保护载荷；放弃的只是 S3
		// 额外把载荷字节也绑进请求签名这一层，这与 SDK 自己的流式分片
		// 上传器所做的取舍相同。
		APIOptions: []func(*middleware.Stack) error{
			v4.SwapComputePayloadSHA256ForUnsignedPayloadMiddleware,
		},
	}
	if cfg.Endpoint != "" {
		opts.BaseEndpoint = aws.String(cfg.Endpoint)
	}
	prefix := cfg.Prefix
	if prefix != "" && prefix[len(prefix)-1] != '/' {
		prefix += "/"
	}
	return &S3{
		client: s3.New(opts),
		bucket: cfg.Bucket,
		prefix: prefix,
		redact: func(s string) string { return runtime.Redact(s, cfg.AccessKeyID, cfg.SecretAccessKey) },
	}, nil
}

func (b *S3) fullKey(key string) string { return b.prefix + key }

// Put uploads body under key. The SDK streams the body as it is read
// (chunked transfer with a trailing checksum) rather than buffering it
// whole, so this holds for large artifacts the same "never buffer a whole
// object" property the package doc comment promises.
//
// Put 把 body 上传到 key 下。SDK 会在读取的同时流式传输 body（分块传输 + 结尾
// 校验和），而不是把它整体缓冲，因此对大产物同样保持包文档注释所承诺的
// 「绝不整体缓冲一个对象」这条性质。
func (b *S3) Put(ctx context.Context, key string, body io.Reader, size int64) error {
	if err := validateKey(key); err != nil {
		return err
	}
	input := &s3.PutObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(b.fullKey(key)),
		Body:   newBoundedReader(body, size),
	}
	if size >= 0 {
		input.ContentLength = aws.Int64(size)
	}
	if _, err := b.client.PutObject(ctx, input); err != nil {
		return fmt.Errorf("objectstore: s3 put %q: %s", key, b.redact(err.Error()))
	}
	return nil
}

// Open returns key's object. See the Backend doc comment.
//
// Open 返回 key 对应的对象。见 Backend 的文档注释。
func (b *S3) Open(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	if err := validateKey(key); err != nil {
		return nil, ObjectInfo{}, err
	}
	out, err := b.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(b.fullKey(key)),
	})
	if err != nil {
		if isS3NotFound(err) {
			return nil, ObjectInfo{}, ErrNotFound
		}
		return nil, ObjectInfo{}, fmt.Errorf("objectstore: s3 get %q: %s", key, b.redact(err.Error()))
	}
	info := ObjectInfo{Size: -1}
	if out.ContentLength != nil {
		info.Size = *out.ContentLength
	}
	if out.ContentType != nil {
		info.ContentType = *out.ContentType
	}
	return out.Body, info, nil
}

// Delete removes key's object. See the Backend doc comment for why a
// missing key is not an error; S3's DeleteObject already returns success in
// that case, so no extra mapping is needed here.
//
// Delete 移除 key 对应的对象。key 本就不存在为何不算错误，见 Backend 的文档
// 注释；S3 的 DeleteObject 对这种情况本就返回成功，因此这里不需要额外映射。
func (b *S3) Delete(ctx context.Context, key string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	if _, err := b.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(b.fullKey(key)),
	}); err != nil {
		return fmt.Errorf("objectstore: s3 delete %q: %s", key, b.redact(err.Error()))
	}
	return nil
}

// isS3NotFound reports whether err is the SDK's representation of a missing
// object. Different S3-compatible servers spell this differently — some
// return the specific NoSuchKey type, others the generic NotFound, others
// only a bare 404 status without a recognizable typed error — so this checks
// the two typed cases and falls back to the HTTP status.
//
// isS3NotFound 报告 err 是否是 SDK 对「对象不存在」的表达。不同的
// S3-compatible 服务端对此的表达方式不同——有的返回具体的 NoSuchKey 类型，
// 有的返回通用的 NotFound，还有的只给一个裸的 404 状态码、没有可识别的类型化
// 错误——因此这里先检查两种类型化情形，再回退到 HTTP 状态码。
func isS3NotFound(err error) bool {
	var noSuchKey *types.NoSuchKey
	var notFound *types.NotFound
	if errors.As(err, &noSuchKey) || errors.As(err, &notFound) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound":
			return true
		}
	}
	return false
}
