package client

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// startHealthServer 起一个只提供健康检查的 gRPC 服务，返回它的地址。
//
// 用真实服务而不是 mock：这里要验证的正是"连接有没有真的建立起来，
// 以及用的是哪种传输"，把服务端换成假的就什么也证明不了。
func startHealthServer(t *testing.T, opts ...grpc.ServerOption) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	srv := grpc.NewServer(opts...)
	healthpb.RegisterHealthServer(srv, health.NewServer())
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

// selfSignedCert 签发一张只给本测试用的证书，返回它与其信任池。
//
// 用 RootCAs 而不是 InsecureSkipVerify：测试里也走真实的校验路径，
// 否则"证书其实没被校验"这类问题会连同测试一起通过。
func selfSignedCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成私钥失败: %v", err)
	}
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("签发证书失败: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("解析证书失败: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

func checkServing(t *testing.T, c *Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := healthpb.NewHealthClient(c.Conn()).Check(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("健康检查失败: %v", err)
	}
	if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Errorf("健康检查返回 %v，期望 SERVING", resp.GetStatus())
	}
}

// 非回环地址必须走 TLS；显式给了配置就得真的用上。
//
// 地址本身是回环（测试只能监听回环），因此这条用例同时说明了
// Options.TLS 为什么是显式字段而不是从地址推导出来的。
func TestDialUsesTLSWhenConfigured(t *testing.T) {
	cert, pool := selfSignedCert(t)
	addr := startHealthServer(t, grpc.Creds(credentials.NewTLS(
		&tls.Config{Certificates: []tls.Certificate{cert}})))

	c, err := Dial(Options{
		Address: addr,
		TLS:     &tls.Config{RootCAs: pool, ServerName: "localhost"},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Dial 失败: %v", err)
	}
	checkServing(t, c)
	if err := c.Close(); err != nil {
		t.Errorf("关闭连接失败: %v", err)
	}
}

// 回环地址不带 TLS 配置仍然可用：本机开发与 SSH 隧道都依赖这条。
func TestDialAllowsPlaintextToLoopback(t *testing.T) {
	c, err := Dial(Options{Address: startHealthServer(t), Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("Dial 失败: %v", err)
	}
	checkServing(t, c)
	_ = c.Close()
}

// 指向非回环地址却不给 TLS 配置：在建立连接之前就拒绝。
//
// 这条是底线而不是判断——判断哪些地址需要 TLS 由 config.CLIConfig 做；
// 这里只是不让一次漏传把 Bearer 凭证明文发到公网。
func TestDialRejectsPlaintextToNonLoopback(t *testing.T) {
	c, err := Dial(Options{Address: "aladdin.example.test:443", Timeout: time.Second})
	if err == nil {
		_ = c.Close()
		t.Fatal("指向非回环地址却未使用 TLS，Dial 应当报错")
	}
	if !strings.Contains(err.Error(), "TLS") {
		t.Errorf("错误信息应指出缺的是 TLS，得到 %q", err)
	}
}
