import js from '@eslint/js'
import tseslint from 'typescript-eslint'
import jsdoc from 'eslint-plugin-jsdoc'

export default [
  { ignores: ['dist', 'src/gen', 'node_modules'] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    files: ['**/*.ts', '**/*.tsx'],
    plugins: { jsdoc },
    rules: {
      // 文件有效代码行上限 800 行（不含注释和空行）
      'max-lines': ['warn', { max: 800, skipBlankLines: true, skipComments: true }],

      // 所有 export 的顶层声明应附带 JSDoc 注释
      'jsdoc/require-jsdoc': [
        'warn',
        {
          publicOnly: true,
          require: {
            FunctionDeclaration: true,
            ClassDeclaration: true,
          },
          contexts: [
            'ExportNamedDeclaration > VariableDeclaration',
            'TSInterfaceDeclaration',
            'TSTypeAliasDeclaration',
          ],
        },
      ],
    },
  },
  {
    files: ['**/*.ts'],
    rules: {
      // 单函数体有效代码行上限 100 行（不含注释和空行），不对 .tsx 生效
      'max-lines-per-function': ['warn', { max: 100, skipBlankLines: true, skipComments: true }],
    },
  },
]
