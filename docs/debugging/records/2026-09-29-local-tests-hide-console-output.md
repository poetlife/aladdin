# 本地跑前端测试看不到告警：vitest 默认 reporter 吞掉通过用例的 console 输出

**日期**：2026-09-29
**收敛到的端**：前端——测试运行器（vitest 5）的输出策略，与业务代码无关

## 症状

门禁的「前端单元测试」一步会刷出几十条 `Warning: [antd: ...] ... is deprecated`，
但在本机 `make test-web`（或 `cd web && npm run test`）跑同样的代码，**一条都没有**，
测试全绿。第一反应是"本机依赖版本与 CI 不一致"，但核对后 antd 同为 6.6.5。

## 收敛过程

1. **是否版本漂移**：`node -e "require('./web/node_modules/antd/package.json').version"`
   与 lockfile 一致（6.6.5）——排除。
2. **告警在本机到底有没有触发**：临时加一个探针用例，渲染 `<Spin tip="x" />` 与
   `<Alert message="y" />` 并 spy `console.error`，结果是**调用确实发生了**——
   告警在本机是触发的，只是没被打出来。探针用完即删。
3. **输出去哪了**：同一个用例换成 `--reporter=verbose`，告警原样出现。
   所以是 reporter 的问题，不是代码或依赖的问题。

## 根因

**vitest 5 的默认 reporter 不打印通过用例的 console 输出**，而 CI 上会打印
（CI 日志里能看到 `stderr | <测试文件> > <用例名>` 的前缀）。于是"本地干净、
CI 刷屏"这个差异与代码、依赖版本、NODE_ENV 都无关，只与 reporter 有关。

顺带澄清一处易误判的守卫逻辑：antd 的 `_util/warning.js` 在 `NODE_ENV === 'test'`
时会主动 `resetWarned()`，即**测试环境下告警不去重、每次渲染都打**
（所以 26 个调用点在测试里会刷出 29～39 条）。这与"本地为什么看不到"无关，
但解释了 CI 上条数为什么远多于调用点数。

## 结论与边界

- **要复现或验证前端测试里的告警，用这条命令**：

  ```bash
  cd web && npx vitest run --reporter=verbose
  ```

  默认 reporter 的干净输出**不代表没有告警**，只代表没人把它打出来。
- 判断"某条告警是否已清零"时，以 CI 日志或上面的命令为准，不要以 `make test-web`
  的输出为准。
- 本条的适用版本是 vitest 5；升级后若默认 reporter 行为变化，这条结论需要重验。
