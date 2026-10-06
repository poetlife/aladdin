import { theme } from 'antd'

import type { Skill } from '../../gen/proto/aladdin/skill/v1/skill_pb'

/** 卡片上封面那块面积的高宽比。 */
export const COVER_HEIGHT = 144

/**
 * 一张技能封面。
 *
 * **没有封面时渲染占位**（标题首字 + 中性底），而不是去取一张不存在的图：平台里没有
 * "默认图"这类二进制资源，占位由界面自己生成（见 docs/design/skill/catalog.md 的
 * "封面"）。
 *
 * 地址是服务端下发的**短时预签名地址**，会过期。因此这里不做任何缓存：重新读一次
 * 技能就有新的，而缓存下来的一份只会换来一张图裂。
 */
export function SkillCover({ skill, height = COVER_HEIGHT }: { skill: Skill; height?: number }): React.ReactNode {
  const { token } = theme.useToken()

  if (skill.coverUrl === '') {
    // 首字取有效标题：它已经由服务端算过回退，前端不再拼一遍。
    const initial = Array.from(skill.title.trim())[0] ?? '·'
    return (
      <div
        aria-hidden
        style={{
          height,
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          background: token.colorFillTertiary,
          color: token.colorTextQuaternary,
          fontSize: Math.round(height / 4),
          lineHeight: 1,
          userSelect: 'none',
        }}
      >
        {initial}
      </div>
    )
  }

  return (
    <img
      // 标题就在图旁边，这张图不承担额外的语义，因此空 alt 是对的。
      alt=""
      src={skill.coverUrl}
      style={{ height, width: '100%', objectFit: 'cover', display: 'block' }}
    />
  )
}
