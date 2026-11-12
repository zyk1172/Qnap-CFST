function cfstCooldownMeta() {
  const state = store.state.cfstRateLimit || {}
  const sources = Object.values(store.state.cfstSources || {})
  const blockedCount = sources.filter(source => source.rateLimit?.active && Date.parse(source.rateLimit.until) > Date.now()).length
  const blocked = sources.length ? blockedCount === sources.length : !!state.active && Date.parse(state.until) > Date.now()
  const recovered = !!state.detectedAt && !blocked && state.mode !== 'normal'
  const next = store.state.cfstSchedule?.nextAllowedAt
  const waiting = Date.parse(next) > Date.now()
  return { ...state, active: blocked, label: blocked ? '冷却中' : blockedCount ? '部分源冷却' : waiting ? '等待间隔' : recovered ? '待恢复验证' : '可申请', detail: blocked ? `全部源冷却，最早可申请 ${fmtTime(next || state.until)}` : blockedCount ? `${blockedCount}/${sources.length} 个源限流；最早测速 ${fmtTime(next)}` : waiting ? `最早可申请 ${fmtTime(next)}` : recovered ? '冷却已结束，等待小规模恢复探测' : '仍受全局测速间隔限制' }
}

function renderCandidates() {
  const list = [...(store.state.candidates || [])]
  const rateLimit = cfstCooldownMeta()
  const by = store.candidateSort
  list.sort((a, b) => {
    if (by === 'speed') return (Number(b.speedMB) || 0) - (Number(a.speedMB) || 0) || (Number(a.delayMs) || 0) - (Number(b.delayMs) || 0)
    if (by === 'loss') return (Number(a.lossRate) || 0) - (Number(b.lossRate) || 0) || (Number(a.delayMs) || 0) - (Number(b.delayMs) || 0)
    return (Number(a.delayMs) || 0) - (Number(b.delayMs) || 0) || (Number(b.speedMB) || 0) - (Number(a.speedMB) || 0)
  })
  const maxSpeed = Math.max(...list.map(candidate => Number(candidate.speedMB) || 0), 1)
  const maxDelay = Math.max(...list.map(candidate => Number(candidate.delayMs) || 0), 1)
  const best = list[0]

  return `
    ${pageHeader('候选 IP', '查看 CFST 测速结果、延迟、丢包和下载速度，并手动触发新一轮测速。', `<button class="btn" data-job="run">${icon('bolt')}申请 CFST 测速</button>`)}
    <div class="grid cols-4">
      ${statCard('候选数量', list.length, store.config.cfst.ipv6 ? '当前 IPv6 池' : '当前 IPv4 池', 'network', 'info', true)}
      ${statCard('最低延迟', list.length ? Math.min(...list.map(candidate => Number(candidate.delayMs) || Infinity)).toFixed(1) : '—', list.length ? 'ms' : '尚无数据', 'activity', 'success', false)}
      ${statCard('最高速度', list.length ? Math.max(...list.map(candidate => Number(candidate.speedMB) || 0)).toFixed(2) : '—', list.length ? 'MB/s' : '尚无数据', 'bolt', 'warning', false)}
      ${statCard('测速模式', rateLimit.label, rateLimit.detail, rateLimit.active ? 'repair' : 'check', rateLimit.active ? 'warning' : 'success', false)}
    </div>
    <div class="grid cols-2 section">
      <section class="card">
        <div class="card-head"><div><h2 class="card-title">候选质量</h2><div class="card-subtitle">按延迟与速度对比 Top 8</div></div></div>
        <div class="card-body metric-stack">
          ${list.length ? list.slice(0, 8).map(candidate => `
            <div class="metric-line">
              <div class="metric-line-label"><strong>${esc(candidate.ip)}</strong><small>${esc(candidate.colo || '—')}</small></div>
              <div>
                <div class="metric-bar"><i style="--value:${Math.max(4, 100 - (Number(candidate.delayMs) || 0) / maxDelay * 100)}%"></i></div>
                <div class="metric-bar speed" style="margin-top:5px"><i style="--value:${(Number(candidate.speedMB) || 0) / maxSpeed * 100}%"></i></div>
              </div>
              <div class="metric-line-value">${Number(candidate.delayMs || 0).toFixed(1)} ms<br>${candidate.latencyOnly ? '未测速' : `${Number(candidate.speedMB || 0).toFixed(2)} MB/s`}</div>
            </div>
          `).join('') : emptyState('先执行一次 CFST 测速')}
        </div>
      </section>
      <section class="card">
        <div class="card-head"><div><h2 class="card-title">当前测速配置</h2><div class="card-subtitle">完整测速会使用这些参数</div></div><button class="btn secondary small" data-nav="settings">设置 ${icon('chevron')}</button></div>
        <div class="card-body metric-stack">
          ${infoLine('延迟上限', `${store.config.cfst.maxDelayMs} ms`)}
          ${infoLine('最大丢包率', String(store.config.cfst.maxLossRate))}
          ${infoLine('最低下载速度', `${store.config.cfst.minSpeedMB} MB/s`)}
          ${infoLine('下载测速数量', String(store.config.cfst.downloadCount))}
          ${infoLine('冷却后恢复探测参数', `${store.config.cfst.degradedDownloadMB} MB/次 · ${store.config.cfst.degradedDownloadCount} 个 · ${store.config.cfst.degradedDownloadSeconds} 秒`)}
          ${infoLine('当前限流状态', rateLimit.detail, rateLimit.active ? 'warning' : 'success')}
          ${infoLine('最近测速尝试', fmtTime(store.state.lastRefresh))}
          ${candidateReserveInfo()}
          ${infoLine('下次允许测速', fmtTime(store.state.cfstSchedule?.nextAllowedAt))}
          ${infoLine('最近触发入口', store.state.cfstSchedule?.trigger || '—')}
          ${infoLine('最近测速源', store.state.cfstSchedule?.source || '—')}
          ${Object.values(store.state.cfstSources || {}).map(source => infoLine(source.origin, source.rateLimit?.active ? `冷却至 ${fmtTime(source.rateLimit.until)}` : source.lastError ? `最近未取得带宽结果 · 最早重试 ${fmtTime(source.retryAt)}` : '可参与轮换')).join('')}
          ${infoLine('最近触发原因', store.state.cfstSchedule?.reason || '—')}
          ${infoLine('最近限流', fmtTime(rateLimit.detectedAt))}
          ${best ? infoLine('当前排序第一', `${best.ip} · ${Number(best.delayMs || 0).toFixed(1)} ms`, 'success') : ''}
        </div>
      </section>
    </div>
    <section class="card section">
      <div class="card-head">
        <div><h2 class="card-title">全部候选</h2><div class="card-subtitle">过期记录不会直接参与选择；未测带宽的候选仅供需要域名验证的延迟策略使用</div></div>
        <div class="segmented">
          <button data-candidate-sort="latency" class="${by === 'latency' ? 'is-active' : ''}">延迟</button>
          <button data-candidate-sort="speed" class="${by === 'speed' ? 'is-active' : ''}">速度</button>
          <button data-candidate-sort="loss" class="${by === 'loss' ? 'is-active' : ''}">丢包</button>
        </div>
      </div>
      <div class="card-body">
        <div class="table-wrap">
          <table class="data-table"><thead><tr><th>#</th><th>IP</th><th>丢包率</th><th>延迟</th><th>下载速度</th><th>地区</th><th>测速源</th><th>采样时间</th></tr></thead>
          <tbody>${list.length ? list.map((candidate, index) => `
            <tr><td>${index + 1}</td><td class="mono"><strong>${esc(candidate.ip)}</strong></td><td>${Number(candidate.lossRate || 0).toFixed(3)}</td><td>${Number(candidate.delayMs || 0).toFixed(1)} ms</td><td>${candidate.latencyOnly ? '未测速 · 需域名验证' : `<strong>${Number(candidate.speedMB || 0).toFixed(2)}</strong> MB/s`}</td><td>${esc(candidate.colo || '—')}</td><td>${esc(candidate.source || '历史记录')}</td><td>${fmtTime(candidate.observedAt)}${!candidate.observedAt || Date.now() - Date.parse(candidate.observedAt) > store.config.repair.candidateTTLMinutes * 60000 ? ' · 已过期' : ''}</td></tr>
          `).join('') : '<tr><td colspan="8" class="table-empty">暂无候选数据</td></tr>'}</tbody></table>
        </div>
      </div>
    </section>
  `
}

function candidateReserveInfo() {
  const reserve = store.state.candidateReserve
  if (!reserve) return ''
  const label = { disabled: '自动补充关闭', sufficient: '储备充足', waiting: '等待补充间隔或源冷却', ready: '等待空闲时补充' }[reserve.status] || reserve.status
  return infoLine('有效候选储备', `${reserve.usableCount}/${reserve.minimumCount} · ${label}`) +
    (reserve.nextAllowedAt ? infoLine('最早补充时间', fmtTime(reserve.nextAllowedAt)) : '')
}

function renderOperations() {
  const history = [...(store.state.history || [])].reverse()
  const sync = store.state.sync || {}
  return `
    ${pageHeader('任务与历史', '手动运行维护任务，查看调度、同步和最近执行记录。')}
    <div class="grid cols-4">
      ${operationCard('repair', '智能 Repair', '验证当前映射，仅在需要时使用候选或完整测速。', 'repair')}
      ${operationCard('optimize', '手动完整优化', '遵守测速冷却，重新测速并选择各域名的可用 IP。', 'optimize')}
      ${operationCard('apply', '应用 Hosts', '将当前已验证映射写入宿主机受管 Marker。', 'host')}
      ${operationCard('sync', '同步 GitHub', '原子发布 hosts-map.tsv 与 status.json。', 'sync')}
    </div>
    <div class="grid cols-2 section">
      <section class="card">
        <div class="card-head"><div><h2 class="card-title">调度器</h2><div class="card-subtitle">自动任务计划</div></div></div>
        <div class="card-body metric-stack">
          ${infoLine('Smart Repair', store.config.autoRepair ? `启用 · ${store.config.repairIntervalMinutes} 分钟` : '关闭', store.config.autoRepair ? 'success' : '')}
          ${infoLine('周期 Full Optimize', store.config.optimize.scheduledFull ? `启用 · ${store.config.optimize.intervalMinutes} 分钟` : '关闭 · 自动维护仅 Repair', store.config.optimize.scheduledFull ? 'warning' : 'success')}
          ${infoLine('下次允许 Refresh', fmtTime(store.state.nextRefresh))}
          ${infoLine('测速服务限流', cfstCooldownMeta().detail, cfstCooldownMeta().active ? 'warning' : 'success')}
          ${infoLine('最近 Optimize', fmtTime(store.state.lastOptimize))}
          ${infoLine('最近任务', fmtTime(store.state.lastRun))}
          ${candidateReserveInfo()}
        </div>
      </section>
      <section class="card">
        <div class="card-head"><div><h2 class="card-title">GitHub 同步</h2><div class="card-subtitle">${esc(store.config.sync.repository)} · ${esc(store.config.sync.branch)}</div></div><span class="chip ${sync.lastError ? 'danger' : sync.lastSuccess ? 'success' : ''}">${sync.lastError ? '失败' : sync.lastSuccess ? '正常' : '未发布'}</span></div>
        <div class="card-body metric-stack">
          ${infoLine('自动同步', store.config.sync.enabled ? '启用' : '关闭', store.config.sync.enabled ? 'success' : '')}
          ${infoLine('最近尝试', fmtTime(sync.lastAttempt))}
          ${infoLine('最近成功', fmtTime(sync.lastSuccess))}
          ${infoLine('最近 Commit', sync.lastCommit || '—')}
          ${sync.lastError ? infoLine('错误', sync.lastError, 'danger') : ''}
        </div>
      </section>
    </div>
    <section class="card section">
      <div class="card-head"><div><h2 class="card-title">运行历史</h2><div class="card-subtitle">最多保留 200 次记录</div></div><span class="chip info">${history.length} 条</span></div>
      <div class="card-body">
        <div class="table-wrap">
          <table class="data-table"><thead><tr><th>任务</th><th>开始</th><th>耗时</th><th>结果</th><th>完整 CFST</th><th>映射变化</th><th>候选</th><th>未解析</th><th>错误</th></tr></thead>
          <tbody>${history.length ? history.map(historyRow).join('') : '<tr><td colspan="9" class="table-empty">暂无运行记录</td></tr>'}</tbody></table>
        </div>
      </div>
    </section>
  `
}

// A run can succeed while individual domains stay unresolved, so the result
// column must not read as a clean success in that case.
function historyRow(item) {
  const unresolved = Number(item.unresolvedCount) || 0
  const names = item.unresolvedDomains || []
  const verdict = !item.success
    ? { cls: 'danger', label: '失败' }
    : unresolved
      ? { cls: 'warning', label: '部分完成' }
      : { cls: 'success', label: '成功' }
  const unresolvedCell = unresolved
    ? `<span class="chip warning" title="${esc(names.join('、'))}">${unresolved}</span>`
    : '—'
  return `
    <tr><td><strong>${esc(jobLabel(item.kind))}</strong>${item.targetDomain ? `<div class="card-subtitle mono">${esc(item.targetDomain)}</div>` : ''}</td><td>${fmtTime(item.startedAt)}</td><td>${fmtDuration(item.durationMs)}</td><td><span class="chip ${verdict.cls}"><span class="dot"></span>${verdict.label}</span></td><td>${item.fullRefresh ? '<span class="chip primary">是</span>' : '否'}</td><td>${item.mappingsBefore} → ${item.mappingsAfter}</td><td>${item.candidateCount}</td><td>${unresolvedCell}</td><td>${item.error ? esc(item.error) : '—'}</td></tr>
  `
}

function operationCard(action, title, description, iconName) {  return `
    <button class="card stat-card hoverable operation-card" data-op="${action}">
      <div class="stat-head"><span class="stat-icon">${icon(iconName)}</span><span class="chip">${action === 'apply' ? 'Hosts' : action === 'sync' ? 'GitHub' : 'Task'}</span></div>
      <div class="operation-title">${esc(title)}</div>
      <div class="stat-foot operation-desc">${esc(description)}</div>
    </button>
  `
}

function filteredLogs() {
  const logs = store.logPaused ? (store.frozenLogs || []) : (store.state.logs || [])
  const query = store.logQuery.trim().toLowerCase()
  return { logs, filtered: logs.filter(line => !query || String(line).toLowerCase().includes(query)) }
}

function logLinesHTML(filtered) {
  return filtered.length ? filtered.map(formatLogLine).join('') : '<span class="log-line">暂无日志</span>'
}

// Patch only the result region. Re-rendering the whole page would replace the
// focused search input and lose keystrokes typed while it is detached.
function updateLogResults() {
  const { logs, filtered } = filteredLogs()
  const content = $('#log-content')
  if (content) {
    content.innerHTML = logLinesHTML(filtered)
    if (!store.logPaused) content.scrollTop = content.scrollHeight
  }
  const count = $('#log-count')
  if (count) count.textContent = `${filtered.length} / ${logs.length}`
}

function renderLogs() {
  const { logs, filtered } = filteredLogs()
  return `
    ${pageHeader('日志', '实时查看 CFST、Repair、Tracker、Hosts 与同步运行日志.',
      `<button class="btn secondary" data-action="copy-logs">${icon('copy')}复制</button>
       <button class="btn ${store.logPaused ? '' : 'secondary'}" data-action="toggle-log-pause">${icon(store.logPaused ? 'play' : 'pause')}${store.logPaused ? '继续' : '暂停'}</button>`)}
    <div class="toolbar">
      <div class="search-field">${icon('search')}<input id="log-search" type="search" value="${esc(store.logQuery)}" placeholder="筛选日志内容"></div>
      <span class="toolbar-spacer"></span>
      <span class="chip ${store.state.running ? 'primary' : 'success'}"><span class="dot"></span>${store.state.running ? jobLabel(store.state.currentJob) : '空闲'}</span>
      <span id="log-count" class="chip">${filtered.length} / ${logs.length}</span>
    </div>
    <div class="log-shell">
      <div class="log-toolbar"><span class="log-dots"><i></i><i></i><i></i></span><span class="log-title">CFHost runtime · ${store.logPaused ? 'PAUSED' : 'LIVE'}</span></div>
      <div id="log-content" class="log-content">${logLinesHTML(filtered)}</div>
    </div>
  `
}

function formatLogLine(line) {
  const value = esc(line)
  const colored = value
    .replace(/^([0-9:]{8})/, '<span class="log-time">$1</span>')
    .replace(/(failed|error|warning)/ig, '<span class="log-error">$1</span>')
    .replace(/(finished|success|verified|retained)/ig, '<span class="log-ok">$1</span>')
    .replace(/(repair|optimize|CFST|tracker|hosts|github)/ig, '<span class="log-job">$1</span>')
  return `<span class="log-line">${colored}</span>`
}
