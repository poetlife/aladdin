package watermark

import (
	"crypto/rand"
	"errors"
	"fmt"
)

// 图案几何，单位是 CSS 像素。这些常量是唯一取值来源。
//
// 16×7 个数据位不是随便定的：一帧 112 位，扣掉 16 位前导与 16 位 CRC 后正好
// 80 位，而 80 位刚好是 16 个 Crockford base32 字符（每字符 5 位）。位宽、码长、
// 校验强度三件事由这一组数字一次定死，改动任何一项都会同时改掉另外两项——所以
// 它们必须是常量而不是配置。
const (
	CellPx          = 4                                     // 一个格子
	BlockCells      = 4                                     // 一位数据占 4×4 个格子
	FrameBlocksWide = 16                                    // 一帧横向 16 位
	FrameBlocksHigh = 7                                     // 一帧纵向 7 位
	FrameWidthPx    = CellPx * BlockCells * FrameBlocksWide // 256
	FrameHeightPx   = CellPx * BlockCells * FrameBlocksHigh // 112
)

// 帧内以「格子」为单位的尺寸。提取时按格重采样，因此这些派生量在那边用得更多。
const (
	cellsWide = FrameBlocksWide * BlockCells // 64
	cellsHigh = FrameBlocksHigh * BlockCells // 28
)

// 前导位。取值本身没有含义，选它是因为它**不是**一堆常数位：16 位里 0/1 大致各半，
// 且自相关在所有非零循环位移上都很低，于是"帧相位找错了"会立刻在相关值上表现出来。
// 若前导选 0xFFFF，任何相位下的常数区域都能对上，定位就退化了。
const preamble uint16 = 0xA5C3

// 码长与码表。
//
// Crockford base32 而不是 base32 标准表：它去掉 I/L/O/U 四个易混字符（1 与 I、
// 0 与 O），码是要被人抄下来、念出来、填进工单的。32 个字符 × 16 位 = 80 位，
// 与帧里留给码的位数严丝合缝。
const (
	CodeLen     = 16
	codeBits    = CodeLen * 5 // 80
	bitsPerChar = 5
)

// crockford 是码表，顺序与 Crockford base32 一致：下标即 5 位取值。
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// ErrNotFound 是提取失败的唯一结论。
//
// 「不是本平台的图」「裁得太小」「变换太强」三件事在提取层面无法区分，也不该
// 区分：调用方要做的事完全相同——当作没有水印，不要再试一次，也不要根据错误
// 文案去猜原因。给出更细的错误只会诱使上层对某一种情况做特殊处理，而那些情况
// 本身没有稳定的边界。
var ErrNotFound = errors.New("提取不到水印")

// CRC-16 参数取 CCITT-FALSE：多项式 0x1021、初值 0xFFFF、不反射、不异或输出。
//
// 手写而不是调库：Go 标准库没有 hash/crc16（只有 crc32/crc64），而这里既要
// 标准库限定、又不想引第三方。表驱动的版本只有二十行，且**参数必须写死**——
// 它和位序、字节打包顺序一起构成"历史截图还能不能解出来"的契约。
var crcTable = makeCRCTable()

func makeCRCTable() [256]uint16 {
	var t [256]uint16
	for i := 0; i < 256; i++ {
		crc := uint16(i) << 8
		for b := 0; b < 8; b++ {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
		t[i] = crc
	}
	return t
}

// crc16Of 算 CCITT-FALSE：初值 0xFFFF、逐字节异或进高八位、最后不取反。
func crc16Of(data []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, b := range data {
		crc = crc<<8 ^ crcTable[byte(crc>>8)^b]
	}
	return crc
}

// NewCode 生成一个新的水印码：16 个 Crockford base32 字符（大写，不含 I/L/O/U）。
//
// 80 位来自密码学随机源而不是计数器或时间戳：码与水印的每一次下发绑定，必须
// 猜不出来，否则"这张截图是谁的"就能被伪造。这里的熵与 idgen 的 128 位不同档，
// 因为它只用于追溯、不做访问控制。
func NewCode() (string, error) {
	var raw [codeBits / 8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("生成水印码失败: %w", err)
	}
	return encodeCode(raw[:]), nil
}

// ValidCode 判定一个字符串是不是合法水印码。
//
// 只认大写的规范形式，不做 Crockford 那套 I→1、O→0 的纠错映射：手抄场景在
// 这个码的用途里不存在（它是给程序比对用的），放宽只会让"两个不同输入是同一个
// 码"成为可能，而那是审计里最难解释的一类问题。
func ValidCode(code string) bool {
	if len(code) != CodeLen {
		return false
	}
	for i := 0; i < len(code); i++ {
		if decodeChar(code[i]) < 0 {
			return false
		}
	}
	return true
}

// decodeChar 把字符映射回 5 位取值，非法字符返回 -1。
func decodeChar(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'A' && c <= 'Z':
		// 字母段在码表里是跳着排的（去掉 I/L/O/U），因此只能线性找。
		for i := 10; i < len(crockford); i++ {
			if crockford[i] == c {
				return i
			}
		}
	}
	return -1
}

// encodeCode 把 10 字节按大端序切成 16 个 5 位组，逐个映射成字符。
//
// 大端序是这里唯一需要记住的约定：字节 0 的高 5 位是第 0 个字符。位序在
// codeToBits 与 bitsToCode 两处必须一致，否则同一条码会在 Tile 与 Extract 之间
// 变成两条。
func encodeCode(raw []byte) string {
	out := make([]byte, CodeLen)
	for i := 0; i < CodeLen; i++ {
		v := 0
		for b := 0; b < bitsPerChar; b++ {
			bit := i*bitsPerChar + b
			v = v<<1 | int(raw[bit/8]>>(7-uint(bit%8))&1)
		}
		out[i] = crockford[v]
	}
	return string(out)
}

// codeToBits 把码展开成 80 个 0/1，下标即帧内位序。
//
// 帧内位序按 row*FrameBlocksWide+col 排，码占 16..95 位（前导占 0..15，CRC 占
// 96..111）。位序是提取与生成之间唯一的隐含契约，因此它只在这一个函数里成形。
func codeToBits(code string) ([]byte, error) {
	if !ValidCode(code) {
		return nil, fmt.Errorf("水印码 %q 不合法", code)
	}
	bits := make([]byte, codeBits)
	for i := 0; i < CodeLen; i++ {
		v := decodeChar(code[i])
		for b := 0; b < bitsPerChar; b++ {
			bits[i*bitsPerChar+b] = byte(v >> (bitsPerChar - 1 - uint(b)) & 1)
		}
	}
	return bits, nil
}

// bitsToCode 是 codeToBits 的逆向，供提取结果使用。
func bitsToCode(bits []byte) string {
	out := make([]byte, 0, CodeLen)
	for i := 0; i < CodeLen; i++ {
		v := 0
		for b := 0; b < bitsPerChar; b++ {
			v = v<<1 | int(bits[i*bitsPerChar+b]&1)
		}
		out = append(out, crockford[v])
	}
	return string(out)
}

// frameBits 是"一个码 → 一帧 112 位"的完整布局：前导 + 码 + CRC。
//
// CRC 只覆盖码那 80 位，不覆盖前导：前导在提取里是**搜索目标**（用它定位相位），
// 若把它也算进校验，那么"相位找错但数据恰好自洽"的图仍会被拒——这没有坏处，
// 但会让定位失败的图连一次校验机会都没有。更实际的理由是：CRC 的用途是拒掉
// 无水印图，而误报来自"数据位看起来像码"，与前导无关。
func frameBits(code string) ([]byte, error) {
	codeBitsOut, err := codeToBits(code)
	if err != nil {
		return nil, err
	}
	frame := make([]byte, FrameBlocksWide*FrameBlocksHigh)
	for i := 0; i < 16; i++ {
		frame[i] = byte(preamble >> (15 - uint(i)) & 1)
	}
	copy(frame[16:], codeBitsOut)
	crc := crcOfBits(codeBitsOut)
	for i := 0; i < 16; i++ {
		frame[96+i] = byte(crc >> (15 - uint(i)) & 1)
	}
	return frame, nil
}

// crcOfBits 按"每 8 位一字节、高位在前"把位流打包后算 CCITT-FALSE。
//
// 打包顺序是生成与提取之间第二处隐含契约。它不需要与任何外部标准一致（两处都
// 是我们自己的代码），但必须写死在一处，否则改一次实现就会让所有历史截图失效。
func crcOfBits(bits []byte) uint16 {
	packed := make([]byte, len(bits)/8)
	for i := range packed {
		var v byte
		for b := 0; b < 8; b++ {
			v = v<<1 | bits[i*8+b]&1
		}
		packed[i] = v
	}
	return crc16Of(packed)
}

// splitFrame 把 112 位拆回前导、码、CRC 三段，供提取校验使用。
func splitFrame(frame []byte) (pre uint16, code string, crc uint16) {
	for i := 0; i < 16; i++ {
		pre = pre<<1 | uint16(frame[i]&1)
	}
	code = bitsToCode(frame[16:96])
	for i := 0; i < 16; i++ {
		crc = crc<<1 | uint16(frame[96+i]&1)
	}
	return pre, code, crc
}
