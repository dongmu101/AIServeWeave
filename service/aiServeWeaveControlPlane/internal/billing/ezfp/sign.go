package ezfp

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"sort"
	"strings"
)

// signType is the only sign_type the V2 API uses.
//
// signType 是 V2 接口使用的唯一 sign_type。
const signType = "RSA"

// phpSpace is what PHP's trim() strips, which the reference SDK uses to
// decide that a parameter is empty and left out of the signature.
//
// phpSpace 是 PHP trim() 去除的字符；参考 SDK 用它判断参数为空而不纳入签名。
const phpSpace = " \t\n\r\x00\x0B"

// signContent is the string EZFP signs: every non-empty parameter except
// sign and sign_type, sorted by key byte order, joined as k=v with & and
// with no URL encoding, exactly as the reference SDK's getSignContent.
//
// signContent 是易支付签名的字符串：除 sign 与 sign_type 外的每个非空参数，按键的字节
// 顺序排序，以 & 连接成 k=v，不做 URL 编码，与参考 SDK 的 getSignContent 完全一致。
func signContent(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if k == "sign" || k == "sign_type" || strings.Trim(v, phpSpace) == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(params[k])
	}
	return b.String()
}

// sign signs params with the merchant key: SHA256WithRSA, PKCS #1 v1.5,
// base64.
//
// sign 用商户私钥为 params 签名：SHA256WithRSA、PKCS #1 v1.5、base64。
func sign(key *rsa.PrivateKey, params map[string]string) (string, error) {
	digest := sha256.Sum256([]byte(signContent(params)))
	raw, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", errors.New("ezfp: signing failed")
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// verify checks params' sign against the platform key.
//
// verify 用平台公钥校验 params 的 sign。
func verify(key *rsa.PublicKey, params map[string]string) bool {
	encoded := params["sign"]
	if encoded == "" {
		return false
	}
	if t, ok := params["sign_type"]; ok && t != "" && t != signType {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		if raw, err = base64.RawStdEncoding.DecodeString(encoded); err != nil {
			return false
		}
	}
	digest := sha256.Sum256([]byte(signContent(params)))
	return rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], raw) == nil
}

// keyDER reads a key given as PEM or as the bare base64 the merchant
// dashboard shows; line breaks and spaces in the base64 are ignored.
//
// keyDER 读取以 PEM 或商户后台所示裸 base64 给出的密钥；base64 中的换行与空格被忽略。
func keyDER(text string) ([]byte, error) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "-----BEGIN") {
		block, _ := pem.Decode([]byte(text))
		if block == nil {
			return nil, errors.New("not a PEM block")
		}
		return block.Bytes, nil
	}
	compact := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
			return -1
		}
		return r
	}, text)
	return base64.StdEncoding.DecodeString(compact)
}

// ParsePrivateKey reads the merchant private key: PKCS #8, as the merchant
// dashboard generates it, or PKCS #1.
//
// ParsePrivateKey 读取商户私钥：商户后台生成的 PKCS #8，或 PKCS #1。
func ParsePrivateKey(text string) (*rsa.PrivateKey, error) {
	der, err := keyDER(text)
	if err != nil {
		return nil, errors.New("ezfp: the merchant private key is not valid base64 or PEM")
	}
	if parsed, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		if key, ok := parsed.(*rsa.PrivateKey); ok {
			return key, nil
		}
		return nil, errors.New("ezfp: the merchant private key is not an RSA key")
	}
	key, err := x509.ParsePKCS1PrivateKey(der)
	if err != nil {
		return nil, errors.New("ezfp: the merchant private key is not a PKCS #8 or PKCS #1 RSA key")
	}
	return key, nil
}

// ParsePublicKey reads the platform public key: PKIX, as the merchant
// dashboard shows it, or PKCS #1.
//
// ParsePublicKey 读取平台公钥：商户后台所示的 PKIX，或 PKCS #1。
func ParsePublicKey(text string) (*rsa.PublicKey, error) {
	der, err := keyDER(text)
	if err != nil {
		return nil, errors.New("ezfp: the platform public key is not valid base64 or PEM")
	}
	if parsed, err := x509.ParsePKIXPublicKey(der); err == nil {
		if key, ok := parsed.(*rsa.PublicKey); ok {
			return key, nil
		}
		return nil, errors.New("ezfp: the platform public key is not an RSA key")
	}
	key, err := x509.ParsePKCS1PublicKey(der)
	if err != nil {
		return nil, errors.New("ezfp: the platform public key is not a PKIX or PKCS #1 RSA key")
	}
	return key, nil
}
