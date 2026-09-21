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
  const summary = success
    ? `${item.mappingsBefore} → ${item.mappingsAfter} 映射 · ${item.candidateCount} 候选`
    : esc(item.error || '执行失败')
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

function filteredDomains() {
  const domains = store.config.domains || []
  const query = store.domainQuery.trim().toLowerCase()
  const filtered = domains.filter(domain => {
    if (store.domainFilter === 'enabled' && !domain.enabled) return false
    if (store.domainFilter === 'failed' && store.state.mappings?.[domain.host]) return false
    if (!query) return true
    return [domain.host, domain.group, domain.class, domain.mode].some(value => String(value || '').toLowerCase().includes(query))
  })
  return { domains, filtered }
}

function domainRowsHTML(domains, filtered) {
  const mappings = store.state.mappings || {}
  const statuses = store.state.domainStatus || {}
  const health = store.state.domainHealth || {}
  return filtered.length ? filtered.map(domain => {
    const realIndex = domains.indexOf(domain)
    const ip = mappings[domain.host]
    const streak = health[domain.host]?.failureStreak || 0
    return `
      <tr>
        <td><strong>${esc(domain.host)}</strong><div class="card-subtitle">${esc(domain.endpoint || '/')}</div></td>
        <td><span class="chip ${domain.mode === 'tracker' ? 'primary' : ''}">${esc(domain.mode.toUpperCase())}</span></td>
        <td><span class="chip ${domain.class === 'bandwidth' ? 'success' : domain.class === 'normal' ? 'warning' : 'info'}">${esc(domain.class)}</span></td>
        <td>${domain.group ? `<span class="chip">${esc(domain.group)}</span>` : '—'}</td>
        <td class="mono">${ip ? esc(ip) : '—'}</td>
        <td><span class="chip ${!domain.enabled ? '' : ip ? 'success' : 'warning'}"><span class="dot"></span>${!domain.enabled ? '停用' : ip ? '正常' : '待处理'}</span><div class="card-subtitle">${esc(statuses[domain.host] || '')}</div></td>
        <td>${streak ? `<span class="chip danger">${streak}</span>` : '<span class="chip">0</span>'}</td>
        <td><div class="table-actions">
          <button class="icon-button" data-edit-domain="${realIndex}" aria-label="编辑">${icon('edit')}</button>
          <button class="icon-button" data-toggle-domain="${realIndex}" aria-label="${domain.enabled ? '停用' : '启用'}">${icon(domain.enabled ? 'pause' : 'play')}</button>
          <button class="icon-button" data-delete-domain="${realIndex}" aria-label="删除">${icon('trash')}</button>
        </div></td>
      </tr>
    `
  }).join('') : '<tr><td colspan="8" class="table-empty">没有符合条件的域名</td></tr>'
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
            <button data-domain-filter="enabled" class="${store.domainFilter === 'enabled' ? 'is-active' : ''}">已启用</button>
            <button data-domain-filter="failed" class="${store.domainFilter === 'failed' ? 'is-active' : ''}">待处理</button>
          </div>
          <span class="toolbar-spacer"></span>
          <span id="domain-count" class="chip info">${filtered.length} / ${domains.length}</span>
        </div>
        <div class="table-wrap">
          <table class="data-table">
            <thead><tr><th>域名</th><th>类型</th><th>策略</th><th>站点组</th><th>当前 IP</th><th>健康</th><th>连续失败</th><th></th></tr></thead>
            <tbody id="domain-rows">
              ${domainRowsHTML(domains, filtered)}
            </tbody>
          </table>
        </div>
      </div>
    </section>
  `
}
