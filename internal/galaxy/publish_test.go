package galaxy

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

// 发布做四件事的结果都留在可读的地方：产物可读回、指针指向它、对外地址返回它、
// 引用的资产在公开区。
func TestPublishRewritesAndServes(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	asset := f.uploadAsset(t, project.ID, "image/png", "a.png", []byte("aaa"))
	placeholder := PlaceholderScheme + asset.ID
	f.saveDraft(t, project.ID, `<img src="`+placeholder+`">`)
	version := f.saveVersion(t, project.ID)
	ctx := context.Background()

	publication := f.publish(t, project.ID, version.ID)

	// 产物是改写好的正文：占位符换成了公开区地址。
	want := strings.ReplaceAll(`<img src="`+placeholder+`">`, placeholder, f.service.Origin().AssetURL(asset.Digest))
	if string(publication.Content) != want {
		t.Errorf("产物 = %q，期望 %q", publication.Content, want)
	}
	// 留痕：发布人是他，时间是这次发布。
	if publication.PublishedBySubjectID != testOwner || publication.PublishedAt.IsZero() {
		t.Errorf("留痕 = %+v", publication)
	}
	// 指针指向这条记录，而对外地址返回的正是它。
	stored, err := f.store.GetProject(ctx, project.ID)
	if err != nil {
		t.Fatalf("读取工程失败: %v", err)
	}
	if stored.CurrentPublicationID != publication.ID {
		t.Errorf("发布指针 = %q，期望 %q", stored.CurrentPublicationID, publication.ID)
	}
	served, err := f.service.CurrentArtifact(ctx, project.ID)
	if err != nil {
		t.Fatalf("取产物失败: %v", err)
	}
	if served.Content != publication.Content {
		t.Error("对外地址返回的不是这次发布产物")
	}
	// 引用的资产在公开区，且是按内容摘要放的。
	exists, err := f.public.Exists(ctx, asset.Digest)
	if err != nil {
		t.Fatalf("查询公开区失败: %v", err)
	}
	if !exists {
		t.Error("被引用的资产没有上架")
	}
}

// **只上架该版本引用到的资产**，不搬整个资产库。
//
// 一个工程可能有几百个素材而某个页面只用了三个，整体搬运会让一次发布的时间与
// 成本取决于一个与它无关的数字。
func TestOnlyReferencedAssetsArePromoted(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	used := f.uploadAsset(t, project.ID, "image/png", "used.png", []byte("used"))
	f.uploadAsset(t, project.ID, "image/png", "unused1.png", []byte("unused1"))
	f.uploadAsset(t, project.ID, "image/png", "unused2.png", []byte("unused2"))
	f.saveDraft(t, project.ID, `<img src="`+PlaceholderScheme+used.ID+`">`)
	version := f.saveVersion(t, project.ID)

	f.publish(t, project.ID, version.ID)

	if f.public.Count() != 1 {
		t.Errorf("公开区对象数 = %d，期望 1", f.public.Count())
	}
	if digests := f.public.Digests(); len(digests) != 1 || digests[0] != used.Digest {
		t.Errorf("公开区的摘要 = %v，期望只含被引用的那一个", digests)
	}
}

// 撤回之后重新发布同一个版本：产物**逐字相同**，且不重复上架。
func TestRepublishIsIdenticalAndIdempotent(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	// 再放一个不被引用的资产：它不该被上架。
	f.uploadAsset(t, project.ID, "image/png", "unused.png", []byte("unused"))
	asset := f.uploadAsset(t, project.ID, "image/png", "b.png", []byte("bbb"))
	f.saveDraft(t, project.ID, `<img src="`+PlaceholderScheme+asset.ID+`">`)
	version := f.saveVersion(t, project.ID)
	ctx := context.Background()

	first := f.publish(t, project.ID, version.ID)
	promotedAfterFirst := f.public.Count()

	if err := f.service.Unpublish(ctx, testOwner, project.ID); err != nil {
		t.Fatalf("撤回失败: %v", err)
	}
	if _, err := f.service.CurrentArtifact(ctx, project.ID); !errors.Is(err, ErrPublicationNotFound) {
		t.Fatalf("撤回之后仍能取到产物: %v", err)
	}

	second := f.publish(t, project.ID, version.ID)
	if second.ID != first.ID {
		t.Errorf("重新发布的记录标识变了：%q → %q", first.ID, second.ID)
	}
	if second.Content != first.Content {
		t.Error("同一个版本两次发布的产物不同")
	}
	if f.public.Count() != promotedAfterFirst {
		t.Errorf("公开区对象数 %d → %d，重复发布不该产生新字节", promotedAfterFirst, f.public.Count())
	}
	// 上架是幂等的：第二次一个 Put 都没发出去（存在即跳过）。
	if len(f.public.Calls) != 1 {
		t.Errorf("上架调用 = %d 次，期望只有第一次那一次", len(f.public.Calls))
	}
}

// 被拒的发布**不留任何痕迹**：指针未动、公开区无新对象、发布表无新行。
func TestRejectedPublishLeavesNoTrace(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	good := f.uploadAsset(t, project.ID, "image/png", "a.png", []byte("aaa"))

	// 先发布一个合法的版本，作为"上一次的产物"。
	f.saveDraft(t, project.ID, `<img src="`+PlaceholderScheme+good.ID+`">`)
	published := f.saveVersion(t, project.ID)
	previous := f.publish(t, project.ID, published.ID)
	publicAfterPublish := f.public.Count()

	// 再准备一个引用了外部地址的版本：它必须发布失败。
	f.saveDraft(t, project.ID, `<img src="https://evil.example.com/a.png">`)
	broken := f.saveVersion(t, project.ID)

	ctx := context.Background()
	if _, err := f.service.Publish(ctx, testOwner, project.ID, broken.ID); !errors.Is(err, ErrInvalidContent) {
		t.Fatalf("err = %v，期望 ErrInvalidContent", err)
	}

	stored, err := f.store.GetProject(ctx, project.ID)
	if err != nil {
		t.Fatalf("读取工程失败: %v", err)
	}
	if stored.CurrentPublicationID != previous.ID {
		t.Error("被拒的发布动了指针")
	}
	if f.public.Count() != publicAfterPublish {
		t.Error("被拒的发布在公开区留下了对象")
	}
	served, err := f.service.CurrentArtifact(ctx, project.ID)
	if err != nil {
		t.Fatalf("取产物失败: %v", err)
	}
	if served.Content != previous.Content {
		t.Error("被拒的发布换掉了对外产物")
	}
	if stored.CurrentPublicationID == PublicationID(project.ID, broken.ID) {
		t.Error("被拒的发布落了库")
	}
}

// 落库**之前**中断（上架失败）：对外不留影响，指针没动。
func TestPublishFailureBeforeRecordLeavesPreviousArtifact(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	asset := f.uploadAsset(t, project.ID, "image/png", "a.png", []byte("aaa"))
	f.saveDraft(t, project.ID, `<img src="`+PlaceholderScheme+asset.ID+`">`)
	version := f.saveVersion(t, project.ID)

	f.public.PutErr = errors.New("上架失败")
	ctx := context.Background()
	if _, err := f.service.Publish(ctx, testOwner, project.ID, version.ID); err == nil {
		t.Fatal("上架失败却没有让发布失败")
	}

	stored, err := f.store.GetProject(ctx, project.ID)
	if err != nil {
		t.Fatalf("读取工程失败: %v", err)
	}
	if stored.CurrentPublicationID != "" {
		t.Error("上架失败却动了发布指针")
	}
	if _, err := f.store.GetPublication(ctx, PublicationID(project.ID, version.ID)); !errors.Is(err, ErrPublicationNotFound) {
		t.Error("上架失败却落了库")
	}
}

// **从检查点续跑**：产物已落库、指针还没切换时重试，不重复上架、不产生第二条记录。
//
// 这个状态由"撤回"构造出来（记录留着、指针为空），与"落库后崩溃重启"是同一个
// 形状。续跑之所以成立，是因为发布标识只由工程与版本决定。
func TestResumeFromCheckpointAfterRecordWritten(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	asset := f.uploadAsset(t, project.ID, "image/png", "a.png", []byte("aaa"))
	f.saveDraft(t, project.ID, `<img src="`+PlaceholderScheme+asset.ID+`">`)
	version := f.saveVersion(t, project.ID)
	ctx := context.Background()

	first := f.publish(t, project.ID, version.ID)
	if err := f.service.Unpublish(ctx, testOwner, project.ID); err != nil {
		t.Fatalf("撤回失败: %v", err)
	}
	// 库里的记录还在：这正是"已落库未切换"的形状。
	if _, err := f.store.GetPublication(ctx, first.ID); err != nil {
		t.Fatalf("发布记录应当保留: %v", err)
	}

	resumed := f.publish(t, project.ID, version.ID)
	if resumed.ID != first.ID {
		t.Errorf("续跑产生了第二条记录：%q → %q", first.ID, resumed.ID)
	}
	// 上架一次都没重做。
	if len(f.public.Calls) != 1 {
		t.Errorf("上架调用 = %d 次，期望第一次那一次", len(f.public.Calls))
	}
	stored, err := f.store.GetProject(ctx, project.ID)
	if err != nil {
		t.Fatalf("读取工程失败: %v", err)
	}
	if stored.CurrentPublicationID != first.ID {
		t.Error("续跑没有把指针切回去")
	}
}

// 撤回**立刻**生效：不依赖缓存过期。未发布与不存在返回同一个否定结果。
func TestUnpublishAndMissingAreIndistinguishable(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	asset := f.uploadAsset(t, project.ID, "image/png", "a.png", []byte("aaa"))
	f.saveDraft(t, project.ID, `<img src="`+PlaceholderScheme+asset.ID+`">`)
	version := f.saveVersion(t, project.ID)
	ctx := context.Background()

	// 未发布。
	_, errUnpublished := f.service.CurrentArtifact(ctx, project.ID)

	f.publish(t, project.ID, version.ID)
	if err := f.service.Unpublish(ctx, testOwner, project.ID); err != nil {
		t.Fatalf("撤回失败: %v", err)
	}
	// 撤回之后的下一次请求即不可达。
	_, errWithdrawn := f.service.CurrentArtifact(ctx, project.ID)
	// 标识没被猜中。
	_, errMissing := f.service.CurrentArtifact(ctx, "prj_不存在")

	for name, err := range map[string]error{
		"未发布": errUnpublished, "已撤回": errWithdrawn, "不存在": errMissing,
	} {
		if !errors.Is(err, ErrPublicationNotFound) {
			t.Errorf("%s 的结论 = %v，期望 ErrPublicationNotFound", name, err)
		}
	}
	if errUnpublished.Error() != errWithdrawn.Error() || errWithdrawn.Error() != errMissing.Error() {
		t.Errorf("三种情形的信息不同：%q / %q / %q", errUnpublished, errWithdrawn, errMissing)
	}
}

// 未配置公开桶或发布域时，发布整体不可用，而工程与资产照常可用。
func TestPublishUnavailableWithoutConfiguration(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	asset := f.uploadAsset(t, project.ID, "image/png", "a.png", []byte("aaa"))
	f.saveDraft(t, project.ID, `<img src="`+PlaceholderScheme+asset.ID+`">`)
	version := f.saveVersion(t, project.ID)
	ctx := context.Background()

	// 重装一个没有公开区、也没有发布域的部署。
	bare := NewService(Deps{
		Store:  f.store,
		Assets: f.objects,
		Logger: f.service.logger,
		Now:    func() time.Time { return f.now },
	})

	if _, err := bare.Publish(ctx, testOwner, project.ID, version.ID); !errors.Is(err, ErrPublishUnavailable) {
		t.Errorf("发布 err = %v，期望 ErrPublishUnavailable", err)
	}
	if _, err := bare.CurrentArtifact(ctx, project.ID); !errors.Is(err, ErrPublicationNotFound) {
		t.Errorf("取产物 err = %v，期望 ErrPublicationNotFound", err)
	}
	if bare.PageURL(project.ID) != "" {
		t.Error("没有发布域却给出了页面地址")
	}
	if bare.Capabilities().PublishEnabled {
		t.Error("没有公开区却报告发布可用")
	}
	// 工程与资产照常可用。
	if _, err := bare.ListAssets(ctx, testOwner, project.ID); err != nil {
		t.Errorf("列资产失败: %v", err)
	}
}

// **发布物不含任何主体信息**：产物里不出现拥有者的主体标识。
//
// 发布态是公开匿名的，把展示信息带上去会凭空引入一条身份暴露面。
func TestProductCarriesNoSubjectInformation(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	asset := f.uploadAsset(t, project.ID, "image/png", "a.png", []byte("aaa"))
	f.saveDraft(t, project.ID, `<img src="`+PlaceholderScheme+asset.ID+`">`)
	version := f.saveVersion(t, project.ID)

	publication := f.publish(t, project.ID, version.ID)

	if strings.Contains(string(publication.Content), testOwner) {
		t.Error("产物里出现了主体标识")
	}
	// 留痕在记录上（发布人是谁），但那是库里的事，不进产物。
	if publication.PublishedBySubjectID != testOwner {
		t.Error("发布的留痕丢了")
	}
}

// 改写生成的地址与内容安全策略里的**允许来源**来自同一个配置值。
//
// 两处各写一份的表现是"地址指向 A、策略允许 B"，而它表现为"发布成功了但什么
// 都显示不出来"。
func TestAllowedSourceAndAssetAddressComeFromOneValue(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	asset := f.uploadAsset(t, project.ID, "image/png", "a.png", []byte("aaa"))
	f.saveDraft(t, project.ID, `<img src="`+PlaceholderScheme+asset.ID+`">`)
	version := f.saveVersion(t, project.ID)
	publication := f.publish(t, project.ID, version.ID)

	// 从产物里取出改写后的地址，与策略里允许的来源比对。
	address := f.service.Origin().AssetURL(asset.Digest)
	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatalf("改写出的地址不是合法 URL: %v", err)
	}
	policy := f.service.Origin().AllowedSource()
	if parsed.Scheme+"://"+parsed.Host != policy {
		t.Errorf("地址的来源 %s://%s 与策略允许的 %s 不同", parsed.Scheme, parsed.Host, policy)
	}
	if !strings.Contains(string(publication.Content), address) {
		t.Error("产物里的地址不是这一处派生的")
	}
	// 两个地址（页面与资源）都由同一个 origin 对象派生，因此页面的来源
	// 与资源的来源不会互相串。
	if want := testPageOrigin + PublicPathPrefix + project.ID; f.service.PageURL(project.ID) != want {
		t.Errorf("页面地址 = %q，期望 %q", f.service.PageURL(project.ID), want)
	}
}

// 内容安全策略逐条固定：**扫描漏掉的写法仍然取不到东西**。
func TestContentSecurityPolicy(t *testing.T) {
	policy := ContentSecurityPolicy(mustOrigin(t))
	allowed := mustOrigin(t).AllowedSource()

	required := []string{
		"default-src 'none'",
		"img-src " + allowed,
		"media-src " + allowed,
		"script-src 'unsafe-inline'",
		"style-src 'unsafe-inline'",
		"connect-src 'none'",
		"frame-src 'none'",
		"object-src 'none'",
		"form-action 'none'",
		"base-uri 'none'",
	}
	for _, directive := range required {
		if !strings.Contains(policy, directive) {
			t.Errorf("策略里缺少 %q：\n%s", directive, policy)
		}
	}
	// 脚本可以跑，但发不出请求：这是"用户可以写交互，但不能把访问者的数据送出去"
	// 的落点，因此 connect-src 必须是 'none'。
	if strings.Contains(policy, "connect-src 'self'") || strings.Contains(policy, "connect-src *") {
		t.Error("connect-src 被放开了")
	}
}

// 发布域与公开桶的取值必须干净：https、有主机名、不带用户信息与路径。
func TestPublicOriginRejectsUnusableValues(t *testing.T) {
	cases := []struct {
		name   string
		assets string
		page   string
	}{
		{"公开桶不是 https", "http://assets.example.com", testPageOrigin},
		{"公开桶没有主机名", "https://", testPageOrigin},
		{"公开桶带路径", "https://assets.example.com/bucket", testPageOrigin},
		{"发布域带路径", testAssetsOrigin, "https://pages.example.com/g"},
		{"发布域带用户信息", testAssetsOrigin, "https://user:pass@pages.example.com"},
		{"发布域带查询串", testAssetsOrigin, "https://pages.example.com?a=1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewPublicOrigin(tc.assets, tc.page); err == nil {
				t.Error("期望拒绝，实际通过了")
			}
		})
	}
}
