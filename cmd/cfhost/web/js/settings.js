function renderSettings() {
  if (!store.configDraft) store.configDraft = deepClone(store.config)
  const config = store.configDraft
  return `
    ${pageHeader('设置', '按功能分区调整 CFST、验证、Repair、Hosts、Tracker 与 GitHub 同步。')}
    <div class="settings-layout">
      <aside class="card settings-nav">
        ${settingsNavButton('speed', '测速', 'bolt', true)}
        ${settingsNavButton('verify', '验证策略', 'shield')}
        ${settingsNavButton('repair', 'Repair 与优化', 'repair')}
        ${settingsNavButton('hosts', 'Hosts 与 Tracker', 'host')}
        ${settingsNavButton('sync', 'GitHub 同步', 'github')}
      </aside>
      <div>
        ${settingsSection('speed', 'CFST 测速', '控制 CloudflareSpeedTest 的候选生成、延迟和下载测速参数。', `
          <div class="form-grid">
            ${numberField('cfst.maxDelayMs', '延迟上限', config.cfst.maxDelayMs, 'ms')}
            ${numberField('cfst.maxLossRate', '最大丢包率', config.cfst.maxLossRate, '0 ~ 1', '0.01')}
            ${numberField('cfst.minSpeedMB', '最低下载速度', config.cfst.minSpeedMB, 'MB/s', '0.1')}
            ${numberField('cfst.threads', '测速线程', config.cfst.threads)}
            ${numberField('cfst.pingTimes', 'Ping 次数', config.cfst.pingTimes)}
            ${numberField('cfst.downloadCount', '下载测速数量', config.cfst.downloadCount)}
            ${numberField('cfst.downloadSeconds', '单 IP 下载时长', config.cfst.downloadSeconds, '秒')}
            ${numberField('cfst.runTimeoutMinutes', '完整任务超时', config.cfst.runTimeoutMinutes, '分钟')}
            ${numberField('cfst.degradedRateMbps', '限流期下载上限', config.cfst.degradedRateMbps, 'Mbps', '0.5')}
            ${numberField('cfst.degradedDownloadCount', '限流期测速数量', config.cfst.degradedDownloadCount, '个')}
            ${numberField('cfst.degradedDownloadSeconds', '限流期单 IP 时长', config.cfst.degradedDownloadSeconds, '秒')}
            ${numberField('cfst.rateLimitFallbackMinutes', '无 Retry-After 时冷却', config.cfst.rateLimitFallbackMinutes, '分钟')}
            ${textField('cfst.downloadUrl', '下载测速 URL', config.cfst.downloadUrl, '', 'span-2')}
          </div>
          ${switchRow('cfst.ipv6', 'IPv6 IP 池', '开启后 CFST 使用 ipv6.txt 候选池。', config.cfst.ipv6)}
          ${switchRow('cfst.adaptiveRateLimit', '测速地址限流自适应', '检测到 429 或带 Retry-After 的 503 后记录解封时间；解封前自动使用低流量降级测速，避免 Repair 反复触发完整测速。', config.cfst.adaptiveRateLimit)}
        `)}

        ${settingsSection('verify', '域名验证', '决定候选 IP 是否真正适用于目标站点，严格 HTTP 会识别挑战页和占位页。', `
          <div class="form-grid">
            ${numberField('verifyTimeoutSeconds', '验证超时', config.verifyTimeoutSeconds, '秒')}
            ${numberField('verify.httpRetries', 'HTTP 重试次数', config.verify.httpRetries)}
            ${numberField('verify.candidateLimit', '每域名最多候选', config.verify.candidateLimit)}
            ${numberField('verify.minBodyBytes', 'HTTP 200 最小正文', config.verify.minBodyBytes, 'bytes')}
            ${numberField('verify.maxRedirects', '最大跳转次数', config.verify.maxRedirects)}
            ${textField('verify.blockPatterns', '挑战页 / 占位页规则', config.verify.blockPatterns, '正则表达式', 'span-2')}
          </div>
          ${switchRow('verify.strictHttp', '严格 HTTP 验证', '要求最终 2xx，并检查正文大小与挑战页特征。', config.verify.strictHttp)}
          <div class="alert info" style="margin-top:14px">
            ${icon('info')}
            <div>
              <strong>域名策略类别</strong>
              <span>latency：验证域名后按低延迟优先；bandwidth：验证域名后按高吞吐优先；normal：不做 HTTP/Tracker 有效性验证，直接使用 CFST 已筛选候选中延迟最低的 IP。</span>
            </div>
          </div>
        `)}

        ${settingsSection('repair', 'Repair 与完整优化', '自动维护默认只修复失效域名；Full Optimize 是显式的全局重新选择操作。', `
          <div class="form-grid">
            ${numberField('repairIntervalMinutes', 'Repair 间隔', config.repairIntervalMinutes, '分钟')}
            ${numberField('repair.candidateTTLMinutes', '候选缓存 TTL', config.repair.candidateTTLMinutes, '分钟')}
            ${numberField('repair.failureThreshold', '完整测速失败阈值', config.repair.failureThreshold, '次')}
            ${numberField('repair.refreshCooldownMinutes', 'Refresh 基础冷却', config.repair.refreshCooldownMinutes, '分钟')}
            ${numberField('repair.refreshMaxBackoffMinutes', 'Refresh 最大退避', config.repair.refreshMaxBackoffMinutes, '分钟')}
            ${numberField('optimize.intervalMinutes', '完整优化周期', config.optimize.intervalMinutes, '分钟')}
            ${numberField('optimize.retryMinutes', '优化失败重试', config.optimize.retryMinutes, '分钟')}
            ${numberField('bandwidth.maxDelayMs', 'Bandwidth 最大延迟', config.bandwidth.maxDelayMs, 'ms')}
            ${numberField('bandwidth.maxLossRate', 'Bandwidth 最大丢包率', config.bandwidth.maxLossRate, '0 ~ 1', '0.01')}
            ${numberField('bandwidth.minSpeedMB', 'Bandwidth 最低速度', config.bandwidth.minSpeedMB, 'MB/s', '0.1')}
          </div>
          ${switchRow('autoRepair', '自动 Smart Repair', '按 Repair 间隔检查当前映射；正常域名保持原 IP，仅修复失效域名。', config.autoRepair)}
          ${switchRow('optimize.scheduledFull', '允许周期性全局优化', '开启后才会按完整优化周期重新选择所有域名。默认关闭；手动“完整优化”不受此开关影响。', config.optimize.scheduledFull)}
          <div class="alert info" style="margin-top:14px">
            ${icon('info')}
            <div>
              <strong>自动维护采用逐域名修复</strong>
              <span>某个域名失效时，只为该域名寻找替代 IP。健康域名不会因为其他域名故障而更换当前映射。</span>
            </div>
          </div>
        `)}

        ${settingsSection('hosts', 'Hosts 与 Tracker', '控制宿主机 Hosts、真实 Tracker announce，以及从 Transmission / qBittorrent 自动发现测试种子。', `
          <div class="form-grid">
            ${textField('hostsPath', 'Hosts 路径', config.hostsPath)}
            ${numberField('hosts.backupRetention', 'Hosts 备份保留', config.hosts.backupRetention, '份')}
            ${textField('hosts.legacyStateMapPath', '旧 hosts-map.tsv 导入路径', config.hosts.legacyStateMapPath, '', 'span-2')}
            ${textField('tracker.samplesPath', '手工 Tracker 样本文件', config.tracker.samplesPath, '', 'span-2')}
            ${numberField('tracker.retries', 'Tracker announce 重试', config.tracker.retries)}
            ${numberField('tracker.announcePort', 'Tracker announce 端口', config.tracker.announcePort)}
            ${textField('tracker.userAgent', 'Tracker User-Agent', config.tracker.userAgent)}
            ${textField('tracker.peerIdPrefix', 'Peer ID 前缀', config.tracker.peerIdPrefix)}
          </div>
          ${switchRow('autoApply', 'Repair 后自动应用 Hosts', '成功解析后自动写入宿主机 Hosts Marker。', config.autoApply)}
          ${switchRow('tracker.realAnnounce', 'Tracker 真实 announce', '通过候选 IP 连接 Tracker。收到 HTTP 200 + 合法 bencode 后，一般 Tracker 业务错误仍算候选可达；但 Could not connect to tracker / Failed to connect to tracker 等明确上游连接失败的错误仍判失败并继续换候选。', config.tracker.realAnnounce)}
          ${switchRow('tracker.autoDiscover', '自动从下载器发现 Tracker 测试种子', 'Repair / Full Optimize 前自动读取已完成种子；不会暂停、重启、reannounce 或修改下载器任务。', config.tracker.autoDiscover)}

          <div class="alert info" style="margin-top:14px">
            ${icon('info')}
            <div>
              <strong>下载器地址要从 CFHost 容器内可访问</strong>
              <span>如果 Transmission / qBittorrent 运行在同一台 QNAP 上，bridge 模式下通常填写 NAS 的局域网 IP，不要填写 127.0.0.1。自动发现只选已完成任务，并只读取 hash 与 Tracker URL。</span>
            </div>
          </div>

          <div class="form-grid">
            ${textField('tracker.autoSamplesPath', '自动样本缓存', config.tracker.autoSamplesPath, '', 'span-2')}
            ${numberField('tracker.discoveryTimeoutSeconds', '自动发现超时', config.tracker.discoveryTimeoutSeconds, '秒')}
            ${numberField('tracker.maxTrackerLookups', '下载器 Tracker 补查上限', config.tracker.maxTrackerLookups, '个完成任务（每个 Tracker 找到 1 个样本即停止）')}
          </div>

          <div class="grid cols-2 section">
            <div class="card" style="box-shadow:none">
              <div class="card-head"><div><h3 class="card-title">Transmission</h3><div class="card-subtitle">读取 torrent-get：hashString / leftUntilDone / trackers</div></div></div>
              <div class="card-body">
                ${switchRow('tracker.transmission.enabled', '启用 Transmission', '从 Transmission RPC 自动寻找目标 Tracker 的已完成种子。', config.tracker.transmission.enabled)}
                <div class="form-grid">
                  ${textField('tracker.transmission.url', 'RPC 地址', config.tracker.transmission.url, 'http://192.168.1.10:9091/transmission/rpc', 'span-2')}
                  ${textField('tracker.transmission.username', '用户名', config.tracker.transmission.username)}
                  ${passwordField('tracker.transmission.password', '密码', config.tracker.transmission.password)}
                </div>
              </div>
            </div>

            <div class="card" style="box-shadow:none">
              <div class="card-head"><div><h3 class="card-title">qBittorrent</h3><div class="card-subtitle">读取 completed torrents，并在需要时查询 trackers</div></div></div>
              <div class="card-body">
                ${switchRow('tracker.qbittorrent.enabled', '启用 qBittorrent', '通过 qB WebUI API 自动寻找目标 Tracker 的已完成种子。', config.tracker.qbittorrent.enabled)}
                <div class="form-grid">
                  ${textField('tracker.qbittorrent.url', 'WebUI 地址', config.tracker.qbittorrent.url, 'http://192.168.1.10:8080', 'span-2')}
                  ${textField('tracker.qbittorrent.username', '用户名', config.tracker.qbittorrent.username)}
                  ${passwordField('tracker.qbittorrent.password', '密码', config.tracker.qbittorrent.password)}
                </div>
              </div>
            </div>
          </div>

          <div class="page-actions" style="margin-top:14px">
            <button class="btn secondary small" data-action="discover-trackers">${icon('search')}立即从下载器发现</button>
          </div>
        `)}

        ${settingsSection('sync', 'GitHub 同步', '将映射和状态以一个原子 commit 发布给其他设备。', `
          <div class="form-grid">
            ${textField('sync.repository', '仓库', config.sync.repository)}
            ${textField('sync.branch', '分支', config.sync.branch)}
            ${textField('sync.tokenFile', 'Token 文件', config.sync.tokenFile, '', 'span-2')}
            ${textField('sync.commitMessage', 'Commit 信息', config.sync.commitMessage, '', 'span-2')}
          </div>
          ${switchRow('sync.enabled', '自动 GitHub 同步', 'Hosts 自动应用成功后，映射有变化时自动发布。', config.sync.enabled)}
        `)}

        <div id="save-bar" class="save-bar">
          <span>有未保存的设置更改</span>
          <div class="page-actions">
            <button class="btn secondary small" data-action="discard-settings">放弃</button>
            <button class="btn small" data-action="save-settings">${icon('check')}保存设置</button>
          </div>
        </div>
      </div>
    </div>
  `
}

function settingsNavButton(id, label, iconName, active = false) {
  return `<button data-settings-nav="${id}" class="${active ? 'is-active' : ''}">${icon(iconName)}<span>${esc(label)}</span></button>`
}

function settingsSection(id, title, description, content) {
  return `
    <section id="settings-${id}" class="card settings-section">
      <div class="card-head"><div><h2 class="settings-section-title">${esc(title)}</h2><p class="settings-section-desc">${esc(description)}</p></div></div>
      <div class="card-body">${content}</div>
    </section>
  `
}

function numberField(path, label, value, hint = '', step = '1') {
  return `<div class="field"><label>${esc(label)}</label><input type="number" step="${step}" data-config-path="${path}" data-config-type="number" value="${esc(value)}">${hint ? `<span class="hint">${esc(hint)}</span>` : ''}</div>`
}

function textField(path, label, value, placeholder = '', className = '') {
  return `<div class="field ${className}"><label>${esc(label)}</label><input type="text" data-config-path="${path}" value="${esc(value)}" placeholder="${esc(placeholder)}"></div>`
}

function passwordField(path, label, value, placeholder = '') {
  return `<div class="field"><label>${esc(label)}</label><input type="password" autocomplete="new-password" data-config-path="${path}" value="${esc(value)}" placeholder="${esc(placeholder)}"></div>`
}

function switchRow(path, title, description, checked) {
  return `
    <div class="switch-row">
      <div class="switch-copy"><strong>${esc(title)}</strong><small>${esc(description)}</small></div>
      <label class="switch"><input type="checkbox" data-config-path="${path}" data-config-type="bool" ${checked ? 'checked' : ''}><span></span></label>
    </div>
  `
}

function emptyState(text) {
  return `<div class="empty-state">${esc(text)}</div>`
}

function jobLabel(kind) {
  return ({ run: 'CFST 测速', repair: '智能 Repair', optimize: '完整优化', maintain: '单域名维护' })[kind] || kind || '任务'
}

function hydrateIcons(root = document) {
  root.querySelectorAll('[data-icon]').forEach(element => { element.innerHTML = icon(element.dataset.icon) })
}

function animateNumbers() {
  if (matchMedia('(prefers-reduced-motion: reduce)').matches) return
  document.querySelectorAll('[data-animate-number]').forEach(element => {
    const target = Number(element.dataset.animateNumber) || 0
    const started = performance.now()
    const duration = 420
    const tick = now => {
      const progress = Math.min((now - started) / duration, 1)
      const eased = 1 - Math.pow(1 - progress, 4)
      element.textContent = Math.round(target * eased).toLocaleString()
      if (progress < 1) requestAnimationFrame(tick)
    }
    requestAnimationFrame(tick)
  })
}
