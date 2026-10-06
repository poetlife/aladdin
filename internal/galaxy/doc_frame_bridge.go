package galaxy

import "fmt"

// 文档页与宿主之间的接入桥：`docs` 槽渲染出的页面 ↔ 嵌它的主站壳 / 工作台预览。
//
// **这是平台在自己的页面上提供的一个标准接口，不是往用户内容里注入业务脚本。**
// `docs` 槽交付的整页本来就由本模块渲染（外壳、侧栏、目录、样式见 doc_render.go），
// 桥与它们同级；`site` 槽交付的是用户自己的整站，那条路一个字节都不加。分界写在
// docs/design/galaxy/site-model.md 的"平台接入桥"。
//
// **形状是跨语言的一份。** 页面侧这一段由服务端渲染进产物，宿主侧的解析在前端
// （web/src/pages/galaxy/frame-channel.ts）。两边没有共享的生成物——一边是 Go
// 常量、一边是 TypeScript——因此改这里就必须同时改那边，两侧的测试各钉一次。
const (
	// FrameChannelName 是通道标识。两侧都只认这一个值，别的一律忽略。
	FrameChannelName = "aladdin/frame"

	// FrameChannelVersion 是协议版本。收方不认识的版本一律忽略。
	FrameChannelVersion = 1
)

// frameBridgeSource 是那段脚本的模板：`%s` 是通道标识、`%d` 是协议版本。
//
// 两处占位而不是把取值写死，是为了让**上面那两个常量成为唯一的取值来源**——
// 写死的话，改了常量而忘了改这里，表现是宿主认不出页面发的消息，而两侧的代码
// 各自看都自洽。
//
// **它必须是常量，且不含任何用户数据。** 它的输入只有页面自己的 DOM，因此同一份
// 渲染规则下每一页的脚本逐字相同——这是它不动"渲染确定性"的前提（见
// RenderRulesVersion：桥是渲染规则的一部分，加它就要把版本加一）。
//
// 它只做两件事：
//
//   - 用户点正文里的一张图时，把**这一页的全部正文图**与被点那张的下标发给宿主；
//   - 按信封收下宿主发来的消息（v1 没有定义任何宿主侧类型，认不出的忽略）。
//
// 它不读也不写存储、不取任何外部地址、不改页面结构——**状态与呈现都不在它这里**，
// 这正是"明暗不做手动开关"仍然成立的原因（见 docStyleSheet）。
//
// 写法停在 ES5（`var` / `function`），产物要能在浏览器里直接打开，本模块不为它设
// 一个"最低浏览器版本"的前提。
const frameBridgeSource = `(function(){
var CHANNEL='%s',VERSION=%d;
function collect(){
var list=document.querySelectorAll('.doc img'),out=[],i,src;
for(i=0;i<list.length;i++){
src=list[i].currentSrc||list[i].src;
if(!src)continue;
out.push({src:src,alt:list[i].getAttribute('alt')||''});
}
return out;
}
function send(payload){
if(window.parent===window)return;
try{window.parent.postMessage({channel:CHANNEL,version:VERSION,type:'image-preview',payload:payload},'*');}catch(e){}
}
document.addEventListener('click',function(event){
var node=event.target;
while(node&&node!==document&&node.tagName!=='IMG'){node=node.parentNode;}
if(!node||node===document||node.tagName!=='IMG')return;
var images=collect(),wanted=node.currentSrc||node.src,index=-1,i;
for(i=0;i<images.length;i++){if(images[i].src===wanted){index=i;break;}}
if(index<0)return;
send({images:images,index:index});
},true);
window.addEventListener('message',function(event){
var data=event.data;
if(!data||data.channel!==CHANNEL||data.version!==VERSION)return;
});
})();
`

// frameBridgeScript 是填好取值的那段脚本。由模板与上面两个常量派生，因此不存在
// "常量改了、脚本没改"的漂移。
var frameBridgeScript = fmt.Sprintf(frameBridgeSource, FrameChannelName, FrameChannelVersion)

// frameBridgeTag 是那段脚本在产物里的形态。
//
// 走**内联**而不是外链，理由与 docStyleSheet 同源：外链要给出一个地址，而预览根与
// 发布根不同，外壳这一层并不知道自己在哪一个下面；内联没有这个地址问题，也与发布域
// 的策略相容（`script-src` 含 `'unsafe-inline'`，见 csp.go）。
func frameBridgeTag() string {
	return "<script>" + frameBridgeScript + "</script>\n"
}
