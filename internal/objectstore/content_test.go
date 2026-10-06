package objectstore_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/poetlife/aladdin/internal/objectstore"
)

// 内容对象是**按内容摘要寻址**的字节，两处消费（galaxy 的产物、skill 的纳管），
// 因此它这几条规则只有一处实现，也只有一处测试。

func TestContentDigestShape(t *testing.T) {
	digest := objectstore.ContentDigest([]byte("same"))
	if !objectstore.IsContentDigest(digest) {
		t.Fatalf("算出来的摘要 %q 不被判为合法形状", digest)
	}
	if objectstore.ContentDigest([]byte("a")) == objectstore.ContentDigest([]byte("b")) {
		t.Error("不同的字节得到同一个摘要")
	}
	if objectstore.ContentDigest([]byte("same")) != digest {
		t.Error("同一份字节两次得到不同的摘要")
	}
}

// 声明的摘要会变成对象键，因此形状必须在被使用之前校验：含 `/` 或 `..` 的取值
// 会把"按内容寻址"变成"按调用方给的路径写"。
func TestIsContentDigestRejects(t *testing.T) {
	for _, declared := range []string{
		"",
		"abc",
		strings.Repeat("a", 63),
		strings.Repeat("a", 65),
		strings.ToUpper(objectstore.ContentDigest(nil)),
		"/" + strings.Repeat("a", 63),
		"../" + strings.Repeat("a", 61),
	} {
		if objectstore.IsContentDigest(declared) {
			t.Errorf("摘要 %q 被判为合法形状", declared)
		}
	}
}

func TestPutContentObjectWritesOnce(t *testing.T) {
	ctx := context.Background()
	store := objectstore.NewMemoryStore()
	data := []byte("hello")
	digest := objectstore.ContentDigest(data)
	key := "skills/text/" + digest

	created, err := objectstore.PutContentObject(ctx, store, key, digest, data)
	if err != nil {
		t.Fatalf("首次写入: %v", err)
	}
	if !created {
		t.Error("首次写入报告没有写")
	}

	// 第二次是空操作：摘要是寻址键，写下去的内容必然相同，而重复写会把"仅当
	// 不存在时写入"这条约定变成一句空话。
	created, err = objectstore.PutContentObject(ctx, store, key, digest, data)
	if err != nil {
		t.Fatalf("重复写入: %v", err)
	}
	if created {
		t.Error("重复写入报告写了新对象")
	}

	got, err := store.Read(ctx, key)
	if err != nil {
		t.Fatalf("读回: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("读回 %q，期望 %q", got, data)
	}
}

// 一个算错的摘要必须在**写入之前**被拒：写下去会把一个错误的键指向一份正确的
// 字节，而那个键此后永远对不上。
func TestPutContentObjectRefusesMismatchedDigest(t *testing.T) {
	ctx := context.Background()
	store := objectstore.NewMemoryStore()
	declared := objectstore.ContentDigest([]byte("bbb"))
	key := "skills/text/" + declared

	if _, err := objectstore.PutContentObject(ctx, store, key, declared, []byte("aaa")); !errors.Is(err, objectstore.ErrDigestMismatch) {
		t.Fatalf("摘要不符 err = %v，期望 ErrDigestMismatch", err)
	}
	if _, err := store.Head(ctx, key); !errors.Is(err, objectstore.ErrObjectNotFound) {
		t.Errorf("被拒的写入留下了对象，Head err = %v", err)
	}
}
