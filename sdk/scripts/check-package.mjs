/**
 * 打包检查：**发出去的包里到底有什么**。
 *
 * 这个包发到公共 npm 源，用户拿到的就是我们 `npm pack` 出来的那些字节，因此
 * "本地跑得对"与"发出去能用"是两件事。检查的每一项都对应一个**不会报错的**
 * 失败方式：
 *
 *   - `dist/` 在 .gitignore 里。少跑一次构建时，`npm pack` **不报错**，只是安静地
 *     打出一个只含 `package.json` 的包——发出去之后用户装不上，而这里一切正常。
 *   - 许可证正文只在包目录里才被 npm 收进包（包外的根 `LICENSE` 收不到），
 *     少了它，包上只剩一个 `license` 字段。
 *   - `src` 进包是为了让使用者的编辑器与 sourcemap 能跳进实现，但测试不该跟着出去。
 *
 * 与 `publint` 的分工：publint 验的是**入口能不能被解析**（`exports` 与类型声明
 * 对得上），这里验的是**字节在不在包里**。两者都不看行为，行为由单元测试管。
 */

import { execFileSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const packageRoot = join(dirname(fileURLToPath(import.meta.url)), '..')

/** 三个入口（外加根入口）在后两列里必须各有一份产物与一份类型声明。 */
const ENTRIES = ['index', 'build/index', 'client/index', 'runtime/index']

/** 无论 `files` 怎么写都必须在包里的东西。 */
const ALWAYS_INCLUDED = ['package.json', 'README.md', 'LICENSE']

const packed = packFileList()
const missing = []

for (const entry of ENTRIES) {
  for (const suffix of ['.js', '.d.ts']) {
    const path = `dist/${entry}${suffix}`
    if (!packed.has(path)) {
      missing.push(path)
    }
  }
}
for (const path of ALWAYS_INCLUDED) {
  if (!packed.has(path)) {
    missing.push(path)
  }
}

const leakedTests = [...packed].filter((path) => path.endsWith('.test.ts'))

if (missing.length > 0 || leakedTests.length > 0) {
  if (missing.length > 0) {
    console.error('包里缺少这些文件：')
    for (const path of missing) {
      console.error(`  - ${path}`)
    }
  }
  if (leakedTests.length > 0) {
    console.error('包里不该有测试文件：')
    for (const path of leakedTests) {
      console.error(`  - ${path}`)
    }
  }
  process.exit(1)
}

console.log(`打包检查通过：${packed.size} 个文件，四个入口的产物、类型声明与许可证都在包里。`)

/**
 * 让 npm 打出将要发布的那份包，取回文件清单。
 *
 * 用 `npm pack` 而不是自己读 `files`：白名单、npm 始终包含的那几项、以及 `.gitignore`
 * 与 `.npmignore` 的相互作用都由 npm 决定，自己再实现一遍就是同一件事的第二个判据。
 * `prepack` 会因此跑一次构建——这正是要的：包里没有构建产物时，这里就该失败。
 */
function packFileList() {
  const stdout = execFileSync('npm', ['pack', '--dry-run', '--json'], {
    cwd: packageRoot,
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'inherit'],
  })
  const [result] = JSON.parse(stdout)
  return new Set(result.files.map((file) => file.path))
}
