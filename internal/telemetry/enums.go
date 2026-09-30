package telemetry

import (
	telemetryv1 "github.com/poetlife/aladdin/api/gen/aladdin/telemetry/v1"
	"github.com/poetlife/aladdin/internal/observability"
)

// 本文件是**读侧把稳定取值折回协议枚举**的唯一入口。
//
// 取值到名字的映射在 actions.go（写侧），这里的方向正好相反。两者必须共用同一张
// 表：各写一份的话，某个动作改个名字就会出现"日志里是新名字、页面上按旧名字查
// 不到"——一个不会报错、只会让人以为"最近没这个动作"的偏差。因此反向映射**由正向
// 映射派生**，而不是再抄一遍。

// invert 把一张映射掉头。正向表是唯一信源，反向表由它算出。
func invert[K comparable, V comparable](m map[K]V) map[V]K {
	out := make(map[V]K, len(m))
	for k, v := range m {
		out[v] = k
	}
	return out
}

var (
	surfaceEnums = invert(surfaceNames)
	resultEnums  = invert(resultNames)
)

// SurfaceEnum 把界面/入口的稳定取值折回枚举。
//
// 库里的取值都是写侧筛过白名单的，因此"折不出来"只可能来自数据被外部改动。
// 那种情况下返回零值（未指定）而不是 panic：一个读侧接口不该因为一条脏数据
// 就整页打不开。
func SurfaceEnum(name string) telemetryv1.Surface {
	if s, ok := surfaceEnums[name]; ok {
		return s
	}
	return telemetryv1.Surface_SURFACE_UNSPECIFIED
}

// ResultEnum 把结局的稳定取值折回枚举。
func ResultEnum(name string) telemetryv1.Result {
	if r, ok := resultEnums[name]; ok {
		return r
	}
	return telemetryv1.Result_RESULT_UNSPECIFIED
}

// ClientEnum 把上报端取值折回枚举。
//
// 取值与 observability 的 ClientWeb / ClientCLI 同源（见 clientName）。
func ClientEnum(name string) telemetryv1.Client {
	switch name {
	case observability.ClientWeb:
		return telemetryv1.Client_CLIENT_WEB
	case observability.ClientCLI:
		return telemetryv1.Client_CLIENT_CLI
	default:
		return telemetryv1.Client_CLIENT_UNSPECIFIED
	}
}
