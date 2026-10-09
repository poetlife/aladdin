import { opsClient } from './transport'

/**
 * 部署实例自述的调用封装。
 *
 * 与 telemetry-admin.ts 同理：类型来自 proto 生成，这里只提供有名字的调用入口。
 *
 * scope **只用于鉴权**、不过滤结果（见 ops.proto）：部署信息没有归属范围。因此
 * 这一层不提供"按范围筛选"的参数——那不是服务端能回答的问题。
 */

/** 读本进程所在部署实例的构建与运行信息。 */
export async function getDeploymentInfo(scope: string) {
  return opsClient().getDeploymentInfo({ scope })
}
