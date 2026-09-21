const NAV_GROUPS = [
  {
    label: '开始',
    items: [{ id: 'dashboard', label: '概览', icon: 'dashboard', hint: '运行状态与快捷操作' }],
  },
  {
    label: '管理',
    items: [
      { id: 'domains', label: '域名', icon: 'globe', hint: '受管域名与当前映射' },
      { id: 'candidates', label: '候选 IP', icon: 'network', hint: 'CFST 测速候选与质量' },
    ],
  },
  {
    label: '运行',
    items: [
      { id: 'operations', label: '任务与历史', icon: 'activity', hint: 'Repair、Optimize 与运行记录' },
      { id: 'logs', label: '日志', icon: 'terminal', hint: '实时运行日志' },
    ],
  },
  {
    label: '系统',
    items: [{ id: 'settings', label: '设置', icon: 'settings', hint: '测速、验证、Hosts 与同步' }],
  },
]

const PAGE_META = {
  dashboard: { title: '概览', eyebrow: 'CFHost Dashboard' },
  domains: { title: '域名', eyebrow: 'Domain Manager' },
  candidates: { title: '候选 IP', eyebrow: 'Cloudflare Speed Test' },
  operations: { title: '任务与历史', eyebrow: 'Operations' },
  logs: { title: '日志', eyebrow: 'Runtime Logs' },
  settings: { title: '设置', eyebrow: 'Configuration' },
}

const ICONS = {
  dashboard: '<rect x="3" y="3" width="7" height="7" rx="2"/><rect x="14" y="3" width="7" height="7" rx="2"/><rect x="3" y="14" width="7" height="7" rx="2"/><rect x="14" y="14" width="7" height="7" rx="2"/>',
  globe: '<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3a15 15 0 0 1 0 18M12 3a15 15 0 0 0 0 18"/>',
  network: '<path d="M5 8.5a10 10 0 0 1 14 0M8 12a6 6 0 0 1 8 0M10.7 15.4a2 2 0 0 1 2.6 0"/><circle cx="12" cy="18" r=".8" fill="currentColor" stroke="none"/>',
  activity: '<path d="M3 12h4l2.2-5.2 4.1 10.4L15.5 12H21"/>',
  terminal: '<path d="m5 7 4 4-4 4M11 16h8"/><rect x="3" y="4" width="18" height="16" rx="3"/>',
  settings: '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .34 1.88l.06.06-2.83 2.83-.06-.06a1.7 1.7 0 0 0-1.88-.34 1.7 1.7 0 0 0-1.03 1.56V21h-4v-.08A1.7 1.7 0 0 0 9 19.36a1.7 1.7 0 0 0-1.88.34l-.06.06-2.83-2.83.06-.06A1.7 1.7 0 0 0 4.63 15 1.7 1.7 0 0 0 3.08 14H3v-4h.08A1.7 1.7 0 0 0 4.64 9a1.7 1.7 0 0 0-.34-1.88l-.06-.06 2.83-2.83.06.06A1.7 1.7 0 0 0 9 4.63 1.7 1.7 0 0 0 10 3.08V3h4v.08A1.7 1.7 0 0 0 15 4.64a1.7 1.7 0 0 0 1.88-.34l.06-.06 2.83 2.83-.06.06A1.7 1.7 0 0 0 19.37 9 1.7 1.7 0 0 0 20.92 10H21v4h-.08A1.7 1.7 0 0 0 19.4 15Z"/>',
  search: '<circle cx="11" cy="11" r="7"/><path d="m20 20-4-4"/>',
  refresh: '<path d="M20 7v5h-5M4 17v-5h5"/><path d="M18.2 9A7 7 0 0 0 6.1 6.1L4 8M5.8 15A7 7 0 0 0 17.9 17.9L20 16"/>',
  theme: '<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.41-1.41M17.66 6.34l1.41-1.41"/>',
  menu: '<path d="M4 7h16M4 12h16M4 17h16"/>',
  sidebar: '<rect x="3" y="4" width="18" height="16" rx="3"/><path d="M9 4v16"/>',
  palette: '<path d="M12 3a9 9 0 1 0 0 18h1.2a2 2 0 0 0 0-4H12a1.5 1.5 0 0 1 0-3h3.2A5.8 5.8 0 0 0 21 8.2C20.1 5.2 16.4 3 12 3Z"/><circle cx="7.5" cy="10" r="1" fill="currentColor" stroke="none"/><circle cx="10" cy="6.8" r="1" fill="currentColor" stroke="none"/><circle cx="14.5" cy="6.8" r="1" fill="currentColor" stroke="none"/>',
  bolt: '<path d="m13 2-8 12h7l-1 8 8-12h-7l1-8Z"/>',
  repair: '<path d="M14.7 6.3a4 4 0 0 0-5.3 5.3L4 17l3 3 5.4-5.4a4 4 0 0 0 5.3-5.3l-2.4 2.4-3-3 2.4-2.4Z"/>',
  optimize: '<path d="m12 3 1.7 5.3L19 10l-5.3 1.7L12 17l-1.7-5.3L5 10l5.3-1.7L12 3Z"/><path d="m19 16 .8 2.2L22 19l-2.2.8L19 22l-.8-2.2L16 19l2.2-.8L19 16Z"/>',
  host: '<rect x="3" y="4" width="18" height="6" rx="2"/><rect x="3" y="14" width="18" height="6" rx="2"/><path d="M7 7h.01M7 17h.01M11 7h6M11 17h6"/>',
  sync: '<path d="M20 7h-5V2M4 17h5v5"/><path d="M18 4.5A8 8 0 0 0 5.2 7M6 19.5A8 8 0 0 0 18.8 17"/>',
  check: '<path d="m5 12 4 4L19 6"/>',
  close: '<path d="M6 6l12 12M18 6 6 18"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  edit: '<path d="M4 20h4l11-11-4-4L4 16v4Z"/><path d="m13.5 6.5 4 4"/>',
  trash: '<path d="M4 7h16M9 7V4h6v3M7 7l1 13h8l1-13M10 11v5M14 11v5"/>',
  copy: '<rect x="8" y="8" width="11" height="11" rx="2"/><path d="M16 8V5a2 2 0 0 0-2-2H5a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h3"/>',
  play: '<path d="m8 5 11 7-11 7V5Z"/>',
  pause: '<path d="M8 5v14M16 5v14"/>',
  clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
  database: '<ellipse cx="12" cy="5" rx="8" ry="3"/><path d="M4 5v6c0 1.7 3.6 3 8 3s8-1.3 8-3V5M4 11v6c0 1.7 3.6 3 8 3s8-1.3 8-3v-6"/>',
  shield: '<path d="M12 3 5 6v5c0 5 3 8 7 10 4-2 7-5 7-10V6l-7-3Z"/><path d="m9 12 2 2 4-4"/>',
  github: '<path d="M12 2.8a9.2 9.2 0 0 0-2.9 17.9c.46.08.63-.2.63-.45v-1.8c-2.55.55-3.09-1.08-3.09-1.08-.42-1.06-1.02-1.34-1.02-1.34-.83-.57.06-.56.06-.56.92.07 1.4.95 1.4.95.82 1.4 2.15 1 2.67.76.08-.59.32-1 .58-1.23-2.04-.23-4.18-1.02-4.18-4.55 0-1 .36-1.83.95-2.47-.1-.23-.41-1.17.09-2.43 0 0 .78-.25 2.53.94A8.8 8.8 0 0 1 12 7.13a8.7 8.7 0 0 1 2.3.31c1.76-1.19 2.53-.94 2.53-.94.5 1.26.19 2.2.1 2.43.59.64.94 1.46.94 2.47 0 3.54-2.15 4.31-4.2 4.54.33.29.63.85.63 1.72v2.59c0 .25.16.54.63.45A9.2 9.2 0 0 0 12 2.8Z"/>',
  info: '<circle cx="12" cy="12" r="9"/><path d="M12 11v6M12 7h.01"/>',
  warning: '<path d="M12 3 2.8 20h18.4L12 3Z"/><path d="M12 9v5M12 17h.01"/>',
  chevron: '<path d="m9 6 6 6-6 6"/>',
  sun: '<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/>',
  moon: '<path d="M20 15.5A8.5 8.5 0 0 1 8.5 4 8.5 8.5 0 1 0 20 15.5Z"/>',
  monitor: '<rect x="3" y="4" width="18" height="14" rx="2"/><path d="M8 22h8M12 18v4"/>',
  glass: '<path d="M5 4h14l-2 8a5 5 0 0 1-10 0L5 4Z"/><path d="M8 8h8M12 17v4M8 21h8"/>',
}

const store = {
  config: null,
  state: null,
  page: 'dashboard',
  dirty: false,
  configDraft: null,
  domainQuery: '',
  domainFilter: 'all',
  candidateSort: 'latency',
  logQuery: '',
  logPaused: false,
  frozenLogs: [],
  commandIndex: 0,
  commandItems: [],
  modalResolver: null,
  polling: null,
  renderSignature: '',
}

const $ = selector => document.querySelector(selector)
const $$ = selector => [...document.querySelectorAll(selector)]

function icon(name, cls = '') {
  const body = ICONS[name] || ICONS.info
  return `<svg class="${cls}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${body}</svg>`
}

function esc(value) {
  return String(value ?? '').replace(/[&<>"']/g, ch => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[ch])
}

function fmtTime(value, short = false) {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return String(value)
  const options = short
    ? { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }
    : { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' }
  return new Intl.DateTimeFormat('zh-CN', options).format(date)
}

function fmtDuration(ms) {
  const value = Number(ms) || 0
  if (value < 1000) return `${value} ms`
  if (value < 60000) return `${(value / 1000).toFixed(value >= 10000 ? 0 : 1)} s`
  return `${Math.floor(value / 60000)}m ${Math.round((value % 60000) / 1000)}s`
}

function deepClone(value) {
  return JSON.parse(JSON.stringify(value))
}

function deepEqual(a, b) {
  return JSON.stringify(a) === JSON.stringify(b)
}

function setPath(obj, path, value) {
  const keys = path.split('.')
  let cursor = obj
  for (let index = 0; index < keys.length - 1; index++) cursor = cursor[keys[index]]
  cursor[keys[keys.length - 1]] = value
}

async function api(path, options = {}) {
  const response = await fetch(path, options)
  const text = await response.text()
  if (!response.ok) throw new Error(text.trim() || `HTTP ${response.status}`)
  if (!text) return null
  try { return JSON.parse(text) } catch { return text }
}

async function loadAll() {
  const [config, state] = await Promise.all([api('/api/config'), api('/api/status')])
  store.config = config
  store.configDraft = deepClone(config)
  store.state = state
  store.dirty = false
}

async function refreshStatus({ quiet = false } = {}) {
  try {
    store.state = await api('/api/status')
    updateShellStatus()
    const nextSignature = pageRefreshSignature()
    if (!quiet && canAutoRender() && nextSignature !== store.renderSignature) {
      renderPage(false, false)
    }
  } catch (error) {
    updateConnectionError(error)
  }
}

function canAutoRender() {
  if ($('#modal-overlay')?.classList.contains('is-open')) return false
  if ($('#command-overlay')?.classList.contains('is-open')) return false
  if (['INPUT', 'SELECT', 'TEXTAREA'].includes(document.activeElement?.tagName)) return false
  if (store.page === 'settings' && store.dirty) return false
  return true
}

function pageRefreshSignature(page = store.page, state = store.state) {
  if (!state) return ''

  let snapshot
  switch (page) {
  case 'dashboard':
    snapshot = {
      running: state.running,
      currentJob: state.currentJob,
      lastError: state.lastError,
      lastSuccess: state.lastSuccess,
      lastOptimize: state.lastOptimize,
      mappings: state.mappings,
      domainStatus: state.domainStatus,
      history: state.history,
      candidates: state.candidates,
      sync: state.sync,
      migrationStatus: state.migrationStatus,
    }
    break
  case 'domains':
    snapshot = {
      mappings: state.mappings,
      domainStatus: state.domainStatus,
      domainHealth: state.domainHealth,
    }
    break
  case 'candidates':
    snapshot = {
      candidates: state.candidates,
      lastRefresh: state.lastRefresh,
    }
    break
  case 'operations':
    snapshot = {
      running: state.running,
      currentJob: state.currentJob,
      lastRun: state.lastRun,
      lastOptimize: state.lastOptimize,
      nextRefresh: state.nextRefresh,
      history: state.history,
      sync: state.sync,
    }
    break
  case 'logs':
    snapshot = {
      running: state.running,
      currentJob: state.currentJob,
      logs: state.logs,
    }
    break
  default:
    snapshot = null
  }

  return JSON.stringify(snapshot)
}

function currentPageFromHash() {
  const page = location.hash.replace(/^#\/?/, '').split('/')[0]
  return PAGE_META[page] ? page : 'dashboard'
}

function navigate(page) {
  if (!PAGE_META[page]) return
  if (location.hash !== `#/${page}`) location.hash = `#/${page}`
  else routeChanged()
}

function routeChanged() {
  store.page = currentPageFromHash()
  closeSidebar()
  closeAppearance()
  updateNavigation()
  renderPage(true)
  $('#page-host')?.focus({ preventScroll: true })
  window.scrollTo({ top: 0, behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' })
}

function renderNavigation() {
  const root = $('#nav-groups')
  root.innerHTML = NAV_GROUPS.map(group => `
    <div class="nav-section">
      <div class="nav-section-title">${esc(group.label)}</div>
      ${group.items.map(item => `
        <button class="nav-item" data-nav="${item.id}" title="${esc(item.label)}">
          <span class="nav-icon">${icon(item.icon)}</span>
          <span class="nav-label">${esc(item.label)}</span>
          ${item.id === 'domains' ? '<span id="domain-nav-badge" class="nav-badge">0</span>' : ''}
        </button>
      `).join('')}
    </div>
  `).join('')
  hydrateIcons()
}

function updateNavigation() {
  $$('.nav-item').forEach(element => element.classList.toggle('is-active', element.dataset.nav === store.page))
  const meta = PAGE_META[store.page]
  $('#page-title').textContent = meta.title
  $('#page-eyebrow').textContent = meta.eyebrow
}

function updateShellStatus() {
  const running = !!store.state?.running
  const error = store.state?.lastError
  const counts = statusCounts()
  const unresolved = counts.failed > 0
  const text = running
    ? jobLabel(store.state.currentJob)
    : error
      ? '需要注意'
      : unresolved
        ? '有域名待处理'
        : '运行正常'
  const subtitle = running
    ? '任务执行中'
    : error
      ? String(error)
      : unresolved
        ? `${counts.failed} 个域名当前无映射`
        : store.state?.lastSuccess
          ? `最近成功 ${fmtTime(store.state.lastSuccess, true)}`
          : 'CFHost Service'
  const statusClass = running ? 'is-running' : error ? 'is-error' : unresolved ? 'is-warning' : 'is-ok'
  $('#sidebar-status').textContent = text
  $('#sidebar-subtitle').textContent = subtitle
  const sideDot = $('#sidebar-status-dot')
  sideDot.className = `status-dot ${statusClass}`
  const top = $('#topbar-service')
  top.innerHTML = `<span class="status-dot ${statusClass}"></span><span>${esc(text)}</span>`
  $('#job-progress').classList.toggle('is-active', running)
  const badge = $('#domain-nav-badge')
  if (badge) badge.textContent = String((store.config?.domains || []).filter(domain => domain.enabled).length)
}

function updateConnectionError(error) {
  $('#sidebar-status').textContent = '连接失败'
  $('#sidebar-subtitle').textContent = String(error.message || error)
  $('#sidebar-status-dot').className = 'status-dot is-error'
  $('#topbar-service').innerHTML = '<span class="status-dot is-error"></span><span>连接失败</span>'
}

function pageHeader(title, description, actions = '') {
  return `
    <div class="page-header">
      <div class="page-header-copy">
        <h1>${esc(title)}</h1>
        <p>${esc(description)}</p>
      </div>
      <div class="page-actions">${actions}</div>
    </div>
  `
}

function renderPage(animate = true, animateDashboardNumbers = animate) {
  if (!store.config || !store.state) return
  const renderers = {
    dashboard: renderDashboard,
    domains: renderDomains,
    candidates: renderCandidates,
    operations: renderOperations,
    logs: renderLogs,
    settings: renderSettings,
  }
  $('#page-host').innerHTML = `<div class="page-route ${animate ? 'is-entering' : ''}">${renderers[store.page]()}</div>`
  hydrateIcons()
  updateShellStatus()
  if (store.page === 'dashboard' && animateDashboardNumbers) animateNumbers()
  if (store.page === 'settings') updateSaveBar()
  if (store.page === 'logs' && !store.logPaused) {
    requestAnimationFrame(() => {
      const content = $('#log-content')
      if (content) content.scrollTop = content.scrollHeight
    })
  }
  store.renderSignature = pageRefreshSignature()
}

function statusCounts() {
  const domains = store.config.domains || []
  const enabled = domains.filter(domain => domain.enabled)
  const mapped = store.state.mappings || {}
  const ok = enabled.filter(domain => !!mapped[domain.host]).length
  return { total: enabled.length, ok, failed: Math.max(enabled.length - ok, 0), disabled: domains.length - enabled.length }
}

function candidateStats() {
  const list = store.state.candidates || []
  if (!list.length) return { bestDelay: 0, topSpeed: 0, count: 0 }
  const finiteDelays = list.map(item => Number(item.delayMs)).filter(Number.isFinite)
  return {
    bestDelay: finiteDelays.length ? Math.min(...finiteDelays) : 0,
    topSpeed: Math.max(...list.map(item => Number(item.speedMB) || 0)),
    count: list.length,
  }
}
