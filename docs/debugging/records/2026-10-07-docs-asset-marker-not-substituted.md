# docs 发布出去的两张图是裂的：markdown 里 raw HTML 的记号没人替换，校验也看不见

**日期**：2026-10-07
**收敛到的端**：服务端（`internal/galaxy` 的渲染与校验）。发布域、内容安全策略、上架与内容对象都正常

## 症状

docs 槽的 markdown 里按文档示例写 `<img src="asset://ast_xxx">`，一路都是绿的：

```
draft push → 已推送 5 个文件（3 份 markdown + 2 条资产条目）   ← 服务端认出了这两条引用
validate   → 无问题：这份草稿可以发布
version save / publish                                       → 成功
version pull → 取回的 index.html 与本地推送内容逐字节一致      ← 里面仍是字面量 asset://
```

而发布页上是**两张裂图**，只有读者的浏览器控制台看得见：

```
docs/:208 Loading the image 'asset://ast_-mkbFBta2Rd0e4CIO--nxw' violates the following
Content Security Policy directive: "img-src 'self' https://….myqcloud.com". The action has been blocked.
```

## 收敛过程

1. **`asset://` 有没有进文件组**：`draft push` 的"5 个文件"里含 2 条资产条目——记号登记
   （`galaxy.AssetMarkerIDs`）确实发生了。清单里那两条也在，因此"引用关系"这一层是好的。
2. **记号有没有被解开**：`version pull` 取回的是**源**（版本是源的快照），逐字节一致正好
   说明这条路没经过渲染。要证明发布态是否解开，当时**没有任何命令能读回发布态**——
   这是同一个问题暴露出来的第二个缺口（见下"结论与边界"）。
3. **渲染器为什么漏掉它**：`renderMarkdown` 的遍历只改写 `ast.Image` 与 `ast.Link` 的
   目的地；raw HTML 由 goldmark 原样写进产物（`mdhtml.WithUnsafe()`），**没有任何一步经手**。
4. **校验为什么也看不见**：`buildArtifacts` 对 markdown 走的是"渲染成页"那条分支，而
   引用扫描（`checkResourceReferences`）只跑在**非 markdown 的源**上。于是 raw HTML 里的
   记号、以及 raw HTML 里的**外部资源引用**，两样都不在任何一门规则里。

## 根因

**"记号会被替换"这条性质只对"已知的引用位置"成立，而 raw HTML 不在那份名单里。**

设计上写的是"记号是可被逐字替换的文本，因此不需要解析 HTML"，但实现把它落在了
"markdown 的图片/链接节点 + 非 markdown 文本整份替换"这两处上——raw HTML 两边都不沾。
更糟的是失败方式：产物里的字面量 `asset://…` 不是"取不到东西"那么显眼，它被发布域的
CSP 挡下，**只有读者的控制台会说话**。

看到这个症状先查什么：

- **文字、样式、链接都正常，只有某些图裂** → 先看那些图是 `asset://` 还是路径；是记号就
  落到这里。
- **`validate` 说没问题、publish 也成功** → 不要因此认为渲染路径被覆盖过：markdown 那一条
  分支当时不跑引用扫描。
- **`version pull` 与本地逐字节一致** → 那是**源**的结论，与发布态无关（版本是源的快照）。

## 结论与边界

- **替换点补齐到 raw HTML**（行内与块级，含其中的内联 CSS），仍复用"哪一段文本算一个记号"
  的那一处实现；**代码块、行内代码与正文散文里的记号原样保留**——那是给人看的文本，不是
  一处引用。写成 `<asset://…>` 这种自动链接的形状则是显式拒绝（它不是可替换的位置）。
- **复核改到产物上做**（`internal/galaxy/artifact_audit.go`）：产物里残留的记号、产物里指向
  文件组之外的资源引用、落在产物清单之外的引用，都在校验期拒绝。源回答"准备发布什么"，
  产物回答"实际发出去什么"——这条判据只能落在产物上。顺带补上 docs 槽 raw HTML 里外部
  资源引用此前完全不被检查的缺口。
- **读回发布态是另一半**：只修替换、不给"读回并逐条核对"的手段，下一次同类失败仍然只能
  靠读者反馈。因此同一批改动里加了 `aladdin galaxy publication get / pull / verify`，按
  **访客走的那条地址**把产物取回来复核（见 [publication.md](../../design/galaxy/publication.md)
  的"回读发布态"）。

## 参考

- [internal/galaxy/doc_render.go](../../../internal/galaxy/doc_render.go)（raw HTML 的替换点）
- [internal/galaxy/artifact_audit.go](../../../internal/galaxy/artifact_audit.go)（产物复核唯一入口）
- [cmd/aladdin/command-galaxy-publication.go](../../../cmd/aladdin/command-galaxy-publication.go)
- [docs/design/galaxy/authoring.md](../../design/galaxy/authoring.md)（"替换落在哪些位置"那张表）
