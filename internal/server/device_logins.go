package server

import (
	"crypto/rand"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/poetlife/aladdin/internal/rbac"
)

const (
	// deviceLoginTTL 是一次设备码登录从发起到批准之间的允许时长。
	deviceLoginTTL = 15 * time.Minute
	// deviceLoginInterval 是建议的轮询间隔。
	deviceLoginInterval = 5 * time.Second
	// deviceLoginMax 是服务端同时记住的设备码登录条数上界。
	deviceLoginMax = 4096
	// deviceCodeBytes 是设备码的随机字节数：32 字节即 256 位。
	deviceCodeBytes = 32
	// userCodeLength 是短码的位数（不含给人看时那个连字符）。
	userCodeLength = 8

	// DeviceApprovalPath 是批准页在前端的路径。
	//
	// 它**是对外契约的一部分**：服务端把它拼进对外地址交给终端，前端要用
	// 同一个路径注册路由（见 web/src/router.tsx）。改一处而不改另一处，
	// 表现为终端打印出一个打不开的地址。
	//
	// 与 /login/callback 同理，它**不在 /auth/ 下**：那一段被反向代理整段
	// 转发给服务端，前端在那里放路由会被服务端接走。
	DeviceApprovalPath = "/device"
)

// userCodeAlphabet 是短码的字母表：20 个辅音字母。
//
// 不含数字，也不含元音。不含数字让"1 与 I、0 与 O"这类混淆根本无从发生；
// 不含元音让它拼不出成词的东西——短码要被人从终端的等宽字体里读出来、再在
// 浏览器里打进去，一次读错的表现是一次无解的"代码无效"。
//
// 大小写不敏感，因此这里只用大写表示，输入侧的折算见 normalizeUserCode。
const userCodeAlphabet = "BCDFGHJKLMNPQRSTVWXZ"

// deviceLoginState 是一次设备码登录当前所处的阶段。
//
// 服务端把"不能交付"的几种理由分开记，调用方却只该看到一件事：这次登录还能
// 不能继续等。把"还没批准"表达成一个错误码，会让"这一次轮询没结果"与
// "你未认证"变成同一个结论（见 docs/design/identity/device-login.md）。
type deviceLoginState int

const (
	// deviceLoginNone：从未存在、已过期、或已经交付过。三者对调用方是同一件事。
	deviceLoginNone deviceLoginState = iota
	deviceLoginPending
	deviceLoginApproved
	deviceLoginDenied
)

// deviceLoginEntry 是一次设备码登录的记录。
type deviceLoginEntry struct {
	userCode  string
	expiresAt time.Time
	state     deviceLoginState
	// subject 只在已批准时有值。**它是待交付的东西，不是待交付的凭证**：
	// 会话在被交付的那一刻才签发，因此这张表里不存在任何明文令牌。
	subject rbac.Subject
}

// deviceLogins 是一张有界的短时设备码表。
//
// 它为什么**不**长在 oneTimeStore 上：那张表的值是不可变的、键是单向的，而
// 设备码登录要经历 待批准 → 已批准 的流转，且批准方只持有短码、必须能反查。
// 硬套会让那张表承担两种语义，而它的"单次使用"性质正是靠现在这份简单换来的
// （见 docs/ssot-registry.md）。
//
// 它**不跨重启**：进程重启让进行中的登录作废，重新发起一次即可。理由与其它的
// 一次性凭据相同——这些记录的有效期以分钟计，为它建一张表并写一次迁移不划算。
type deviceLogins struct {
	mu sync.Mutex
	// byDeviceCode 按设备码索引：轮询与交付都从这里进。
	byDeviceCode map[string]*deviceLoginEntry
	// byUserCode 从短码反查设备码：批准方只持有短码。两张表必须同增同删。
	byUserCode map[string]string

	// now 允许测试拨动时间；生产上就是 time.Now。
	now func() time.Time
}

// newDeviceLogins 构造一张有界的短时设备码表。
func newDeviceLogins(now func() time.Time) *deviceLogins {
	return &deviceLogins{
		byDeviceCode: make(map[string]*deviceLoginEntry),
		byUserCode:   make(map[string]string),
		now:          now,
	}
}

// issuedDeviceLogin 是一次刚发起的设备码登录。
type issuedDeviceLogin struct {
	deviceCode string
	userCode   string
	expiresAt  time.Time
}

// start 发起一次设备码登录。
func (d *deviceLogins) start() (issuedDeviceLogin, error) {
	deviceCode, err := newOpaqueToken(deviceCodeBytes)
	if err != nil {
		return issuedDeviceLogin{}, err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	now := d.now()
	d.pruneLocked(now)
	// 先腾位置再生成短码：反过来做的话，上界已满时被淘汰的可能正是这条
	// 刚写进去的记录——它的短码刚落进索引又被摘掉。
	if len(d.byDeviceCode) >= deviceLoginMax {
		d.evictSoonestLocked()
	}

	userCode, err := d.newUserCodeLocked()
	if err != nil {
		return issuedDeviceLogin{}, err
	}

	expiresAt := now.Add(deviceLoginTTL)
	d.byDeviceCode[deviceCode] = &deviceLoginEntry{
		userCode:  userCode,
		expiresAt: expiresAt,
		state:     deviceLoginPending,
	}
	d.byUserCode[userCode] = deviceCode
	return issuedDeviceLogin{deviceCode: deviceCode, userCode: userCode, expiresAt: expiresAt}, nil
}

// poll 查一次状态；已批准时**在同一次加锁里把记录消费掉**并交出主体。
//
// 交付恰好一次由这一次加锁保证：分开写的话，两个并发轮询会各查到一次"已批准"，
// 然后各领走一份会话——那正是"只交付一次"要消灭的形状。
//
// 已批准但会话签发失败时，这条记录也不会回来（它已经被消费掉）。这是有意的：
// 让一份已批准记录在下一次轮询里再次交付，就等于允许重复交付。
//
// 已拒绝的记录**不消费**：让调用方多读到几次同一个结论，比只读到一次更可靠，
// 而它同样会随有效期被清掉。
func (d *deviceLogins) poll(deviceCode string) (deviceLoginState, rbac.Subject) {
	d.mu.Lock()
	defer d.mu.Unlock()

	entry, ok := d.byDeviceCode[deviceCode]
	if !ok {
		return deviceLoginNone, rbac.Subject{}
	}
	if !d.now().Before(entry.expiresAt) {
		d.deleteLocked(deviceCode, entry)
		return deviceLoginNone, rbac.Subject{}
	}
	switch entry.state {
	case deviceLoginApproved:
		subject := entry.subject
		d.deleteLocked(deviceCode, entry)
		return deviceLoginApproved, subject
	case deviceLoginDenied:
		return deviceLoginDenied, rbac.Subject{}
	default:
		return deviceLoginPending, rbac.Subject{}
	}
}

// approve 按短码把这次登录记为已批准，归属记成 subject。
//
// 返回 false 表示这份短码不存在、已过期、已交付，或**已被另一个人批准**——
// 对调用方是同一件事：这次批准不能完成，让命令行重新发起。
func (d *deviceLogins) approve(userCode string, subject rbac.Subject) bool {
	return d.conclude(userCode, func(entry *deviceLoginEntry) bool {
		switch entry.state {
		case deviceLoginPending:
			entry.state = deviceLoginApproved
			entry.subject = subject
			return true
		case deviceLoginApproved:
			// 同一个人重复按（页面重试、双击）：幂等地成功。
			// 换一个人按：第一次批准已经决定了归属，后来者不得改写它——
			// 否则拿到短码的人能在别人已经批准之后把归属抢到自己身上。
			return entry.subject.ID == subject.ID
		default:
			return false
		}
	})
}

// deny 按短码把这次登录记为已拒绝。
func (d *deviceLogins) deny(userCode string) bool {
	return d.conclude(userCode, func(entry *deviceLoginEntry) bool {
		if entry.state != deviceLoginPending {
			return false
		}
		entry.state = deviceLoginDenied
		return true
	})
}

// conclude 按短码找到记录，在锁内作出结论。
//
// 批准与拒绝共用它：两者的差别只在锁内那一下做了什么，而"查得到吗、过期了吗"
// 是同一条判断。各写一份迟早会让其中一条漏掉过期判断。
func (d *deviceLogins) conclude(userCode string, decide func(*deviceLoginEntry) bool) bool {
	code := normalizeUserCode(userCode)
	if code == "" {
		return false
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	deviceCode, ok := d.byUserCode[code]
	if !ok {
		return false
	}
	entry, ok := d.byDeviceCode[deviceCode]
	if !ok {
		// 索引与记录不同步：记录已被消费或被淘汰，留下的是索引残渣。
		delete(d.byUserCode, code)
		return false
	}
	if !d.now().Before(entry.expiresAt) {
		d.deleteLocked(deviceCode, entry)
		return false
	}
	return decide(entry)
}

// newUserCodeLocked 生成一个当前未被占用的短码。
//
// "没有重复"必须是**生成时保证**的，而不是概率上期待：两条记录撞上同一个短码，
// 后一条会顶掉前一条的索引，表现为一次无法解释的批准落到了另一次登录上。
func (d *deviceLogins) newUserCodeLocked() (string, error) {
	for {
		code, err := newUserCode()
		if err != nil {
			return "", err
		}
		if _, taken := d.byUserCode[code]; !taken {
			return code, nil
		}
	}
}

// deleteLocked 摘掉一条记录，短码索引一并摘掉。
func (d *deviceLogins) deleteLocked(deviceCode string, entry *deviceLoginEntry) {
	delete(d.byDeviceCode, deviceCode)
	delete(d.byUserCode, entry.userCode)
}

// pruneLocked 清掉已过期的记录。
func (d *deviceLogins) pruneLocked(now time.Time) {
	for deviceCode, entry := range d.byDeviceCode {
		if !now.Before(entry.expiresAt) {
			d.deleteLocked(deviceCode, entry)
		}
	}
}

// evictSoonestLocked 淘汰最快过期的一条。
//
// 它只在"上界已被占满"时发生，因此这次扫描罕见且有界。
// 淘汰的代价是一次进行中的登录要重来，比无界增长小得多。
func (d *deviceLogins) evictSoonestLocked() {
	var soonest string
	var soonestAt time.Time
	for deviceCode, entry := range d.byDeviceCode {
		if soonest == "" || entry.expiresAt.Before(soonestAt) {
			soonest, soonestAt = deviceCode, entry.expiresAt
		}
	}
	if soonest == "" {
		return
	}
	d.deleteLocked(soonest, d.byDeviceCode[soonest])
}

// newUserCode 生成一份短码，存的是**不带连字符**的规范形态。
//
// 给人看时的那道连字符由 formatUserCode 加，不进来：存放形态只有一种，
// 输入的折算才有唯一的目标（见 normalizeUserCode）。
func newUserCode() (string, error) {
	limit := big.NewInt(int64(len(userCodeAlphabet)))
	code := make([]byte, userCodeLength)
	for i := range code {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", err
		}
		code[i] = userCodeAlphabet[n.Int64()]
	}
	return string(code), nil
}

// normalizeUserCode 把使用者输入的短码折成存放时的形态。
//
// 短码是给人打的：大小写、连字符、顺手敲进的空格都随人怎么写。折算只应有
// 这一处——散在各调用方各折一遍，迟早会有一条路径折得不一致，而那表现为
// "终端上明明是这个码，页面却说无效"。
func normalizeUserCode(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		switch r {
		case '-', ' ', '\t':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// formatUserCode 把短码折成给人看的形式（中间一个连字符）。
func formatUserCode(code string) string {
	mid := len(code) / 2
	if mid == 0 || mid >= len(code) {
		return code
	}
	return code[:mid] + "-" + code[mid:]
}
