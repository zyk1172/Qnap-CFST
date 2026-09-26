document.addEventListener('click', async event => {
  createRipple(event)

  const nav = event.target.closest('[data-nav]')
  if (nav) {
    navigate(nav.dataset.nav)
    return
  }

  const job = event.target.closest('[data-job]')
  if (job) {
    await runJob(job.dataset.job)
    return
  }

  const op = event.target.closest('[data-op]')
  if (op) {
    const action = op.dataset.op
    if (['run', 'repair', 'optimize'].includes(action)) await runJob(action)
    else if (action === 'apply') await applyHosts()
    else if (action === 'sync') await syncGitHub()
    return
  }

  const action = event.target.closest('[data-action]')?.dataset.action
  if (action === 'open-sidebar') openSidebar()
  else if (action === 'close-sidebar') closeSidebar()
  else if (action === 'open-command') openCommand()
  else if (action === 'open-appearance') openAppearance()
  else if (action === 'refresh') {
    try {
      await loadAll()
      renderPage(false, false)
      updateShellStatus()
      toast('已刷新', '配置和运行状态已重新读取。', 'success')
    } catch (error) {
      toast('刷新失败', error.message, 'error')
    }
  } else if (action === 'close-modal') closeModal(false)
  else if (action === 'confirm-modal') closeModal(true)
  else if (action === 'add-domain') openDomainEditor()
  else if (action === 'save-settings') await saveSettings()
  else if (action === 'discard-settings') discardSettings()
  else if (action === 'discover-trackers') {
    await discoverTrackerSamplesNow()
  } else if (action === 'copy-logs') {
    try {
      await navigator.clipboard.writeText((store.state.logs || []).join('\n'))
      toast('日志已复制', `${(store.state.logs || []).length} 行`, 'success')
    } catch (error) {
      toast('复制失败', error.message, 'error')
    }
  } else if (action === 'toggle-log-pause') {
    if (!store.logPaused) store.frozenLogs = [...(store.state.logs || [])]
    store.logPaused = !store.logPaused
    renderPage(false, false)
  }

  const theme = event.target.closest('[data-theme-choice]')
  if (theme) {
    setTheme(theme.dataset.themeChoice)
    return
  }

  const expandDomain = event.target.closest('[data-domain-expand]')
  if (expandDomain) {
    const index = Number(expandDomain.dataset.domainExpand)
    const domain = store.config?.domains?.[index]
    if (domain) {
      const opening = store.expandedDomainHost !== domain.host
      store.expandedDomainHost = opening ? domain.host : ''
      updateDomainResults()
      if (opening && domain.mode === 'tracker' && domain.class !== 'follow') await loadTrackerRuntime(domain)
    }
    return
  }

  const maintain = event.target.closest('[data-maintain-domain]')
  if (maintain) {
    await maintainDomain(Number(maintain.dataset.maintainDomain))
    return
  }

  const edit = event.target.closest('[data-edit-domain]')
  if (edit) {
    openDomainEditor(Number(edit.dataset.editDomain))
    return
  }

  const toggle = event.target.closest('[data-toggle-domain]')
  if (toggle) {
    await toggleDomain(Number(toggle.dataset.toggleDomain))
    return
  }

  const remove = event.target.closest('[data-delete-domain]')
  if (remove) {
    await deleteDomain(Number(remove.dataset.deleteDomain))
    return
  }

  const saveDomainButton = event.target.closest('[data-save-domain]')
  if (saveDomainButton) {
    await saveDomain(saveDomainButton.dataset.saveDomain)
    return
  }

  const filter = event.target.closest('[data-domain-filter]')
  if (filter) {
    store.domainFilter = filter.dataset.domainFilter
    renderPage(false)
    return
  }

  const sort = event.target.closest('[data-candidate-sort]')
  if (sort) {
    store.candidateSort = sort.dataset.candidateSort
    renderPage(false)
    return
  }

  const settingsNav = event.target.closest('[data-settings-nav]')
  if (settingsNav) {
    document.querySelector(`#settings-${settingsNav.dataset.settingsNav}`)?.scrollIntoView({ behavior: 'smooth', block: 'start' })
    return
  }

  const command = event.target.closest('[data-command-index]')
  if (command) executeCommand(Number(command.dataset.commandIndex))

  // The appearance toggle buttons are not all inside .popover-anchor (the sidebar
  // footer one is not), so an outside-click must not immediately undo an open.
  if (action !== 'open-appearance' && !event.target.closest('.popover-anchor')) closeAppearance()
})

// Search inputs only patch the result region. Re-rendering the whole page used
// to replace the focused <input>, which left the document unfocused for ~80ms
// per keystroke and silently dropped every character typed faster than roughly
// 12 keys per second.
document.addEventListener('input', event => {
  if (event.target.matches?.('#domain-form select[name="class"]')) {
    syncDomainFollowSelector()
    return
  }

  if (event.target.id === 'domain-search') {
    store.domainQuery = event.target.value
    updateDomainResults()
    return
  }

  if (event.target.id === 'log-search') {
    store.logQuery = event.target.value
    updateLogResults()
    return
  }

  if (event.target.id === 'command-input') {
    store.commandIndex = 0
    renderCommands(event.target.value)
    return
  }

  const path = event.target.dataset.configPath
  if (path && store.configDraft) {
    const type = event.target.dataset.configType
    const value = type === 'number'
      ? Number(event.target.value)
      : type === 'bool'
        ? event.target.checked
        : event.target.value
    setPath(store.configDraft, path, value)
    store.dirty = !deepEqual(store.configDraft, store.config)
    updateSaveBar()
  }
})

document.addEventListener('change', event => {
  const path = event.target.dataset.configPath
  if (!path || !store.configDraft) return
  const type = event.target.dataset.configType
  const value = type === 'number'
    ? Number(event.target.value)
    : type === 'bool'
      ? event.target.checked
      : event.target.value
  setPath(store.configDraft, path, value)
  store.dirty = !deepEqual(store.configDraft, store.config)
  updateSaveBar()
})

document.addEventListener('keydown', event => {
  const commandOpen = $('#command-overlay').classList.contains('is-open')
  const modalOpen = $('#modal-overlay').classList.contains('is-open')

  if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
    event.preventDefault()
    commandOpen ? closeCommand() : openCommand()
    return
  }

  if (event.key === 'Escape') {
    if (commandOpen) closeCommand()
    else if (modalOpen) closeModal(false)
    else closeAppearance()
    return
  }

  if (!commandOpen) return

  if (event.key === 'ArrowDown') {
    event.preventDefault()
    store.commandIndex = Math.min(store.commandIndex + 1, Math.max((store.commandItems.length || 1) - 1, 0))
    renderCommands($('#command-input').value)
  } else if (event.key === 'ArrowUp') {
    event.preventDefault()
    store.commandIndex = Math.max(store.commandIndex - 1, 0)
    renderCommands($('#command-input').value)
  } else if (event.key === 'Enter') {
    event.preventDefault()
    executeCommand(store.commandIndex)
  }
})

$('#command-overlay').addEventListener('click', event => {
  if (event.target === $('#command-overlay')) closeCommand()
})

$('#modal-overlay').addEventListener('click', event => {
  if (event.target === $('#modal-overlay')) closeModal(false)
})

window.addEventListener('hashchange', routeChanged)

window.addEventListener('scroll', () => {
  document.documentElement.classList.toggle('window-scrolled', scrollY > 12)
  updateSettingsNav()
}, { passive: true })

matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
  if ((localStorage.getItem('cfhost-theme') || 'system') === 'system') setTheme('system')
})

window.addEventListener('beforeunload', event => {
  if (!store.dirty) return
  event.preventDefault()
  event.returnValue = ''
})

async function boot() {
  localStorage.removeItem('cfhost-sidebar-collapsed')
  document.documentElement.classList.remove('sidebar-collapsed')

  renderNavigation()
  renderAppearance()

  try {
    await loadAll()
    store.page = currentPageFromHash()
    updateNavigation()
    renderPage(false, true)
    updateShellStatus()
    $('#app').setAttribute('aria-hidden', 'false')
    setTimeout(() => $('#boot-screen').classList.add('is-hidden'), 100)
    store.polling = setInterval(() => refreshStatus({ quiet: false }), 3000)
  } catch (error) {
    $('#boot-screen').innerHTML = `
      <div class="boot-logo"><span></span><span></span></div>
      <div class="boot-wordmark">CFHOST</div>
      <div class="boot-error">${esc(error.message)}</div>
      <button id="boot-retry" class="btn">重新连接</button>
    `
    $('#boot-retry').addEventListener('click', () => location.reload())
  }
}

boot()
