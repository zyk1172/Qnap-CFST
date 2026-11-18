const GUIDE_REPOSITORY = 'https://github.com/zyk1172/Qnap-CFST'

const GUIDE_CHAPTERS = [
  {
    id: 'deploy',
    title: '部署与挂载',
    body: `
      <p>CFHost 为适用的 Cloudflare 域名筛选候选 IP，按站点策略验证后生成映射，再按你的设置写入 NAS 的 Hosts。应用通过系统解析器读取这些映射。Hosts 管理的范围由文件挂载和应用的解析方式决定。</p>
      <ol>
        <li><strong>选择镜像和安装方式。</strong> 使用 Docker Compose 或 NAS 的容器管理界面。多架构镜像为 <code>ghcr.io/zyk1172/qnap-cfst:cfhost</code>，支持 Linux AMD64 和 ARM64。需要可回退的部署时，选用仓库发布页提供的固定版本标签。</li>
        <li><strong>准备数据目录和访问端口。</strong> 将 NAS 上专用目录挂载到容器的 <code>/data</code>，确保容器可读写。默认 Compose 将容器的 <code>8080</code> 映射到 NAS 的 <code>9876</code>。启动后在局域网打开 <code>http://NAS的局域网IP:9876</code>，如有自定义端口请使用实际端口。</li>
        <li><strong>挂载同一份 Hosts。</strong> 将 NAS 的 <code>/etc/hosts</code> 以读写方式绑定到 CFHost 的 <code>/host/etc/hosts</code>，并把宿主机 <code>/etc</code> 只读挂到 <code>/host/etc-check</code>。设置 <code>HOSTS_VERIFY_PATH=/host/etc-check/hosts</code>，供程序核对读写挂载是否仍指向宿主机当前文件。</li>
        <li><strong>接入其他容器。</strong> 要让下载器或代理共用映射，按其镜像文档把同一份 NAS Hosts 只读绑定到它使用的位置，通常为容器内的 <code>/etc/hosts</code>。保留 localhost、NAS 主机名等原有条目。使用自带 DNS 或 DoH 的应用还要确认其解析链路。</li>
      </ol>
      <p class="guide-note">镜像默认以 root 运行以访问挂载文件，普通部署无需特权模式。遇到权限错误，请检查数据目录 ACL、挂载读写标志和文件所有权，无需把系统 Hosts 改成所有用户可写。</p>
      <p class="guide-links">
        ${guideLink(GUIDE_REPOSITORY + '/tree/master/deploy', '各 NAS 平台部署说明')}
        ${guideLink(GUIDE_REPOSITORY + '/releases', 'GitHub 发布页')}
      </p>
    `,
  },
  {
    id: 'first-run',
    title: '首次配置与验证',
    body: `
      <ol>
        <li><strong>先保留手动控制。</strong> 新配置默认关闭自动 Smart Repair 和自动应用 Hosts。复用已有 <code>/data</code> 会沿用原开关，建议先到「设置」确认，并保存修改。</li>
        <li><strong>在「域名」中核对列表。</strong> 禁用或删除不需要的预置条目。先选一个适合使用 Cloudflare 候选 IP 的域名测试，域名填主机名，HTTP 验证路径从 <code>/</code> 开始。按实际用途选择 HTTP 或 Tracker 验证。</li>
        <li><strong>在「候选 IP」执行一次测速。</strong> 查看候选数量、延迟、丢包和采样时间。仅测延迟的候选没有带宽证据，主要用于通过域名验证的延迟策略。</li>
        <li><strong>验证映射。</strong> 在「域名」点击目标条目的「维护」，或在「任务与历史」运行智能 Repair。确认状态、当前 IP 和探测结果后，再到概览执行「应用 Hosts」。</li>
        <li><strong>验证实际应用。</strong> 在依赖该映射的容器中检查实际域名解析和业务请求。对于 Tracker，观察下载器新一次汇报结果。确认正常后，再按需打开自动修复与自动应用。</li>
      </ol>
      <h3>如何选择域名策略</h3>
      <dl class="guide-definitions">
        <div><dt>延迟（latency）</dt><dd>优先低丢包、低延迟，仍须通过该域名的验证。适合一般站点或 Tracker。</dd></div>
        <div><dt>带宽（bandwidth）</dt><dd>按带宽条件过滤并优先选吞吐较好的候选，同时验证目标域名。适合对下载速度有要求的站点。</dd></div>
        <div><dt>普通（normal）</dt><dd>从有带宽测量的候选中选低延迟地址，跳过站点验证。仅在你接受这一取舍时使用。</dd></div>
        <div><dt>跟随（follow）</dt><dd>复用一个已配置的独立域名的映射，跟随域名本身不发探测。先确认两个域名可以共用同一个 IP。</dd></div>
      </dl>
    `,
  },
  {
    id: 'probes',
    title: 'HTTP API 与 Tracker 探测',
    body: `
      <h3>带凭据的 HTTP API</h3>
      <p>在「域名」编辑 HTTP 条目，选择延迟或带宽策略后配置自定义探测。按目标站点文档填写路径、GET 或 POST、请求头与正文类型。POST 会对多个候选重复发送，只用于无副作用的查询。默认要求 2xx，并检查挑战页。如果 API 用 200 返回业务错误，请额外填写成功正文正则。</p>
      <dl class="guide-definitions">
        <div><dt>请求头名</dt><dd><code>x-api-key</code>，具体名称以目标 API 文档为准。</dd></div>
        <div><dt>请求头值引用</dt><dd><code>file:api-key</code>，从容器的 <code>/run/secrets/cfhost/api.example.com/api-key</code> 读取。示例中的域名替换成该条目的完整主机名，凭据目录以只读方式挂载。</dd></div>
        <div><dt>环境变量引用</dt><dd>也可以用 <code>env:CFHOST_PROBE_API_KEY</code>。同时设置 <code>CFHOST_PROBE_API_KEY_HOST=api.example.com</code>，绑定到对应域名。环境变量可能被有容器管理权限的人查看，优先用文件引用。</dd></div>
      </dl>
      <p>密钥只写入对应的凭据文件或环境变量。需要 Bearer 时，引用的内容包含完整的 <code>Bearer </code> 前缀和值。POST 如需 JSON 正文，可通过正文引用读取完整 JSON，并将正文类型填为 <code>application/json</code>。路径和正则等普通配置字段请勿填写密钥。探测限制跨域、跨端口和 HTTPS 降级跳转。凭据缺失时先补配置再维护。</p>
      <h3>下载器与真实 Tracker 汇报</h3>
      <ol>
        <li>在「设置 → Hosts 与 Tracker」启用使用的 Transmission 或 qBittorrent 客户端，填写 CFHost 容器可访问的 RPC 或 WebUI 地址及认证信息。独立的 bridge 容器内，<code>127.0.0.1</code> 指向 CFHost 自身。</li>
        <li>需要验证真实 Tracker 时打开「Tracker 真实 announce」。启用自动样本发现后，程序为受管 Tracker 从已完成任务中选择合适的样本。在域名详情检查样本来源和测试结果，缺样本时检查下载器地址、认证、任务状态和 Tracker 域名是否匹配。</li>
        <li>真实探测面向 HTTPS Tracker 和 v1 info-hash。样本可达与下载器实际汇报是两项证据。Transmission 的保活功能在符合条件时可请求下载器重新汇报，建议以下载器最新的实际汇报确认恢复。</li>
      </ol>
    `,
  },
  {
    id: 'maintenance',
    title: '日常维护与回退',
    body: `
      <dl class="guide-definitions">
        <div><dt>智能 Repair</dt><dd>先检查当前 IP，再为失效域名尝试缓存候选。自动验证分批续跑，保存已验证结果和剩余候选。后台 CFST 生成候选时，域名维护仍可继续。</dd></div>
        <div><dt>单域名维护</dt><dd>用于某一站点异常，或刚修改其路径、凭据、跟随关系时。该操作仅重新处理目标域名及其跟随关系。</dd></div>
        <div><dt>完整优化</dt><dd>重新测速并为域名选择映射，适合首次建立候选或需要全量重选时。耗时通常比 Repair 长，周期性完整优化默认关闭。</dd></div>
        <div><dt>手动强制测速</dt><dd>更新候选池，生成候选后仍需执行域名维护或优化。手动维护可以越过程序内部的等待、暂停与批次限制，真实超时、服务端限流、Hosts 校验和任务互斥仍生效。</dd></div>
        <div><dt>应用 Hosts</dt><dd>将当前映射写入 CFHost 受管区块。程序原地写入、有写前备份和写后校验，其他规则保持原样。</dd></div>
        <div><dt>全部清空</dt><dd>概览快捷操作中可移除全部 CFHost 受管 Hosts 规则，并持久关闭自动应用，保留其他规则、域名配置和候选。检查结果后可手动「应用 Hosts」恢复受管映射。</dd></div>
      </dl>
      <p><strong>可选 GitHub 同步。</strong> 在「设置 → GitHub 同步」填写目标仓库 <code>owner/name</code>、分支和容器内 Token 文件路径。Token 需要能写入目标仓库内容。保存后可从概览执行「同步 GitHub」，并到目标分支核对提交。需要随 Hosts 更新自动同步时，再开启对应开关。</p>
      <p>更新镜像前备份 <code>/data</code> 和现用 Hosts，记下当前镜像标签或摘要。更新时沿用相同的数据目录、网络和挂载配置。更新后核对概览、Hosts 绑定及实际应用连接，出现异常时使用上一个镜像和必要的对应备份回退。</p>
    `,
  },
  {
    id: 'troubleshooting',
    title: '常见故障排查',
    body: `
      <dl class="guide-definitions">
        <div><dt>候选太少或测速遇到 429</dt><dd>在「候选 IP」看可用储备、测速源和下次允许时间。自动测速共享间隔并遵守服务端冷却，已有候选仍可验证。连续点击强制测速会增加请求，建议等冷却结束并用现有候选定位问题。</dd></div>
        <div><dt>HTTP 401、403 或响应不匹配</dt><dd>先核对协议、路径、凭据、权限和返回正文，再区分业务错误、WAF 拦截、站点维护与候选不可达。地址变更无法补齐认证信息，也无法修复远端服务故障。</dd></div>
        <div><dt>任务显示等待续跑或站点暂停</dt><dd>在「任务与历史」看阶段耗时和待续数量。自动验证用完时间片后保存进度，真正的失败按站点退避重试。连续修复失败达到 30 次的域名会暂停自动维护，页面打开时会提示。站点恢复后可在「域名」恢复维护，或手动检查。</dd></div>
        <div><dt>CFHost 更新了 Hosts，其他容器仍看到旧内容</dt><dd>NAS 更新网络配置时可能替换系统 Hosts 文件。旧绑定能继续指向被替换的文件，即使容器配置里的源路径相同。先只读比较设备号和 inode，再按原配置刷新受影响容器的绑定，并实测解析。CFHost 的绑定校验只覆盖它自己的写入目标。</dd></div>
        <div><dt>文件已更新，Tracker 仍标红</dt><dd>核对对应容器看到的 Hosts、实际解析 IP、代理或 Fake-IP 的处理规则，以及最近一次汇报时间和错误。用新的实际请求区分应用缓存、旧挂载和站点问题。</dd></div>
      </dl>
      <pre><code>stat -c '%d:%i links=%h' /etc/hosts
docker exec cfhost stat -c '%d:%i links=%h' /host/etc/hosts /host/etc-check/hosts
docker exec transmission stat -c '%d:%i links=%h' /etc/hosts</code></pre>
      <p>以上命令在 NAS 上只读检查文件身份。将示例容器名换成你的实际名称。若某个消费者的设备号或 inode 不同，或者链接数为 0，先重新建立它对 NAS 当前 <code>/etc/hosts</code> 的只读绑定，再核对应用行为。</p>
    `,
  },
  {
    id: 'privacy',
    title: '隐私与数据处理',
    body: `
      <dl class="guide-definitions">
        <div><dt>NAS 本地保存</dt><dd><code>/data</code> 保存配置、域名和 IP 映射、候选、运行日志、任务历史、样本缓存及 Hosts 备份。下载器账号密码可能在配置中以明文保存，样本 URL 可能含 Tracker passkey。浏览器在本地保存外观偏好。</dd></div>
        <div><dt>探测与下载器通信</dt><dd>测速请求发往所配置的测速服务，HTTP 探测发往目标站点。自定义请求携带为该域名配置的凭据，真实 Tracker 探测会向相应 Tracker 发送样本汇报数据。对端可看到连接来源与请求信息。启用下载器集成后，CFHost 连接对应 RPC 或 WebUI 读取样本和运行状态，符合保活条件时可向 Transmission 发出重新汇报请求。</dd></div>
        <div><dt>可选 GitHub 同步</dt><dd>启用并配置后，程序向你指定的仓库和分支提交 <code>hosts-map.tsv</code> 与 <code>status.json</code>，包含域名、IP、策略、测速指标、状态和时间等信息。Token 用来认证 API 请求，程序不会把 Token、下载器密码或 Tracker 样本作为同步文件写入仓库。请按数据敏感度选择仓库可见性，关闭同步后仍需管理已发布的 Git 历史。</dd></div>
        <div><dt>教程与外部链接</dt><dd>教程资源随容器提供。CFHost WebUI 不包含广告或遥测脚本，教程也不加载 GitHub Star 小组件。点击外部链接后，你的浏览器按目标网站的隐私规则访问该网站。</dd></div>
      </dl>
      <p class="guide-note">管理 UI 和 API 面向可信局域网，程序本身没有登录鉴权和 HTTPS。请用网络访问控制限制可达范围，保护数据目录、备份和凭据挂载。提交 Issue 或分享截图前，请清除账号、Token、passkey、内网地址和其他敏感数据。保留或清理本地数据由部署者负责，清理前先备份仍需使用的配置。</p>
    `,
  },
  {
    id: 'legal',
    title: '法律声明、版权与许可证',
    body: `
      <h3>使用边界与担保</h3>
      <p>请仅在你有权管理的设备上运行 CFHost，并仅对获准访问的站点、API、测速服务和 Tracker 发起请求。使用时须遵守适用法律、服务条款、访问授权及站点限流规则。</p>
      <p>在适用法律允许的范围内，软件按 GPL v3 条款以现状提供，不附带担保。测速、映射或第三方服务的可用性会随网络和服务状态变化。担保与责任限制见 GPL v3 第 15、16 节，完整条款以许可证原文为准。</p>
      <h3>项目与容器镜像的来源</h3>
      <dl class="guide-definitions">
        <div><dt>CFHost / Qnap-CFST</dt><dd>NAS 服务、WebUI 与部署改动由 zyk1172 和项目贡献者维护，相应著作权归各权利人。项目按 GNU General Public License v3.0 发布，允许依照许可证复制、修改和再分发。</dd></div>
        <div><dt>CloudflareSpeedTest</dt><dd>镜像内的 <code>cfst</code> 由本项目中的上游派生测速代码构建。原始测速核心由 XIU2 和上游贡献者开发，上游采用 GPL v3，保留上游作者和许可证声明。</dd></div>
        <div><dt>界面致谢</dt><dd>WebUI 参考 MoviePilot V3 的视觉和交互。MoviePilot-Frontend 版权声明为 © 2023 jxxghp，采用 MIT 许可证，许可声明见第三方 NOTICE。</dd></div>
        <div><dt>基础镜像与其他组件</dt><dd>发行镜像以 Alpine Linux 为基础，含系统软件包、Go 运行时代码及 <code>go.mod</code> 所列依赖。各组件适用各自的许可证。镜像随附的项目、Go 与依赖许可文件位于 <code>/app/licenses</code>。</dd></div>
      </dl>
      <p>再分发镜像或修改版时，请保留适用的版权和许可声明，按 GPL v3 提供相应源码。获取已安装镜像对应的源码时，请按实际使用的发布标签或构建提交查看仓库。Cloudflare、QNAP、Synology、GitHub 等名称用于说明兼容性和服务来源，商标归相应权利人。</p>
      <p class="guide-links">
        ${guideLink(GUIDE_REPOSITORY, 'CFHost 源码与构建文件')}
        ${guideLink(GUIDE_REPOSITORY + '/blob/master/LICENSE', 'GPL v3 许可证全文')}
        ${guideLink(GUIDE_REPOSITORY + '/blob/master/THIRD_PARTY_NOTICES.md', '第三方版权与许可声明')}
        ${guideLink('https://github.com/XIU2/CloudflareSpeedTest', 'CloudflareSpeedTest 上游源码')}
      </p>
    `,
  },
]

function guideLink(url, label, className = '') {
  return `<a class="${className}" href="${url}" target="_blank" rel="noopener noreferrer">${label}</a>`
}

function renderGuide() {
  const chapters = [...GUIDE_CHAPTERS, {
    id: 'support',
    title: '支持 CFHost',
    body: `
      <p>如果 CFHost 对你有帮助，欢迎在 GitHub 上给 <strong>zyk1172/Qnap-CFST</strong> 点一个 Star。打开项目主页，登录后点击页面右上角的 <strong>Star</strong>，让更多 NAS 用户发现这个项目。</p>
      <p>${guideLink(GUIDE_REPOSITORY, icon('github') + '前往 GitHub 点 Star', 'btn')}</p>
    `,
  }]
  return `
    ${pageHeader('使用教程', '从 NAS 部署和首次验证开始，了解日常维护、故障排查及数据处理。')}
    <div class="guide-layout">
      <nav class="card guide-toc" aria-label="教程目录">
        <h2>本页目录</h2>
        <ul>${chapters.map(({ id, title }) => `<li><a href="#/guide/${id}">${title}</a></li>`).join('')}</ul>
      </nav>
      <article class="guide-content" aria-label="CFHost 使用教程">
        ${chapters.map(({ id, title, body }) => `
          <section class="card guide-chapter" id="/guide/${id}" tabindex="-1" aria-labelledby="guide-title-${id}">
            <h2 id="guide-title-${id}">${title}</h2>
            ${body}
          </section>
        `).join('')}
      </article>
    </div>
  `
}

function focusGuideSection() {
  const [route, section] = location.hash.replace(/^#\/?/, '').split('/')
  if (route !== 'guide' || !section) return false
  const element = document.getElementById('/guide/' + section)
  if (!element) return false
  element.focus({ preventScroll: true })
  element.scrollIntoView()
  return true
}
