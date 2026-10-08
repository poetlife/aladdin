#!/usr/bin/env bash
#
# aladdin 的部署入口。**在目标服务器上执行**，不是在本机。
#
# 一次部署 = 拉取产物 → 校验和 → 原子替换 → 重启 → 健康检查 → 失败回滚。
#
# 为什么是"服务器主动拉"而不是 CI 推：推的模式要把一把能登录生产的 SSH 私钥
# 放进 GitHub secrets。单实例下，"发完版跑一条命令"的成本可以接受，
# 而少一把能登录生产的钥匙是实打实的收窄。
#
# 用法：
#   sudo ./deploy.sh                     # 部署最新 Release
#   sudo ./deploy.sh v0.1.0              # 部署指定版本
#   sudo ./deploy.sh v0.1.0 --from <基地址>  # 产物从别处取（见下）
#
# `--from` 换掉的只是**产物从哪来**，其余一字不改：校验和、备份、原子替换、
# 健康检查、失败回滚仍然走本脚本这同一条路径。因此它取的东西必须与 Release 里
# 的是同一套——同一个地址下三个文件，命名与 `make release-build` 出来的一致。
#
# 它解决的是"传不动"：本机到服务器的上行很差，或者服务器取不到 GitHub。中转
# 地址怎么来见 docs/deploy.md 的"网络不好时：产物走中转"。
#
# 首次部署的前置步骤、回滚的注意事项、迁移与回滚的冲突见 docs/deploy.md。

set -euo pipefail

REPO="poetlife/aladdin"

# 本站域名。**部署前必须把占位符换成真实域名**——脚本靠它定位 nginx 站点，
# 换错的表现是"站点明明配好了，脚本却说它不存在"。
#
# 这类实例相关的值一律不进仓库：这是一份公开仓库，而域名、主机别名、
# 公网地址属于部署实例，不属于这份代码。仓库里放模板，实值只留在服务器上
# （本脚本本身就是被拷到服务器后再填的副本，不是 git 检出）。
SITE_DOMAIN="__SITE_DOMAIN__"

APP_DIR="/opt/aladdin"
BIN_DIR="${APP_DIR}/bin"
BIN="${BIN_DIR}/aladdin-server"
WEB_DIR="${APP_DIR}/web"
CONFIG="${APP_DIR}/config.yml"
SERVICE="aladdin-server"
UNIT="/etc/systemd/system/aladdin-server.service"
NGINX_SITE="/etc/nginx/sites-enabled/${SITE_DOMAIN}"

# 监听端口。它与 config.yml 的 address、nginx 站点配置里的 proxy_pass
# 是同一件事的三处表述，改一处必须同时改另外两处。
# 这里用的就是服务端的内置默认值——该机器上 9090 空闲。
PORT="9090"

HEALTH_URL="http://127.0.0.1:${PORT}/grpc.health.v1.Health/Check"
HEALTH_RETRIES=15

# CLI 自更新兜底镜像的落盘位置。nginx 用 /cli/latest/ 服务它（见
# deploy/nginx-aladdin-site.conf 与 docs/design/cli/self-update.md）。
CLI_ROOT="${APP_DIR}/cli"
CLI_LATEST="${CLI_ROOT}/latest"

WORK=""
PREV_BIN=""
PREV_WEB=""

log()  { printf '>>> %s\n' "$*"; }
warn() { printf '警告：%s\n' "$*" >&2; }
fail() { printf '错误：%s\n' "$*" >&2; exit 1; }

cleanup() {
  [[ -n "${WORK}" && -d "${WORK}" ]] && rm -rf "${WORK}"
  return 0
}

require_root() {
  [[ "$(id -u)" -eq 0 ]] || fail "需要 root：要替换 /opt/aladdin 下的文件并重启 systemd 单元。请用 sudo 执行。"
}

require_cmd() {
  local missing=()
  for c in "$@"; do
    command -v "$c" >/dev/null 2>&1 || missing+=("$c")
  done
  ((${#missing[@]} == 0)) || fail "缺少命令：${missing[*]}"
}

latest_version() {
  # 不用 jq：它不在目标机器的默认安装里，而这里只需要取一个字段。
  curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep -o '"tag_name"[[:space:]]*:[[:space:]]*"[^"]*"' \
    | head -1 \
    | sed 's/.*"\([^"]*\)"$/\1/'
}

download_and_verify() {
  local server_pkg="aladdin-server_${VERSION}_linux_amd64.tar.gz"
  local web_pkg="aladdin-web_${VERSION}.tar.gz"
  # 产物来源只有这一处判断：默认是本次版本的 Release 地址，`--from` 给了就用它。
  # 两条来源的**产物形态完全相同**（同一个地址下三个文件），因此下面的下载、
  # 校验与替换不需要知道自己从哪里取的。
  local base="${ARTIFACT_BASE:-https://github.com/${REPO}/releases/download/${VERSION}}"
  local hint="版本号是否存在？标签必须形如 vX.Y.Z"
  if [[ -n "${ARTIFACT_BASE}" ]]; then
    hint="基地址下有没有这三个文件？命名要与 make release-build 出来的一致"
  fi

  log "拉取 ${VERSION} 的产物（来源：${base}）"
  for f in "${server_pkg}" "${web_pkg}" SHA256SUMS; do
    curl -fsSL -o "${WORK}/${f}" "${base}/${f}" \
      || fail "下载失败：${base}/${f}（${hint}）"
  done

  # 只校验本次要用的两个包：SHA256SUMS 里还有 darwin 与 CLI 的条目，
  # 它们没被下载，整份 -c 会因为"找不到文件"而失败。
  #
  # 先取出条目再喂给 sha256sum，而不是直接管道：`sha256sum -c` 拿到空输入时
  # 会**返回 0**，也就是"一个文件都没校验"与"全部通过"不可区分。
  # 这里把"必须有条目"变成一步显式的断言，而不是依赖 pipefail 兜底。
  log "校验和"
  local sums
  sums="$(grep -E "(${server_pkg}|${web_pkg})$" "${WORK}/SHA256SUMS")" \
    || fail "SHA256SUMS 里没有本次要用的包，产物形态可能变了。已中止，未做任何改动。"
  ( cd "${WORK}" && printf '%s\n' "${sums}" | sha256sum -c - >/dev/null ) \
    || fail "校验和不匹配，产物可能不完整。已中止，未做任何改动。"

  mkdir -p "${WORK}/server" "${WORK}/web"
  tar -xzf "${WORK}/${server_pkg}" -C "${WORK}/server"
  tar -xzf "${WORK}/${web_pkg}" -C "${WORK}/web"
  [[ -f "${WORK}/server/aladdin-server" ]] || fail "服务端包内没有 aladdin-server"
  [[ -f "${WORK}/web/dist/index.html" ]] || fail "前端包内没有 dist/index.html"
}

backup_current() {
  PREV_BIN="${APP_DIR}/aladdin-server.prev"
  PREV_WEB="${APP_DIR}/web.prev.tar.gz"

  if [[ -f "${BIN}" ]]; then
    cp -a "${BIN}" "${PREV_BIN}"
    log "已备份当前二进制"
  else
    log "未发现既有二进制（首次部署）"
  fi

  if [[ -d "${WEB_DIR}" ]]; then
    tar -czf "${PREV_WEB}" -C "${WEB_DIR}" .
    log "已备份当前前端产物"
  else
    PREV_WEB=""
  fi
}

install_binary() {
  log "替换二进制"
  # 先装到同目录的临时名再 mv：mv 在同一文件系统上是原子的，
  # 不会出现"进程正在执行一个被截断的二进制"的窗口。
  install -o aladdin -g aladdin -m 0755 "${WORK}/server/aladdin-server" "${BIN}.new"
  mv -f "${BIN}.new" "${BIN}"
}

install_frontend() {
  log "替换前端产物"
  mkdir -p "${WEB_DIR}"
  # 先清空再铺：留着上一版的文件会让"删掉的页面仍然能访问"，
  # 而那种残留只有在用户点进去时才发现。
  find "${WEB_DIR}" -mindepth 1 -delete
  cp -a "${WORK}/web/dist/." "${WEB_DIR}/"

  # nginx 以 www-data 身份读这些文件，而 deploy.sh 以 root 写入。
  # 把读权限显式放开，免得"文件明明在、nginx 却 403"。
  chmod -R a+rX "${WEB_DIR}"

  # 不需要 reload nginx：静态文件是按请求从磁盘读的，换掉目录内容即刻生效。
  # 这也意味着**改 nginx 配置才需要 reload**，那是前置步骤，不在本脚本里。
}

health_check() {
  curl -fsS --max-time 3 \
    -X POST \
    -H 'Content-Type: application/json' \
    -H 'Connect-Protocol-Version: 1' \
    -d '{"service":""}' \
    "${HEALTH_URL}" 2>/dev/null
}

restart_and_check() {
  log "重启 ${SERVICE}"
  systemctl restart "${SERVICE}"

  local out
  for ((i = 1; i <= HEALTH_RETRIES; i++)); do
    if out="$(health_check)" && grep -q 'SERVING' <<<"${out}"; then
      log "健康检查通过（第 ${i} 次）"
      return 0
    fi
    sleep 1
  done

  printf '健康检查未通过，最后一次响应：%s\n' "${out:-<无响应>}" >&2
  printf -- '--- 最近的服务端日志 ---\n' >&2
  journalctl -u "${SERVICE}" -n 40 --no-pager >&2 || true
  return 1
}

rollback() {
  printf -- '--- 回滚 ---\n' >&2

  if [[ -n "${PREV_BIN}" && -f "${PREV_BIN}" ]]; then
    install -o aladdin -g aladdin -m 0755 "${PREV_BIN}" "${BIN}"
    log "已还原上一个二进制"
  else
    printf '没有可还原的二进制（首次部署失败，保持现状）\n' >&2
  fi

  if [[ -n "${PREV_WEB}" && -f "${PREV_WEB}" ]]; then
    find "${WEB_DIR}" -mindepth 1 -delete
    tar -xzf "${PREV_WEB}" -C "${WEB_DIR}"
    chmod -R a+rX "${WEB_DIR}"
    log "已还原上一个前端产物"
  fi

  systemctl restart "${SERVICE}" || true

  cat >&2 <<'EOF'

回滚后服务是否能起来，取决于本次发布有没有带数据库迁移。
持久化模块约定"库里存在代码中不存在的版本时拒绝启动"（见
docs/design/persistence/README.md），因此**换了二进制而库没换**时，
回滚会直接拒绝启动——这是刻意的，不是故障。

这种情况要连着库快照一起回，见 docs/deploy.md 的"回滚"。
EOF
}

# 刷新主站的 CLI 自更新兜底镜像（/cli/latest/）。
#
# 为什么是服务器拉而不是 CI 推：推的模式要把一把能写生产的东西放进 GitHub
# secrets，与脚本顶部那条取舍是同一件事——单实例下，部署时顺手拉一次的代价
# 可以接受，而少一把能写生产的钥匙是实打实的收窄。
#
# **尽力而为**：走到这一步 Release 已经产出、服务端已经换好，没有可回滚的东西，
# 因此任何失败都只告警、绝不让本次部署失败。失败时**不碰现有镜像**——宁可是旧的
# 一份，也不要是空的（空了等于把兜底路径整个拿掉）。
#
# 镜像的是**GitHub 当时的 latest**，不是本次部署的 VERSION：一次服务端回滚不该
# 把第三方 CLI 的镜像一起往回带。
mirror_cli() {
  local stage="${CLI_ROOT}/.latest.new"
  # 任何一条提前退出都走这里：清掉半成品，保留现有镜像。
  skip() { warn "跳过 CLI 镜像刷新：$1"; rm -rf "${stage}"; return 0; }

  local json
  json="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest")" \
    || { skip "读不到最新 Release 元数据"; return 0; }

  local tag
  tag="$(grep -o '"tag_name"[[:space:]]*:[[:space:]]*"[^"]*"' <<<"${json}" \
    | head -1 | sed 's/.*"\([^"]*\)"$/\1/')"
  [[ -n "${tag}" ]] || { skip "Release 元数据里没有版本号"; return 0; }

  # 取资产列表而不是照 Makefile 的 PLATFORMS 硬编码：将来加平台不必改这里。
  # 只取 CLI 包（排除服务端与前端）与校验和清单——它们的命名是对外契约的一部分。
  local urls
  urls="$(grep -o '"browser_download_url"[[:space:]]*:[[:space:]]*"[^"]*"' <<<"${json}" \
    | sed 's/.*"\([^"]*\)"$/\1/' \
    | grep -E '/(aladdin_[^/]*\.tar\.gz|SHA256SUMS)$' \
    | grep -vE '/aladdin-server_|/aladdin-web_')" || urls=""
  [[ -n "${urls}" ]] || { skip "Release 里没有 CLI 产物"; return 0; }

  rm -rf "${stage}"
  mkdir -p "${stage}" || { skip "无法创建暂存目录 ${stage}"; return 0; }

  local names="" u name
  while IFS= read -r u; do
    [[ -n "${u}" ]] || continue
    name="${u##*/}"
    curl -fsSL -o "${stage}/${name}" "${u}" \
      || { skip "下载 ${name} 失败"; return 0; }
    names="${names}${names:+ }${name}"
  done <<<"${urls}"

  # 只核对本次镜像的包：SHA256SUMS 里还有服务端与前端，它们没被下载，
  # 整份 -c 会因为"找不到文件"而失败。与 download_and_verify 同一处写法，
  # 包括"先取条目再喂给 sha256sum"——空输入时 sha256sum -c 会返回 0，
  # 也就是"一个都没校验"与"全部通过"不可区分。
  local pkgs="" sums
  for name in ${names}; do
    [[ "${name}" == "SHA256SUMS" ]] && continue
    pkgs="${pkgs}${pkgs:+|}${name}"
  done
  sums="$(grep -E "(${pkgs})$" "${stage}/SHA256SUMS")" \
    || { skip "校验和清单里没有本次要用的包"; return 0; }
  ( cd "${stage}" && printf '%s\n' "${sums}" | sha256sum -c - >/dev/null ) \
    || { skip "校验和不匹配"; return 0; }

  # 元数据：tag + 实际镜像的资产名。形态是与客户端之间的对外契约
  # （见 docs/design/cli/self-update.md）：客户端按契约从 tag 推出本平台的包名，
  # 再要求它和 SHA256SUMS 都出现在这个表里。
  local assets=""
  for name in ${names}; do
    assets="${assets}${assets:+,}\"${name}\""
  done
  printf '{"tag":"%s","assets":[%s]}\n' "${tag}" "${assets}" > "${stage}/version.json" \
    || { skip "写 version.json 失败"; return 0; }

  # 整体替换：先清掉旧的一份再换上去（rename 不能覆盖一个非空目录）。
  # 中间那一瞬没有 latest 是可接受的——镜像只是兜底，取不到时客户端会如实报错。
  rm -rf "${CLI_LATEST}"
  mv "${stage}" "${CLI_LATEST}" || { skip "替换 ${CLI_LATEST} 失败"; return 0; }

  # nginx 以 www-data 读这些文件，而本脚本以 root 写入。
  chmod -R a+rX "${CLI_LATEST}" || warn "放开 ${CLI_LATEST} 的读权限失败，nginx 可能读不到"

  log "已刷新 CLI 镜像：${tag}（${names}）"
  return 0
}

main() {
  require_root
  require_cmd curl tar sha256sum install

  id -u aladdin >/dev/null 2>&1 \
    || fail "系统用户 aladdin 不存在。它是二进制与数据目录的属主，
创建命令见 docs/deploy.md 的『一次性前置』。"

  [[ -f "${CONFIG}" ]] \
    || fail "缺少配置文件 ${CONFIG}。
它不在发布产物里，需要一次性创建（见 docs/deploy.md）。"

  [[ -f "${UNIT}" ]] \
    || fail "缺少 systemd 单元 ${UNIT}，重启会失败。
安装命令见 docs/deploy.md 的『一次性前置』。"

  # 这条检查防的是"产物换好了、但没人能访问到"：nginx 站点没配时，
  # 部署会成功、健康检查会通过，而浏览器打开是另一个站点的页面。
  [[ -e "${NGINX_SITE}" ]] \
    || fail "nginx 站点 ${NGINX_SITE} 不存在。
先按 docs/deploy.md 的『一次性前置』配置站点，否则前端产物放了也无人服务。"

  # 参数解析只有这一处。两个值都赋成"后面几个函数直接读的"那样：它们是本脚本的
  # 全局，与既有的那些（APP_DIR、PORT）同一个用法。
  VERSION=""
  ARTIFACT_BASE=""
  while (($# > 0)); do
    case "$1" in
      --from)
        [[ -n "${2:-}" ]] || fail "--from 后面要跟一个基地址（产物所在目录的地址）。"
        ARTIFACT_BASE="${2%/}"
        shift 2
        ;;
      -*)
        fail "未知选项：$1（用法见本脚本顶部）"
        ;;
      *)
        [[ -z "${VERSION}" ]] || fail "只接受一个版本号，多给了：$1"
        VERSION="$1"
        shift
        ;;
    esac
  done

  if [[ -z "${VERSION}" ]]; then
    # 中转地址下没有"最新"这一说：它只是一个目录，谁放的、什么时候放的都没有
    # 记录。因此这条路上版本号必须显式给出——它同时也是产物文件名的一部分。
    [[ -z "${ARTIFACT_BASE}" ]] || fail "用了 --from 就必须显式给出版本号。
中转地址只是一个目录，没有「最新」这一说；版本号要与你放进去的产物文件名一致。"
    VERSION="$(latest_version)" || fail "取不到最新 Release 版本号"
    log "未指定版本，取最新 Release：${VERSION}"
  fi

  WORK="$(mktemp -d)"
  trap cleanup EXIT

  download_and_verify
  backup_current
  install_binary
  install_frontend

  if ! restart_and_check; then
    rollback
    fail "部署 ${VERSION} 失败，已回滚"
  fi

  log "部署完成：${VERSION}"

  # 收尾：刷新主站的 CLI 自更新镜像。它与本次部署的版本号无关（镜像的是
  # GitHub 当时的 latest），失败也不影响这次部署——见上面的说明。
  mirror_cli
}

main "$@"
