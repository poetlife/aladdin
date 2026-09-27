import { useCallback, useEffect, useState } from 'react'
import { useLocation } from 'react-router-dom'
import { Alert, Button, Card, Divider, Flex, Listy, Popconfirm, Space, Tag, Typography } from 'antd'
import { KeyRound, Link2 } from 'lucide-react'

import * as identityApi from '../api/identity'
import { messageOf } from '../api/errors'
import { GoogleSignInButton } from '../auth/google-sign-in-button'
import type { AuthMethod, Identity } from '../gen/proto/aladdin/identity/v1/identity_pb'

/** 回调页带回的绑定结果。identityBound 一定存在：一份结果总是"绑定了谁"。 */
interface BindingResult {
  identityBound: string
  reclaimed?: boolean
}

/**
 * 「登录方式」卡片：列出当前主体已经绑定哪些渠道，并提供绑定/解绑入口。
 *
 * 归属完全由服务端决定：GitHub 走一次浏览器导航 + HttpOnly 待绑定凭据，
 * Google 走一次把 ID token 交给 BindIdentity 的 RPC；两者都只作用于当前
 * 会话代表的主体，前端没有任何"绑到谁"的输入。
 */
export function IdentityCard(): React.ReactNode {
  const location = useLocation()
  const [identities, setIdentities] = useState<Identity[] | null>(null)
  const [methods, setMethods] = useState<AuthMethod[]>([])
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<BindingResult | null>(null)

  // 回调页把结果放在 history state 里；这里只读一次，并保留到下一次操作，
  // 使得用户看得到"刚绑了什么"，而不是默默多出一行。两个处理函数都会清掉它，
  // 否则这条提示会在解绑之后仍然说"已绑定某某"。
  useEffect(() => {
    const state = location.state as BindingResult | null
    if (state?.identityBound !== undefined) {
      setResult(state)
    }
  }, [location.state])

  const reload = useCallback(async (): Promise<void> => {
    try {
      const [list, authMethods] = await Promise.all([
        identityApi.listIdentities(),
        identityApi.getAuthMethods(),
      ])
      setIdentities(list.identities)
      setMethods(authMethods)
      setError(null)
    } catch (err) {
      setError(messageOf(err))
    }
  }, [])

  useEffect(() => {
    void reload()
  }, [reload])

  async function handleBindGoogle(idToken: string): Promise<void> {
    setBusy(true)
    setError(null)
    // 上一次的结果到此为止：再挂着一个"已绑定"，说的就是一件不再成立的事。
    setResult(null)
    try {
      const resp = await identityApi.bindGoogleIdentity(idToken)
      setIdentities(resp.identities)
      setResult({ identityBound: identityApi.AuthSource.Google, reclaimed: resp.reclaimed })
    } catch (err) {
      setError(messageOf(err))
    } finally {
      setBusy(false)
    }
  }

  async function handleUnbind(identity: Identity): Promise<void> {
    setBusy(true)
    setError(null)
    setResult(null)
    try {
      const resp = await identityApi.unbindIdentity(identity.source, identity.externalId)
      setIdentities(resp.identities)
    } catch (err) {
      setError(messageOf(err))
    } finally {
      setBusy(false)
    }
  }

  const list = identities ?? []
  const boundSources = new Set(list.map((identity) => identity.source))
  const bindable = methods.filter((method) => !boundSources.has(method.source))

  return (
    <Card
      title={
        <Space size={8}>
          <KeyRound size={16} />
          登录方式
        </Space>
      }
    >
      {result !== null && (
        <Alert
          type="success"
          showIcon
          style={{ marginBottom: 16 }}
          message={
            result.reclaimed === true
              ? `已把此前单独登录过的 ${sourceLabel(result.identityBound)} 账号并入当前账号`
              : `已绑定 ${sourceLabel(result.identityBound)}`
          }
        />
      )}
      {error !== null && (
        <Alert
          type="error"
          showIcon
          message={error}
          style={{ marginBottom: 16 }}
          // 读不到现状时给一条出路：否则卡片会永远停在"正在读取登录方式…"，
          // 用户除了整页刷新之外没有别的办法。
          action={
            identities === null ? (
              <Button size="small" onClick={() => void reload()}>
                重试
              </Button>
            ) : undefined
          }
        />
      )}

      {identities === null ? (
        error === null && <Typography.Text type="secondary">正在读取登录方式…</Typography.Text>
      ) : (
        <>
          {list.length === 0 ? (
            <Typography.Text type="secondary">还没有绑定任何登录方式</Typography.Text>
          ) : (
            <Listy
              items={list}
              rowKey={(identity) => `${identity.source}:${identity.externalId}`}
              itemRender={(identity) => {
                const last = list.length <= 1
                return (
                  <Flex justify="space-between" align="center" gap={16}>
                    <Space orientation="vertical" size={4}>
                      <Space>
                        <Tag>{sourceLabel(identity.source)}</Tag>
                        <Typography.Text>{identity.display || identity.externalId}</Typography.Text>
                      </Space>
                      <Typography.Text type="secondary">{identity.externalId}</Typography.Text>
                    </Space>
                    <Popconfirm
                      title={`解绑 ${sourceLabel(identity.source)}？`}
                      description="解绑之后它不再进入当前账号，登录时会登记出一个新的零权限主体。"
                      okText="解绑"
                      cancelText="取消"
                      disabled={last}
                      onConfirm={() => void handleUnbind(identity)}
                    >
                      <Button danger type="link" disabled={last}>
                        解绑
                      </Button>
                    </Popconfirm>
                  </Flex>
                )
              }}
            />
          )}
          {list.length <= 1 && (
            <Typography.Text type="secondary">
              不能解绑最后一个登录方式，否则这个账号将再也进不来。
            </Typography.Text>
          )}

          {bindable.length > 0 && (
            <>
              <Divider plain>绑定新的登录方式</Divider>
              <Space orientation="vertical" size="middle">
                {bindable.map((method) => {
                  if (method.source === identityApi.AuthSource.Github) {
                    // 整页跳转：GitHub 的授权码必须由服务端用客户端密钥换取。
                    // 绑定意图只由 purpose 标记表达，归属在回跳后的已认证兑换里决定。
                    return (
                      <Button
                        key={method.source}
                        href="/auth/github/start?purpose=bind"
                        icon={<Link2 size={16} />}
                        loading={busy}
                      >
                        绑定 GitHub
                      </Button>                    )
                  }
                  if (method.source === identityApi.AuthSource.Google) {
                    return (
                      <GoogleSignInButton
                        key={method.source}
                        clientId={method.clientId}
                        onCredential={(idToken) => void handleBindGoogle(idToken)}
                      />
                    )
                  }
                  return null
                })}
              </Space>
            </>
          )}
        </>
      )}
    </Card>
  )
}

/** 把渠道来源翻译成界面文案；未知来源保留原值，方便排查配置。 */
function sourceLabel(source: string): string {
  switch (source) {
    case identityApi.AuthSource.Google:
      return 'Google'
    case identityApi.AuthSource.Github:
      return 'GitHub'
    default:
      return source
  }
}
