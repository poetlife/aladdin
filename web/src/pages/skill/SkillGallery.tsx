import { useState } from 'react'
import { Button, Image, Popconfirm, Space, Typography, Upload, theme } from 'antd'
import { ArrowDown, ArrowUp, ImageUp, Star, Trash2 } from 'lucide-react'

import * as skillApi from '../../api/skill'
import { PermissionGate } from '../../auth'
import { describeBytes } from '../../format/bytes'
import { PermissionCodes } from '../../gen/permission-codes'
import type { Skill } from '../../gen/proto/aladdin/skill/v1/skill_pb'
import { SkillCover } from './SkillCover'

/** 大图那块面积的高度。缩略图是正方形，另取一个更小的值。 */
const MAIN_IMAGE_HEIGHT = 220
const THUMBNAIL_SIZE = 56

interface SkillGalleryProps {
  skill: Skill
  /** 是不是正有一次写动作在进行：进行中时挡掉重复提交。 */
  busy: boolean
  /**
   * 统一执行一次写动作。
   *
   * 失败提示、"写完之后重读技能"（图片地址是服务端签的短时地址，本地猜不出来）
   * 与 busy 都收在页面那一处，画廊不自己维护第二份。
   */
  runAction: (action: () => Promise<unknown>) => Promise<void>
  /**
   * 图集上限，来自**服务端的能力下发**（GetCapabilities）。
   *
   * 三项都还没到（能力读得慢，或这个部署没配对象存储）时，那一句说明就不渲染——
   * 说一个猜的上限，不如不说。
   */
  maxImages?: number | undefined
  maxImageBytes?: number | undefined
  maxImageTotalBytes?: number | undefined
}

/**
 * 一个技能的**详情页画廊**：一张大图 + 一条缩略图 + 点大图放大。
 *
 * 图集是**有序**的，顺序第一张即首图。因此这里按标识记"正在看哪一张"，不按下标：
 * 重排与删除都会让下标变，而标识不会——按下标记的话，删掉第 2 张之后屏幕上会
 * 悄悄换成另一张，人以为删错了。
 *
 * 顺序由缩略图上的**上移 / 下移 / 设为首图**表达，每一次都发出**期望的完整顺序**
 * （重排按整体替换判，见 SkillAdminService）：第 2..n 张之间的相对位置因此也调得了，
 * 而不是只有"谁当首图"这一档。
 *
 * **目录卡片不用这一份。** 卡片是"一眼认出这是哪个技能"的场景，只用首图；把图集
 * 铺到卡片上会把它变成轮播，而"它做出来是什么样"是这一页的问题（见
 * docs/design/skill/catalog.md 的"展示图集"）。卡片走 SkillCover，两者不合并。
 *
 * 空图集时渲染 SkillCover 的占位（标题首字 + 中性底）：平台里没有"默认图"这类
 * 二进制资源，占位由界面自己生成。
 *
 * 每张图的地址都是**短时预签名地址**，会过期。因此这里不做任何缓存，写入之后一律
 * 重读一次技能，而不是在本地猜新地址。
 */
export function SkillGallery({
  skill,
  busy,
  runAction,
  maxImages,
  maxImageBytes,
  maxImageTotalBytes,
}: SkillGalleryProps): React.ReactNode {
  const { token } = theme.useToken()
  const [selectedId, setSelectedId] = useState('')

  const images = skill.images
  // 选中的那张按标识找；找不到（刚被删掉、或还没选过）就回到首图。
  const selected = images.find((image) => image.id === selectedId) ?? images[0]

  // 上限三项要么都用来渲染那句说明，要么都不说。任一项没给（或服务端给了 0，
  // 也就是没填）就整句不渲染：把"没下发"显示成"上限是 0"比不说更误导。
  const limits =
    maxImages !== undefined &&
    maxImages > 0 &&
    maxImageBytes !== undefined &&
    maxImageBytes > 0 &&
    maxImageTotalBytes !== undefined &&
    maxImageTotalBytes > 0
      ? { maxImages, maxImageBytes, maxImageTotalBytes }
      : null

  /**
   * 把某一张排到第一位，给出的是**期望的完整顺序**（服务端按整体替换判）。
   *
   * 不按下标拼：这里只用"去掉它再把剩下的按原顺序接上"，因此无论它原本在哪一位，
   * 得到的都是当前图集的一个排列。
   */
  function orderWithFirst(imageId: string): string[] {
    return [imageId, ...images.map((image) => image.id).filter((id) => id !== imageId)]
  }

  /**
   * 把某一张与相邻的一张对调，同样给出**完整顺序**。
   *
   * 上移、下移、设为首图在协议上是同一件事（一次整体替换），因此服务端不需要知道
   * "这一次是哪一种动作"；三种动作用同一个出口，也就不会有一种忘了发完整顺序。
   *
   * 取不到相邻那一个时原样返回当前顺序：按钮按图集渲染，正常走不到这里；真走到了
   * 也不该发一个少一项的排列出去——服务端只会整条拒绝。
   */
  function orderAfterSwap(index: number, other: number): string[] {
    const ids = images.map((image) => image.id)
    const moved = ids[index]
    const replaced = ids[other]
    if (moved === undefined || replaced === undefined) {
      return ids
    }
    ids[index] = replaced
    ids[other] = moved
    return ids
  }

  return (
    // antd 6 里 `direction` 已弃用（会打警告），因此这一处用 `orientation`：
    // 新代码不再添一处弃用调用。
    <Space orientation="vertical" size={8} style={{ width: '100%' }}>
      {selected === undefined ? (
        <SkillCover skill={skill} height={MAIN_IMAGE_HEIGHT} />
      ) : (
        /* 查看由 antd 的 Image 自带：点一下放大到原图。不做转码、不做裁切，
           放大的就是原样的字节（与资产库同一取向）。 */
        <Image
          src={selected.url}
          // 标题就在图旁边，这张图不承担额外的语义，因此空 alt 是对的。
          alt=""
          styles={{
            root: {
              width: '100%',
              borderRadius: token.borderRadius,
              overflow: 'hidden',
            },
            image: {
              width: '100%',
              height: MAIN_IMAGE_HEIGHT,
              objectFit: 'contain',
              display: 'block',
              background: token.colorFillTertiary,
            },
          }}
        />
      )}

      {/* 缩略图条**在自己的容器里横向滚动**：图多一些也不换行、不撑破这一行，
          窄屏上更不会把整页顶出横向滚动条（见 docs/design/web/responsive.md）。 */}
      <div
        style={{
          display: 'flex',
          gap: 8,
          overflowX: 'auto',
          paddingBottom: 4,
        }}
      >
        {images.map((image, index) => (
          <div key={image.id} style={{ flex: '0 0 auto', width: THUMBNAIL_SIZE }}>
            <button
              type="button"
              aria-label={`看第 ${index + 1} 张`}
              aria-current={image.id === selected?.id}
              onClick={() => setSelectedId(image.id)}
              style={{
                width: THUMBNAIL_SIZE,
                height: THUMBNAIL_SIZE,
                // 边框**两态都是 2px**：选中态只换颜色，免得未选中的那些比选中的
                // 小一圈、一选中就整条缩略图条抖一下。
                boxSizing: 'border-box',
                padding: 0,
                cursor: 'pointer',
                background: 'none',
                border: `2px solid ${image.id === selected?.id ? token.colorPrimary : 'transparent'}`,
                borderRadius: token.borderRadius,
                overflow: 'hidden',
              }}
            >
              <img
                alt=""
                src={image.url}
                style={{ width: '100%', height: '100%', objectFit: 'cover', display: 'block' }}
              />
            </button>
            {/* 管理动作**挨着它们作用的那一张**：上移/下移与设为首图改的都是它在
                图集里的位置，删除删的就是这一张，放到别处都变成要猜的问题（见
                docs/design/uiux/README.md 的"信息层级"）。四颗在 56px 的缩略图下
                排成两行两列，因此这里允许换行。 */}
            <PermissionGate require={PermissionCodes.SkillCatalogWrite}>
              <div style={{ display: 'flex', flexWrap: 'wrap', justifyContent: 'center' }}>
                {/* 第一张没有上一张、最后一张没有下一张：按 uiux 的规矩禁掉并说明
                    原因，而不是等人点了再报错。 */}
                <Button
                  type="text"
                  size="small"
                  disabled={busy || index === 0}
                  icon={<ArrowUp size={14} />}
                  aria-label="上移"
                  title={index === 0 ? '已经是第一张' : '上移'}
                  onClick={() =>
                    void runAction(() =>
                      skillApi.reorderSkillImages(skill.id, orderAfterSwap(index, index - 1)),
                    )
                  }
                />
                <Button
                  type="text"
                  size="small"
                  disabled={busy || index === images.length - 1}
                  icon={<ArrowDown size={14} />}
                  aria-label="下移"
                  title={index === images.length - 1 ? '已经是最后一张' : '下移'}
                  onClick={() =>
                    void runAction(() =>
                      skillApi.reorderSkillImages(skill.id, orderAfterSwap(index, index + 1)),
                    )
                  }
                />
                {/* 已经是首图时这一颗没有可做的事：按 uiux 的规矩禁掉并说明原因，
                    而不是等人点了再报错。 */}
                <Button
                  type="text"
                  size="small"
                  disabled={busy || index === 0}
                  icon={<Star size={14} />}
                  aria-label="设为首图"
                  title={index === 0 ? '已经是首图' : '设为首图'}
                  onClick={() =>
                    void runAction(() => skillApi.reorderSkillImages(skill.id, orderWithFirst(image.id)))
                  }
                />
                <Popconfirm
                  title="删掉这一张？"
                  description="缩略图与大图里的这一张都会消失，其余几张不受影响。"
                  okText="删除"
                  okButtonProps={{ danger: true }}
                  onConfirm={() =>
                    void runAction(() => skillApi.deleteSkillImage(skill.id, image.id))
                  }
                >
                  <Button
                    type="text"
                    size="small"
                    danger
                    disabled={busy}
                    icon={<Trash2 size={14} />}
                    aria-label="删除这一张"
                  />
                </Popconfirm>
              </div>
            </PermissionGate>
          </div>
        ))}
      </div>

      <PermissionGate require={PermissionCodes.SkillCatalogWrite}>
        <Space wrap size={8} align="center">
          {/* 加图走完三步（签发 → 直传 → 提交），都在 api/skill 里。这里只负责把
              文件递进去：**字节不经过服务端**，也不经过这个组件。 */}
          <Upload
            accept="image/png,image/jpeg,image/gif"
            showUploadList={false}
            beforeUpload={(file) => {
              void runAction(async () => {
                await skillApi.addSkillImage(skill.id, file)
              })
              // 返回 false：上传由我们自己走那条直传链路，不让 antd 再发一次。
              return false
            }}
          >
            <Button type="text" size="small" icon={<ImageUp size={14} />} disabled={busy}>
              加图
            </Button>
          </Upload>
          {/* 上限由服务端判（超了会拒），这里只是把服务端下发的规矩先说清楚，
              不在前端写死数字，也不在这里做安全判断。 */}
          {limits !== null && (
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              最多 {limits.maxImages} 张，单张不超过 {describeBytes(limits.maxImageBytes)}、合计不超过{' '}
              {describeBytes(limits.maxImageTotalBytes)}。
            </Typography.Text>
          )}
        </Space>
      </PermissionGate>
    </Space>
  )
}
