function renderAppearance() {
  const root = $('#appearance-popover')
  const mode = localStorage.getItem('cfhost-theme') || 'system'
  const options = [
    ['system', '跟随系统', 'monitor'],
    ['light', '浅色', 'sun'],
    ['dark', '深色', 'moon'],
    ['glass', '玻璃', 'glass'],
  ]
  root.innerHTML = `
    <div class="popover-title">外观</div>
    ${options.map(([id, label, iconName]) => `
      <button class="theme-option ${mode === id ? 'is-active' : ''}" data-theme-choice="${id}">
        ${icon(iconName)}<span>${label}</span><span class="theme-check">${icon('check')}</span>
      </button>
    `).join('')}
  `
}

function setTheme(mode) {
  localStorage.setItem('cfhost-theme', mode)
  document.documentElement.dataset.themeMode = mode
  const dark = matchMedia('(prefers-color-scheme: dark)').matches
  const resolved = mode === 'system' ? (dark ? 'dark' : 'light') : mode
  document.documentElement.dataset.theme = resolved
  document.querySelector('meta[name="theme-color"]')?.setAttribute('content', resolved === 'light' ? '#F4F5FA' : resolved === 'glass' ? '#0B1322' : '#0E1116')
  renderAppearance()
}

function openAppearance() {
  renderAppearance()
  $('#appearance-popover').classList.toggle('is-open')
}

function closeAppearance() {
  $('#appearance-popover')?.classList.remove('is-open')
}

function renderCommands(query = '') {
  const normalized = query.trim().toLowerCase()
  const navCommands = NAV_GROUPS.flatMap(group => group.items.map(item => ({
    type: '页面',
    label: item.label,
    description: item.hint,
    icon: item.icon,
    action: () => navigate(item.id),
  })))
  const actions = [
    { type: '操作', label: '强制 CFST 测速', description: '重新生成 Cloudflare 候选 IP', icon: 'bolt', action: () => runJob('run') },
    { type: '操作', label: '智能 Repair', description: '验证并修复当前映射', icon: 'repair', action: () => runJob('repair') },
    { type: '操作', label: '完整优化', description: '重新测速并择优全部域名', icon: 'optimize', action: () => runJob('optimize') },
    { type: '操作', label: '应用 Hosts', description: '写入当前已验证映射', icon: 'host', action: () => applyHosts() },
    { type: '操作', label: '同步 GitHub', description: '原子发布当前映射与状态', icon: 'sync', action: () => syncGitHub() },
  ]
  const domainCommands = (store.config?.domains || []).map(domain => ({
    type: '域名',
    label: domain.host,
    description: `${domain.mode.toUpperCase()} · ${domain.class}${domain.class === 'follow' && domain.follow ? ` · 跟随 ${domain.follow}` : ''}`,
    icon: 'globe',
    action: () => {
      store.domainQuery = domain.host
      navigate('domains')
    },
  }))
  const all = [...navCommands, ...actions, ...domainCommands].filter(item => {
    if (!normalized) return true
    return `${item.label} ${item.description} ${item.type}`.toLowerCase().includes(normalized)
  })
  store.commandItems = all
  store.commandIndex = Math.min(store.commandIndex, Math.max(all.length - 1, 0))
  const grouped = all.reduce((result, item, index) => {
    ;(result[item.type] ||= []).push({ ...item, index })
    return result
  }, {})
  $('#command-results').innerHTML = all.length ? Object.entries(grouped).map(([group, items]) => `
    <div class="command-group-label">${esc(group)}</div>
    ${items.map(item => `
      <button class="command-item ${item.index === store.commandIndex ? 'is-selected' : ''}" data-command-index="${item.index}">
        <span class="command-item-icon">${icon(item.icon)}</span>
        <span class="command-item-copy"><strong>${esc(item.label)}</strong><small>${esc(item.description)}</small></span>
        ${icon('chevron')}
      </button>
    `).join('')}
  `).join('') : emptyState('没有匹配结果')
}

function openCommand() {
  closeAppearance()
  const overlay = $('#command-overlay')
  overlay.classList.add('is-open')
  overlay.setAttribute('aria-hidden', 'false')
  const input = $('#command-input')
  input.value = ''
  store.commandIndex = 0
  renderCommands()
  setTimeout(() => input.focus(), 40)
}

function closeCommand() {
  const overlay = $('#command-overlay')
  overlay.classList.remove('is-open')
  overlay.setAttribute('aria-hidden', 'true')
}

function executeCommand(index) {
  const item = store.commandItems[index]
  if (!item) return
  closeCommand()
  Promise.resolve(item.action()).catch(error => toast('操作失败', error.message, 'error'))
}

function modalTemplate(title, description, body, actions) {
  return `
    <div class="modal-head">
      <div class="modal-head-copy"><h2>${esc(title)}</h2>${description ? `<p>${esc(description)}</p>` : ''}</div>
      <button class="icon-button" data-action="close-modal" aria-label="关闭">${icon('close')}</button>
    </div>
    <div class="modal-body">${body}</div>
    <div class="modal-actions">${actions}</div>
  `
}

function openModal(html) {
  $('#modal-panel').innerHTML = html
  const overlay = $('#modal-overlay')
  overlay.classList.add('is-open')
  overlay.setAttribute('aria-hidden', 'false')
  hydrateIcons($('#modal-panel'))
}

function closeModal(result = false) {
  const overlay = $('#modal-overlay')
  overlay.classList.remove('is-open')
  overlay.setAttribute('aria-hidden', 'true')
  const resolver = store.modalResolver
  store.modalResolver = null
  if (resolver) resolver(result)
}

function confirmAction(title, description, confirmText = '确认', danger = false) {
  return new Promise(resolve => {
    store.modalResolver = resolve
    openModal(modalTemplate(
      title,
      description,
      `<div class="alert info">${icon('info')}<div><strong>请确认操作</strong><span>${esc(description)}</span></div></div>`,
      `<button class="btn secondary" data-action="close-modal">取消</button><button class="btn ${danger ? 'danger' : ''}" data-action="confirm-modal">${esc(confirmText)}</button>`,
    ))
  })
}

function syncDomainFollowSelector() {
  const form = $('#domain-form')
  if (!form) return
  const follow = form.elements.follow
  const strategy = form.elements.class
  if (!follow || !strategy) return
  const active = strategy.value === 'follow'
  follow.disabled = !active
  follow.required = active
  follow.closest('.field')?.classList.toggle('is-disabled', !active)
}

function openDomainEditor(index = null) {
  const current = index === null
    ? { host: '', follow: '', class: 'latency', mode: 'http', endpoint: '/', enabled: true }
    : store.config.domains[index]
  const title = index === null ? '添加域名' : '编辑域名'
  const sample = index === null ? null : trackerSampleMeta(current)
  const sampleBlock = sample && current.mode === 'tracker' && current.class !== 'follow'
    ? `<div class="field span-2"><label>Tracker 样本状态</label><div class="alert ${sample.variant === 'danger' ? '' : sample.variant === 'warning' ? 'warning' : 'info'}"><div><strong>${esc(sample.label)}</strong><span>${esc(sample.detail)}</span></div></div></div>`
    : ''
  const followOptions = (store.config.domains || [])
    .filter(domain => domain.host !== current.host && domain.class !== 'follow')
    .map(domain => `<option value="${esc(domain.host)}" ${current.follow === domain.host ? 'selected' : ''}>${esc(domain.host)}${domain.enabled ? '' : '（已停用）'}</option>`)
    .join('')
  const body = `
    <form id="domain-form" class="form-grid">
      <div class="field span-2"><label>域名</label><input name="host" required value="${esc(current.host)}" placeholder="tracker.example.com"></div>
      <div class="field ${current.class === 'follow' ? '' : 'is-disabled'}"><label>跟随域名</label><select name="follow" ${current.class === 'follow' ? 'required' : 'disabled'}><option value="">请选择已有域名</option>${followOptions}</select><span class="hint">仅 follow 策略生效；前三种策略下此项不会参与映射选择。</span></div>
      <div class="field"><label>路径</label><input name="endpoint" value="${esc(current.endpoint || '/')}" placeholder="/"></div>
      <div class="field"><label>策略类别</label><select name="class"><option value="latency" ${current.class === 'latency' || !current.class ? 'selected' : ''}>latency · 延迟优先（验证域名）</option><option value="bandwidth" ${current.class === 'bandwidth' ? 'selected' : ''}>bandwidth · 带宽优先（验证域名）</option><option value="normal" ${current.class === 'normal' ? 'selected' : ''}>normal · 普通（不验证域名）</option><option value="follow" ${current.class === 'follow' ? 'selected' : ''}>follow · 跟随已有域名（共享当前 IP）</option></select></div>
      <div class="field"><label>验证类型</label><select name="mode"><option value="http" ${current.mode === 'http' ? 'selected' : ''}>HTTP</option><option value="tracker" ${current.mode === 'tracker' ? 'selected' : ''}>Tracker</option></select></div>
      <div class="field span-2">
        <div class="switch-row"><div class="switch-copy"><strong>启用域名</strong><small>关闭后不会参与 Repair 或 Optimize。</small></div><label class="switch"><input name="enabled" type="checkbox" ${current.enabled ? 'checked' : ''}><span></span></label></div>
      </div>
      ${sampleBlock}
    </form>
  `
  openModal(modalTemplate(
    title,
    'follow 策略直接复用所选域名的当前 Hosts IP，不再单独测速或验证。',
    body,
    `<button class="btn secondary" data-action="close-modal">取消</button><button class="btn" data-save-domain="${index === null ? 'new' : index}">${icon('check')}保存</button>`,
  ))
  setTimeout(() => {
    syncDomainFollowSelector()
    $('#domain-form input[name="host"]')?.focus()
  }, 40)
}

async function saveDomain(indexToken) {
  const form = $('#domain-form')
  if (!form.reportValidity()) return
  const data = new FormData(form)
  const strategy = String(data.get('class') || 'latency')
  const domain = {
    host: String(data.get('host') || '').trim().toLowerCase(),
    follow: strategy === 'follow' ? String(data.get('follow') || '').trim().toLowerCase() : '',
    class: strategy,
    mode: String(data.get('mode') || 'http'),
    endpoint: String(data.get('endpoint') || '/').trim() || '/',
    enabled: form.elements.enabled.checked,
  }
  const next = deepClone(store.config)
  if (indexToken === 'new') {
    next.domains.push(domain)
  } else {
    const index = Number(indexToken)
    const previous = next.domains[index]
    if (previous && previous.host !== domain.host) {
      next.domains.forEach(item => {
        if (item.follow === previous.host) item.follow = domain.host
      })
    }
    next.domains[index] = domain
  }
  await persistConfig(next)
  closeModal()
  toast('域名已保存', domain.host, 'success')
  renderPage(false)
}

async function deleteDomain(index) {
  const domain = store.config.domains[index]
  if (!domain) return
  const followers = (store.config.domains || []).filter(item => item.follow === domain.host)
  if (followers.length) {
    toast('无法删除', `${domain.host} 正被 ${followers.map(item => item.host).join('、')} 跟随，请先修改这些域名的策略。`, 'error')
    return
  }
  if (!await confirmAction('删除域名', `将从 CFHost 配置中移除 ${domain.host}。`, '删除', true)) return
  const next = deepClone(store.config)
  next.domains.splice(index, 1)
  await persistConfig(next)
  toast('域名已删除', domain.host, 'success')
  renderPage(false)
}

async function toggleDomain(index) {
  const next = deepClone(store.config)
  const domain = next.domains[index]
  if (!domain) return
  domain.enabled = !domain.enabled
  await persistConfig(next)
  toast(domain.enabled ? '域名已启用' : '域名已停用', domain.host, 'success')
  renderPage(false)
}

async function persistConfig(next) {
  const saved = await api('/api/config', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(next),
  })
  store.config = saved
  store.configDraft = deepClone(saved)
  store.dirty = false
  updateShellStatus()
}

async function saveSettings() {
  try {
    await persistConfig(store.configDraft)
    toast('设置已保存', '新的配置已写入 CFHost。', 'success')
    renderPage(false)
  } catch (error) {
    toast('保存失败', error.message, 'error')
  }
}

function discardSettings() {
  store.configDraft = deepClone(store.config)
  store.dirty = false
  renderPage(false)
  toast('已放弃更改', '设置恢复为当前已保存值。')
}

function updateSaveBar() {
  $('#save-bar')?.classList.toggle('is-visible', store.dirty)
}

async function loadTrackerRuntime(domain) {
  if (!domain || domain.mode !== 'tracker') return
  const host = domain.host
  if (!host) return
  if (!store.config?.tracker?.transmission?.enabled) {
    store.trackerRuntime[host] = { loading: false, error: 'Transmission 未启用' }
    updateDomainResults()
    return
  }

  store.trackerRuntime[host] = { loading: true, error: '', data: null }
  updateDomainResults()
  try {
    const data = await api(`/api/tracker-runtime?host=${encodeURIComponent(host)}`)
    store.trackerRuntime[host] = { loading: false, error: '', data }
  } catch (error) {
    store.trackerRuntime[host] = { loading: false, error: error.message || '读取 Transmission 状态失败', data: null }
  }
  if (store.page === 'domains' && store.expandedDomainHost === host) updateDomainResults()
}

async function maintainDomain(index) {
  const domain = store.config?.domains?.[index]
  if (!domain) return
  if (!domain.enabled) {
    toast('无法维护', `${domain.host} 当前已停用。`, 'error')
    return
  }
  if (store.state?.running) {
    const target = store.state.currentDomain ? ` · ${store.state.currentDomain}` : ''
    toast('已有任务运行中', `${jobLabel(store.state.currentJob)}${target}`, 'error')
    return
  }
  try {
    await api('/api/domain-maintain', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ host: domain.host }),
    })
    toast('单域名维护已启动', domain.host, 'success')
    await refreshStatus({ quiet: true })
    renderPage(false, false)
  } catch (error) {
    toast('单域名维护启动失败', error.message, 'error')
  }
}

async function runJob(kind) {
  if (store.state?.running) {
    toast('已有任务运行中', jobLabel(store.state.currentJob), 'error')
    return
  }
  if (kind === 'optimize') {
    const ok = await confirmAction('执行完整优化', '将运行完整 CFST，并重新验证和选择全部域名的候选 IP。', '开始优化')
    if (!ok) return
  }
  try {
    await api(`/api/${kind}`, { method: 'POST' })
    toast('任务已启动', jobLabel(kind), 'success')
    await refreshStatus({ quiet: true })
    renderPage(false)
  } catch (error) {
    toast('任务启动失败', error.message, 'error')
  }
}

async function applyHosts() {
  const ok = await confirmAction('应用 Hosts', `将当前已验证映射写入 ${store.config.hostsPath} 的 CFHost Marker。`, '应用 Hosts')
  if (!ok) return
  try {
    const result = await api('/api/apply', { method: 'POST' })
    toast('Hosts 已应用', result?.sync ? `同步：${result.sync}` : '宿主机 Hosts 已更新。', 'success')
    await refreshStatus({ quiet: true })
    renderPage(false)
  } catch (error) {
    toast('Hosts 应用失败', error.message, 'error')
  }
}

async function syncGitHub() {
  try {
    await api('/api/sync', { method: 'POST' })
    toast('GitHub 同步完成', 'hosts-map.tsv 与 status.json 已原子发布。', 'success')
    await refreshStatus({ quiet: true })
    renderPage(false)
  } catch (error) {
    toast('GitHub 同步失败', error.message, 'error')
  }
}

function toast(title, message = '', type = '') {
  const stack = $('#toast-stack')
  const element = document.createElement('div')
  element.className = `toast ${type}`
  element.innerHTML = `<span class="toast-icon">${icon(type === 'success' ? 'check' : type === 'error' ? 'warning' : 'info')}</span><span class="toast-copy"><strong>${esc(title)}</strong>${message ? `<span>${esc(message)}</span>` : ''}</span>`
  stack.appendChild(element)
  requestAnimationFrame(() => element.classList.add('is-visible'))
  setTimeout(() => {
    element.classList.add('is-leaving')
    setTimeout(() => element.remove(), 200)
  }, type === 'error' ? 5200 : 3200)
}

function openSidebar() {
  document.documentElement.classList.add('sidebar-open')
}

function closeSidebar() {
  document.documentElement.classList.remove('sidebar-open')
}

function updateSettingsNav() {
  if (store.page !== 'settings') return
  const sections = $$('.settings-section')
  if (!sections.length) return
  let best = sections[0].id.replace('settings-', '')
  for (const section of sections) {
    if (section.getBoundingClientRect().top <= 155) best = section.id.replace('settings-', '')
  }
  $$('[data-settings-nav]').forEach(button => button.classList.toggle('is-active', button.dataset.settingsNav === best))
}

function createRipple(event) {
  const button = event.target.closest('.btn, .quick-action')
  if (!button) return
  const rect = button.getBoundingClientRect()
  const size = Math.max(rect.width, rect.height)
  const span = document.createElement('span')
  span.className = 'ripple'
  span.style.width = span.style.height = `${size}px`
  span.style.left = `${event.clientX - rect.left}px`
  span.style.top = `${event.clientY - rect.top}px`
  button.appendChild(span)
  setTimeout(() => span.remove(), 480)
}


async function discoverTrackerSamplesNow() {
  if (store.dirty) {
    toast('请先保存设置', '下载器地址或凭据有未保存修改。', 'error')
    return
  }
  try {
    const result = await api('/api/tracker-samples/discover', { method: 'POST' })
    const report = result?.report || {}
    const domains = report.domains || []
    const source = []
    if (report.transmission) source.push(`Transmission ${report.transmission}`)
    if (report.qbittorrent) source.push(`qBittorrent ${report.qbittorrent}`)
    await refreshStatus({ quiet: true })
    if (domains.length) {
      toast('Tracker 样本发现完成', `${domains.length} 个域名 · ${source.join(' · ') || '已更新缓存'} · 可到域名页查看样本状态`, 'success')
    } else if (report.errors?.length) {
      toast('没有发现 Tracker 样本', report.errors.join('；'), 'error')
    } else {
      toast('没有发现 Tracker 样本', '请确认下载器中存在这些 Tracker 的已完成种子。')
    }
  } catch (error) {
    toast('自动发现失败', error.message, 'error')
  }
}
