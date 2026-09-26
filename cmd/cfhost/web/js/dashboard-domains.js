function unresolvedDomainsNow() {
  const domains = (store.config?.domains || []).filter(domain => domain.enabled)
  const mappings = store.state?.mappings || {}
  const statuses = store.state?.domainStatus || {}
  return domains
    .filter(domain => !mappings[domain.host])
    .map(domain => ({ host: domain.host, detail: statuses[domain.host] || '等待验证' }))
}

// A run can report success while individual domains stay unresolved, and each
// run record now carries that. Surface it on the dashboard instead of leaving it
// only in the domain table, because an unresolved domain silently misses its
// Hosts mapping.
function unresolvedNotice() {
  const pending = unresolvedDomainsNow()
  if (!pending.length) return ''
  const shown = pending.slice(0, 4)
  return `
    <div class="alert warning">
      ${icon('warning')}
      <div>
        <strong>${pending.length} 个已启用域名当前没有映射</strong>
        ${shown.map(item => `<span class="alert-line"><code>${esc(item.host)}</code> · ${esc(item.detail)}</span>`).join('')}
        ${pending.length > shown.length ? `<span class="alert-line">另有 ${pending.length - shown.length} 个，详见域名页</span>` : ''}
      </div>
    </div>
  `
}

function renderDashboard() {
  const counts = statusCounts()
  const candidate = candidateStats()
  const history = [...(store.state.history || [])].reverse().slice(0, 5)
  const mapped = Object.keys(store.state.mappings || {}).length
  const actions = `
    <button class="btn secondary" data-job="repair">${icon('repair')}智能 Repair</button>
    <button class="btn" data-job="optimize">${icon('optimize')}完整优化</button>
  `

  return `
    ${pageHeader('概览', '查看 Cloudflare 优选、域名健康、自动修复与同步状态。', actions)}
    ${store.state.lastError ? `
      <div class="alert">${icon('warning')}<div><strong>最近任务失败</strong><span>${esc(store.state.lastError)}</span></div></div>
    ` : ''}
    ${unresolvedNotice()}
    <div class="grid cols-4">
      ${statCard('运行状态', store.state.running ? jobLabel(store.state.currentJob) : '空闲', store.state.running ? '任务执行中' : '等待下一次计划任务', 'activity', store.state.running ? '' : 'success', false)}
      ${statCard('受管域名', counts.total, `${counts.ok} 正常 · ${counts.failed} 待处理`, 'globe', 'info', true)}
      ${statCard('候选 IP', candidate.count, candidate.count ? `最低延迟 ${candidate.bestDelay.toFixed(1)} ms` : '尚未执行测速', 'network', '', true)}
      ${statCard('当前映射', mapped, store.state.lastOptimize ? `最近优化 ${fmtTime(store.state.lastOptimize, true)}` : '尚未完成完整优化', 'host', 'warning', true)}
    </div>

    <div class="grid split-main section">
      <section class="card">
        <div class="card-head">
          <div><h2 class="card-title">域名健康</h2><div class="card-subtitle">当前映射与最近验证状态</div></div>
          <button class="btn secondary small" data-nav="domains">查看全部 ${icon('chevron')}</button>
        </div>
        <div class="card-body">${renderHealthRows()}</div>
      </section>

      <section class="card">
        <div class="card-head"><div><h2 class="card-title">快捷操作</h2><div class="card-subtitle">常用维护操作</div></div></div>
        <div class="card-body">
          <div class="quick-grid">
            ${quickAction('run', '强制测速', '重新生成 CFST 候选', 'bolt')}
            ${quickAction('repair', '智能 Repair', '验证并修复失效映射', 'repair')}
            ${quickAction('optimize', '手动完整优化', '全局重测并重新选择所有域名', 'optimize')}
            ${quickAction('apply', '应用 Hosts', '写入当前已验证映射', 'host')}
            ${quickAction('sync', '同步 GitHub', '发布 hosts-map 与状态', 'sync')}
            ${quickAction('settings', '打开设置', '调整测速与策略', 'settings', true)}
          </div>
        </div>
      </section>
    </div>

    <div class="grid cols-2 section">
      <section class="card">
        <div class="card-head">
          <div><h2 class="card-title">最近任务</h2><div class="card-subtitle">最后 ${history.length} 次运行</div></div>
          <button class="btn secondary small" data-nav="operations">历史记录 ${icon('chevron')}</button>
        </div>
        <div class="card-body">${history.length ? `<div class="timeline">${history.map(historyTimelineItem).join('')}</div>` : emptyState('还没有运行记录')}</div>
      </section>
      <section class="card">
        <div class="card-head"><div><h2 class="card-title">服务与同步</h2><div class="card-subtitle">调度器、Hosts 与 GitHub 状态</div></div></div>
        <div class="card-body metric-stack">
          ${infoLine('自动 Repair', store.config.autoRepair ? `每 ${store.config.repairIntervalMinutes} 分钟` : '关闭', store.config.autoRepair ? 'success' : '')}
          ${infoLine('周期全局优化', store.config.optimize.scheduledFull ? `每 ${store.config.optimize.intervalMinutes} 分钟` : '关闭（Repair 只修坏域名）', store.config.optimize.scheduledFull ? 'warning' : 'success')}
          ${infoLine('Hosts 自动应用', store.config.autoApply ? '启用' : '关闭', store.config.autoApply ? 'success' : '')}
          ${infoLine('GitHub 同步', syncSummary(), store.state.sync?.lastError ? 'danger' : store.state.sync?.lastSuccess ? 'success' : '')}
          ${store.state.migrationStatus ? infoLine('迁移状态', store.state.migrationStatus, 'info') : ''}
        </div>
      </section>
    </div>
  `
}

function statCard(label, value, foot, iconName, variant = '', animate = false) {
  return `
    <section class="card stat-card hoverable">
      <div class="stat-head"><span>${esc(label)}</span><span class="stat-icon ${variant}">${icon(iconName)}</span></div>
      <div class="stat-value" ${animate ? `data-animate-number="${Number(value) || 0}"` : ''}>${esc(value)}</div>
      <div class="stat-foot">${esc(foot)}</div>
    </section>
  `
}

function renderHealthRows() {
  const domains = (store.config.domains || []).filter(domain => domain.enabled)
  if (!domains.length) return emptyState('没有启用的域名')
  const mappings = store.state.mappings || {}
  const statuses = store.state.domainStatus || {}
  return domains.slice(0, 8).map(domain => {
    const ok = !!mappings[domain.host]
    const detail = statuses[domain.host] || (ok ? '映射可用' : '等待验证')
    return `
      <div class="health-row">
        <span class="health-icon" style="color:${ok ? 'var(--success)' : 'var(--warning)'}">${icon(domain.mode === 'tracker' ? 'activity' : 'globe')}</span>
        <div class="health-copy"><strong>${esc(domain.host)}</strong><small>${esc(detail)}</small></div>
        <span class="chip ${ok ? 'success' : 'warning'}"><span class="dot"></span>${ok ? esc(mappings[domain.host]) : '未映射'}</span>
      </div>
    `
  }).join('')
}

function quickAction(action, title, description, iconName, nav = false) {
  return `<button class="quick-action" ${nav ? `data-nav="${action}"` : `data-op="${action}"`}>${icon(iconName)}<strong>${esc(title)}</strong><small>${esc(description)}</small></button>`
}

function infoLine(label, value, variant = '') {
  const iconName = variant === 'danger' ? 'warning' : variant === 'success' ? 'check' : 'info'
  return `<div class="health-row"><span class="health-icon ${variant}">${icon(iconName)}</span><div class="health-copy"><strong>${esc(label)}</strong><small>${esc(value)}</small></div></div>`
}

function syncSummary() {
  const sync = store.state.sync || {}
  if (!store.config.sync.enabled) return '关闭'
  if (sync.lastError) return `失败 · ${sync.lastError}`
  if (sync.lastSuccess) return `最近成功 ${fmtTime(sync.lastSuccess, true)}`
  return '已启用 · 尚未发布'
}

function historyTimelineItem(item) {
  const success = !!item.success
  const unresolved = Number(item.unresolvedCount) || 0
  const names = item.unresolvedDomains || []
  const variant = !success ? 'danger' : unresolved ? 'warning' : 'success'
  const target = item.targetDomain ? `${esc(item.targetDomain)} · ` : ''
  const summary = success
    ? `${target}${item.mappingsBefore} → ${item.mappingsAfter} 映射 · ${item.candidateCount} 候选`
    : `${target}${esc(item.error || '执行失败')}`
  const leftover = success && unresolved
    ? `<small class="timeline-warning">${unresolved} 个域名未解析${names.length ? ` · ${esc(names.join('、'))}` : ''}</small>`
    : ''
  return `
    <div class="timeline-item">
      <span class="timeline-mark ${variant}">${icon(success && !unresolved ? 'check' : 'warning')}</span>
      <div class="timeline-copy"><strong>${esc(jobLabel(item.kind))}</strong><small>${summary}</small>${leftover}</div>
      <span class="timeline-time">${fmtTime(item.startedAt, true)}</span>
    </div>
  `
}

function trackerTorrentStatusLabel(status) {
  return ({
    0: '已停止',
    1: '等待校验',
    2: '校验中',
    3: '等待下载',
    4: '下载中',
    5: '等待做种',
    6: '做种中',
  })[Number(status)] || `未知（${status ?? '—'}）`
}

function trackerAnnounceStateLabel(state) {
  return ({
    0: 'Inactive',
    1: 'Waiting',
    2: 'Queued',
    3: 'Active',
  })[Number(state)] || `Unknown（${state ?? '—'}）`
}

function trackerUnixTime(value) {
  const seconds = Number(value) || 0
  return seconds > 0 ? fmtTime(new Date(seconds * 1000).toISOString()) : '—'
}

function trackerRuntimePanel(domain) {
  if (domain.mode !== 'tracker') return ''
  const entry = store.trackerRuntime?.[domain.host]
  if (!entry || entry.loading) {
    return `
      <div class="tracker-runtime-panel">
        <div class="tracker-runtime-head"><span>Transmission 状态</span><span class="chip info"><span class="dot"></span>读取中</span></div>
        <div class="domain-detail-wide"><span>实时状态</span><strong>正在读取此 Tracker 的 Transmission 汇报状态…</strong></div>
      </div>
    `
  }
  if (entry.error || !entry.data) {
    return `
      <div class="tracker-runtime-panel">
        <div class="tracker-runtime-head"><span>Transmission 状态</span><span class="chip warning"><span class="dot"></span>不可用</span></div>
        <div class="domain-detail-wide"><span>状态读取</span><strong>${esc(entry.error || '没有可用状态')}</strong></div>
      </div>
    `
  }

  const runtime = entry.data
  const trackerStatus = runtime.trackerStatus || 'Waiting'
  const statusLabel = ({
    Working: '正常',
    Connected: '已连接（业务返回）',
    Partial: '部分失败',
    Error: '连接失败',
    Timeout: '超时',
    Waiting: '等待汇报',
    NoActive: '无活跃做种',
  })[trackerStatus] || trackerStatus
  const matched = Number(runtime.matchedTorrents) || 0
  const connected = Number(runtime.connectedTorrents) || 0
  const errors = Number(runtime.connectionFailures) || 0
  const connectedPercent = Number(runtime.connectedPercent) || 0
  const thresholdHealthy = errors <= 5 && connectedPercent >= 90
  const trackerVariant = trackerStatus === 'Working' || trackerStatus === 'Connected'
    ? 'success'
    : trackerStatus === 'Waiting' || trackerStatus === 'NoActive' || (trackerStatus === 'Partial' && thresholdHealthy)
      ? 'warning'
      : 'danger'
  const seeding = Number(runtime.seedingTorrents) || 0
  const queued = Number(runtime.queuedTorrents) || 0
  const issues = Array.isArray(runtime.issues) ? runtime.issues : []
  const latestIssue = issues[0]
  const keepalive = runtime.keepalive || {}
  const keepaliveLabel = ({
    healthy: '健康',
    acceptable: '少量失败',
    waiting: '等待重报',
    observing: '观察中',
    observed: '观察完成',
    'reannounce-due': '准备重报',
    'reannounce-error': '重报失败',
    repair: '需要 Repair',
    idle: '空闲',
  })[keepalive.status] || (keepalive.status || '未触发')
  const rejectedIP = keepalive.rejectedIP || ''
  const repairText = rejectedIP
    ? `${keepaliveLabel} · 排除 ${rejectedIP}`
    : `${keepaliveLabel} · ${Number(keepalive.attempts) || 0}/3`

  return `
    <div class="tracker-runtime-panel">
      <div class="tracker-runtime-head">
        <span>Transmission 状态</span>
        <div class="domain-compact-chips">
          <span class="chip ${trackerVariant}"><span class="dot"></span>${esc(statusLabel)}</span>
          <span class="chip ${errors ? 'danger' : matched ? 'success' : 'warning'}">${errors ? `失败 ${errors}` : `匹配 ${matched}`}</span>
        </div>
      </div>
      <div class="domain-detail-grid tracker-runtime-grid">
        <div class="domain-detail-item"><span>Tracker 连接</span><strong class="${trackerVariant}">匹配 ${matched} · 连接 ${connected} · 失败 ${errors} · ${connectedPercent.toFixed(1)}%</strong></div>
        <div class="domain-detail-item"><span>做种</span><strong>做种 ${seeding} · 等待 ${queued}</strong></div>
        <div class="domain-detail-item"><span>最近 Announce</span><strong>${esc(trackerUnixTime(runtime.lastAnnounceTime))}</strong></div>
        <div class="domain-detail-item"><span>Repair / 保活</span><strong class="${errors ? 'danger' : ''}">${esc(repairText)}</strong></div>
      </div>
      <div class="domain-detail-wide tracker-result ${latestIssue ? 'danger' : trackerVariant}">
        <span>最近 Tracker 返回</span>
        <strong>${esc(latestIssue?.result || runtime.lastAnnounceResult || statusLabel)}</strong>
      </div>
    </div>
  `
}

function httpProbeMeta(domain) {
  if (domain.mode !== 'http') return { available: false, label: '—', variant: '', detail: '' }
  const runtime = store.state?.httpProbe?.[domain.host]
  if (!runtime?.available) return { available: false, label: '未探测', variant: '', detail: '' }
  const code = Number(runtime.statusCode) || 0
  const reachable = !!runtime.reachable
  return {
    available: true,
    reachable,
    code,
    label: code ? `HTTP ${code}` : '无 HTTP 响应',
    variant: reachable ? 'success' : 'danger',
    detail: runtime.detail || '',
  }
}

function httpRuntimePanel(domain) {
  if (domain.mode !== 'http') return ''
  const runtime = store.state?.httpProbe?.[domain.host]
  if (!runtime?.available) {
    return `
      <div class="tracker-runtime-panel">
        <div class="tracker-runtime-head"><span>HTTP 域名状态</span><span class="chip"><span class="dot"></span>未探测</span></div>
        <div class="domain-detail-wide"><span>探测状态</span><strong>尚未执行 HTTP 连通性验证；下一次 Repair、完整优化或单域名维护后会记录返回码。</strong></div>
      </div>
    `
  }

  const currentCode = Number(runtime.statusCode) || 0
  const successCode = Number(runtime.lastSuccessCode) || 0
  const failureCode = Number(runtime.lastFailureCode) || 0
  const reachable = !!runtime.reachable
  const variant = reachable ? 'success' : 'danger'
  const currentLabel = reachable
    ? (currentCode ? `HTTP ${currentCode} · 成功` : '连接成功')
    : (currentCode ? `HTTP ${currentCode} · 失败` : '连接失败 · 无 HTTP 响应')
  const successLabel = runtime.lastSuccessAt
    ? `${successCode ? `HTTP ${successCode}` : '成功'} · ${fmtTime(runtime.lastSuccessAt)}`
    : '—'
  const failureLabel = runtime.lastFailureAt
    ? `${failureCode ? `HTTP ${failureCode}` : '无 HTTP 响应'} · ${fmtTime(runtime.lastFailureAt)}`
    : '—'

  return `
    <div class="tracker-runtime-panel">
      <div class="tracker-runtime-head">
        <span>HTTP 域名状态</span>
        <div class="domain-compact-chips">
          <span class="chip ${variant}"><span class="dot"></span>${esc(currentLabel)}</span>
          <span class="chip ${currentCode ? variant : 'warning'}">${currentCode ? `Code ${currentCode}` : 'No Response'}</span>
        </div>
      </div>
      <div class="domain-detail-grid tracker-runtime-grid">
        <div class="domain-detail-item"><span>当前探测</span><strong class="${variant}">${esc(currentLabel)}</strong></div>
        <div class="domain-detail-item"><span>最近成功</span><strong>${esc(successLabel)}</strong></div>
        <div class="domain-detail-item"><span>最近失败</span><strong>${esc(failureLabel)}</strong></div>
        <div class="domain-detail-item"><span>最近检查</span><strong>${esc(fmtTime(runtime.checkedAt))}</strong></div>
      </div>
      <div class="domain-detail-wide tracker-result ${variant}">
        <span>HTTP 探测详情</span>
        <strong>${esc(runtime.detail || (reachable ? 'HTTP connectivity verified' : 'HTTP connectivity failed'))}</strong>
      </div>
    </div>
  `
}

function filteredDomains() {
  const domains = store.config.domains || []
  const query = store.domainQuery.trim().toLowerCase()
  const filtered = domains
    .map((domain, index) => ({ domain, index }))
    .filter(({ domain }) => {
      if (store.domainFilter === 'tracker' && domain.mode !== 'tracker') return false
      if (store.domainFilter === 'enabled' && !domain.enabled) return false
      if (store.domainFilter === 'failed' && store.state.mappings?.[domain.host]) return false
      if (!query) return true
      return [domain.host, domain.group, domain.class, domain.mode].some(value => String(value || '').toLowerCase().includes(query))
    })
    .sort((a, b) => {
      const trackerOrder = Number(b.domain.mode === 'tracker') - Number(a.domain.mode === 'tracker')
      return trackerOrder || a.index - b.index
    })
    .map(item => item.domain)
  return { domains, filtered }
}

function domainRowsHTML(domains, filtered) {
  const mappings = store.state.mappings || {}
  const statuses = store.state.domainStatus || {}
  const health = store.state.domainHealth || {}
  return filtered.length ? filtered.map(domain => {
    const realIndex = domains.indexOf(domain)
    const ip = mappings[domain.host]
    const domainHealth = health[domain.host] || {}
    const streak = domainHealth.failureStreak || 0
    const sample = trackerSampleMeta(domain)
    const httpProbe = httpProbeMeta(domain)
    const maintaining = !!store.state?.running && store.state.currentJob === 'maintain' && store.state.currentDomain === domain.host
    const expanded = store.expandedDomainHost === domain.host
    const healthLabel = !domain.enabled ? '停用' : ip ? '正常' : '待处理'
    const healthVariant = !domain.enabled ? '' : ip ? 'success' : 'warning'
    const modeLabel = domain.mode === 'tracker' ? 'TRK' : 'HTTP'
    const classLabel = domain.class === 'bandwidth' ? 'BW' : domain.class === 'normal' ? '普通' : 'LAT'
    const classVariant = domain.class === 'bandwidth' ? 'success' : domain.class === 'normal' ? 'warning' : 'info'
    const sampleCompact = domain.mode === 'tracker'
      ? `<span class="chip compact ${sample.variant}"><span class="dot"></span>${esc(sample.label)}</span>`
      : httpProbe.available
        ? `<span class="chip compact ${httpProbe.variant}"><span class="dot"></span>${esc(httpProbe.label)}</span>`
        : ''
    const detailItems = [
      ['配置', `${domain.group || '—'} · ${domain.endpoint || '/'} · ${domain.mode.toUpperCase()} / ${domain.class}`],
      ['当前 IP', ip || '—'],
      ['CFHost 探测', domain.mode === 'tracker' ? sample.label : httpProbe.label],
      ['健康记录', `连续失败 ${streak} · 最近成功 ${domainHealth.lastSuccess ? fmtTime(domainHealth.lastSuccess) : '—'}`],
    ]

    return `
      <tr class="domain-main-row ${expanded ? 'is-expanded' : ''}">
        <td class="domain-primary-cell">
          <button class="domain-expand-button" data-domain-expand="${realIndex}" aria-expanded="${expanded ? 'true' : 'false'}" title="${expanded ? '收起详情' : '展开详情'}">
            <span class="domain-host-text">${esc(domain.host)}</span>
            ${icon('chevron', 'domain-expand-chevron')}
          </button>
        </td>
        <td class="domain-meta-cell">
          <div class="domain-compact-chips">
            <span class="chip compact ${domain.mode === 'tracker' ? 'primary' : ''}">${modeLabel}</span>
            <span class="chip compact ${classVariant}">${classLabel}</span>
          </div>
        </td>
        <td class="domain-ip-cell mono"><span class="domain-ip-text" title="${esc(ip || '未映射')}">${ip ? esc(ip) : '—'}</span></td>
        <td class="domain-status-cell">
          <div class="domain-status-compact">
            <span class="chip compact ${healthVariant}"><span class="dot"></span>${healthLabel}</span>
            ${sampleCompact}
          </div>
        </td>
        <td class="domain-actions-cell">
          <div class="table-actions domain-actions">
            <button class="icon-button domain-action maintain" data-maintain-domain="${realIndex}" aria-label="维护 ${esc(domain.host)}" title="只维护这个域名" ${!domain.enabled || store.state?.running ? 'disabled' : ''}>${maintaining ? icon('activity') : icon('repair')}</button>
            <button class="icon-button domain-action edit" data-edit-domain="${realIndex}" aria-label="编辑 ${esc(domain.host)}" title="编辑">${icon('edit')}</button>
            <button class="icon-button domain-action toggle" data-toggle-domain="${realIndex}" aria-label="${domain.enabled ? '停用' : '启用'} ${esc(domain.host)}" title="${domain.enabled ? '停用' : '启用'}">${icon(domain.enabled ? 'pause' : 'play')}</button>
            <button class="icon-button domain-action delete" data-delete-domain="${realIndex}" aria-label="删除 ${esc(domain.host)}" title="删除">${icon('trash')}</button>
          </div>
        </td>
      </tr>
      ${expanded ? `
        <tr class="domain-detail-row">
          <td colspan="5">
            <div class="domain-detail-panel">
              <div class="domain-detail-grid">
                ${detailItems.map(([label, value]) => `<div class="domain-detail-item"><span>${esc(label)}</span><strong class="${label === '当前 IP' ? 'mono' : ''}">${esc(value)}</strong></div>`).join('')}
              </div>
              <div class="domain-detail-wide">
                <span>CFHost 探测详情</span>
                <strong>${esc(domain.mode === 'tracker' ? (sample.detail || sample.label) : (httpProbe.detail || '尚未执行 HTTP 探测'))}</strong>
              </div>
              ${httpRuntimePanel(domain)}
              ${trackerRuntimePanel(domain)}
            </div>
          </td>
        </tr>
      ` : ''}
    `
  }).join('') : '<tr><td colspan="5" class="table-empty">没有符合条件的域名</td></tr>'
}

// Patch only the result region. Re-rendering the whole page would replace the
// focused search input and lose keystrokes typed while it is detached.
function updateDomainResults() {
  const { domains, filtered } = filteredDomains()
  const rows = $('#domain-rows')
  if (rows) rows.innerHTML = domainRowsHTML(domains, filtered)
  const count = $('#domain-count')
  if (count) count.textContent = `${filtered.length} / ${domains.length}`
}

function renderDomains() {
  const { domains, filtered } = filteredDomains()

  return `
    ${pageHeader('域名', '管理需要 Cloudflare 优选的站点、Tracker、策略类别与共享分组。', `<button class="btn" data-action="add-domain">${icon('plus')}添加域名</button>`)}
    <section class="card">
      <div class="card-body">
        <div class="toolbar">
          <div class="search-field">${icon('search')}<input id="domain-search" type="search" value="${esc(store.domainQuery)}" placeholder="搜索域名或分组"></div>
          <div class="segmented">
            <button data-domain-filter="all" class="${store.domainFilter === 'all' ? 'is-active' : ''}">全部</button>
            <button data-domain-filter="tracker" class="${store.domainFilter === 'tracker' ? 'is-active' : ''}">Tracker</button>
            <button data-domain-filter="enabled" class="${store.domainFilter === 'enabled' ? 'is-active' : ''}">已启用</button>
            <button data-domain-filter="failed" class="${store.domainFilter === 'failed' ? 'is-active' : ''}">待处理</button>
          </div>
          <span class="toolbar-spacer"></span>
          <span id="domain-count" class="chip info">${filtered.length} / ${domains.length}</span>
        </div>
        <div class="table-wrap domain-table-wrap">
          <table class="data-table domain-table">
            <colgroup>
              <col class="domain-col-host">
              <col class="domain-col-meta">
              <col class="domain-col-ip">
              <col class="domain-col-status">
              <col class="domain-col-actions">
            </colgroup>
            <thead><tr><th>域名</th><th>类型 / 策略</th><th>当前 IP</th><th>状态</th><th></th></tr></thead>
            <tbody id="domain-rows">
              ${domainRowsHTML(domains, filtered)}
            </tbody>
          </table>
        </div>
      </div>
    </section>
  `
}
