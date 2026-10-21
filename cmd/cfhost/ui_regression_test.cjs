const assert = require('node:assert/strict')
const { readFileSync } = require('node:fs')
const { join } = require('node:path')
const { test } = require('node:test')
const vm = require('node:vm')

function fixture() {
  const listeners = {}
  const node = { classList: { contains: () => false }, addEventListener() {} }
  const context = vm.createContext({
    document: { addEventListener: (name, listener) => { listeners[name] = listener }, querySelector: () => node },
    window: { addEventListener() {} },
    matchMedia: () => ({ addEventListener() {} }),
    AbortController, setTimeout, clearTimeout,
  })
  for (const name of ['core', 'interactions', 'events']) {
    const source = readFileSync(join(__dirname, 'web/js', `${name}.js`), 'utf8')
    vm.runInContext(name === 'events' ? source.replace(/\nboot\(\)\s*$/, '') : source, context)
  }
  vm.runInContext(`
    updateShellStatus = () => {}
    renderPage = () => {}
    createRipple = () => {}
    store.config = { cfst: { threads: 200 }, domains: [] }
    store.configDraft = deepClone(store.config)
    store.state = { running: false, mappings: {}, logs: [] }
  `, context)
  return { context, listeners, ...vm.runInContext('({ store, refreshStatus, persistConfig, pageRefreshSignature, handleClick })', context) }
}

function deferred() {
  let resolve, reject
  const promise = new Promise((ok, fail) => { resolve = ok; reject = fail })
  return { promise, resolve, reject }
}

test('HTTP probe and rate limit changes invalidate their visible pages', () => {
  const { store, pageRefreshSignature } = fixture()
  const domains = pageRefreshSignature('domains')
  store.state.httpProbe = { 'origin.example': { available: true, statusCode: 403 } }
  assert.notEqual(pageRefreshSignature('domains'), domains)
  const operations = pageRefreshSignature('operations')
  store.state.cfstRateLimit = { active: true, statusCode: 429 }
  assert.notEqual(pageRefreshSignature('operations'), operations)
})

test('paused logs ignore new live log lines', () => {
  const { store, pageRefreshSignature } = fixture()
  store.logPaused = true
  store.frozenLogs = ['frozen']
  const before = pageRefreshSignature('logs')
  store.state.logs.push('new live line')
  assert.equal(pageRefreshSignature('logs'), before)
})

test('concurrent status polls share one request and recover after a failure', async () => {
  const f = fixture()
  const pending = deferred()
  let requests = 0
  f.context.api = () => { requests++; return pending.promise }
  f.context.updateConnectionError = () => {}
  const polls = [f.refreshStatus(), f.refreshStatus(), f.refreshStatus()]
  assert.equal(requests, 1)
  pending.reject(new Error('connection lost'))
  await Promise.all(polls)
  assert.equal(f.store.statusRequest, null)
  f.context.api = () => { requests++; return Promise.resolve({ logs: ['reconnected'] }) }
  await f.refreshStatus()
  assert.equal(requests, 2)
  assert.equal(f.store.state.logs[0], 'reconnected')
})

test('status poll timeout aborts and allows reconnection', async () => {
  const f = fixture()
  let timeout, error
  f.context.setTimeout = callback => { timeout = callback; return 1 }
  f.context.clearTimeout = () => {}
  f.context.updateConnectionError = value => { error = value }
  f.context.api = (_, { signal }) => new Promise((_, reject) => {
    signal.addEventListener('abort', () => reject(new Error('timed out')))
  })
  const poll = f.refreshStatus()
  timeout()
  await poll
  assert.equal(error.message, 'timed out')
  assert.equal(f.store.statusRequest, null)
})

test('failed settings saves preserve the saved configuration and draft', async () => {
  const f = fixture()
  f.store.configDraft.cfst.threads = 123
  f.store.dirty = true
  f.context.api = () => Promise.reject(new Error('disk full'))
  await assert.rejects(f.persistConfig(f.store.configDraft), /disk full/)
  assert.equal(f.store.config.cfst.threads, 200)
  assert.equal(f.store.configDraft.cfst.threads, 123)
  assert.equal(f.store.dirty, true)
  assert.equal(f.store.configSaving, false)
})

test('edits made during a slow save remain unsaved instead of disappearing', async () => {
  const f = fixture()
  const pending = deferred()
  f.store.configDraft.cfst.threads = 123
  f.store.dirty = true
  f.context.api = (_, options) => { const saved = JSON.parse(options.body); return pending.promise.then(() => saved) }
  const save = f.persistConfig(f.store.configDraft)
  f.store.configDraft.cfst.threads = 124
  pending.resolve()
  await save
  assert.equal(f.store.config.cfst.threads, 123)
  assert.equal(f.store.configDraft.cfst.threads, 124)
  assert.equal(f.store.dirty, true)
})

test('overlapping config writes and domain writes with a settings draft are blocked', async () => {
  const f = fixture()
  let requests = 0
  f.context.api = (_, options) => { requests++; return Promise.resolve(JSON.parse(options.body)) }
  const first = f.persistConfig(f.store.configDraft)
  await assert.rejects(f.persistConfig(f.store.configDraft), /正在保存/)
  await first
  assert.equal(requests, 1)
  assert.equal(f.store.dirty, false)
  f.store.configDraft.cfst.threads = 123
  f.store.dirty = true
  await assert.rejects(f.persistConfig(f.store.config), /未保存/)
  assert.equal(requests, 1)
  assert.equal(f.store.configDraft.cfst.threads, 123)
})

test('new settings edits do not undo domains or server normalization from a pending save', async () => {
  const f = fixture()
  const pending = deferred()
  f.context.api = () => pending.promise
  const save = f.persistConfig({ cfst: { threads: 200 }, domains: [{ host: 'new.example' }] })
  f.store.configDraft.cfst.threads = 123
  pending.resolve({ cfst: { threads: 200, normalized: true }, domains: [{ host: 'new.example' }] })
  await save
  assert.equal(f.store.configDraft.cfst.threads, 123)
  assert.equal(f.store.configDraft.cfst.normalized, true)
  assert.equal(f.store.configDraft.domains[0].host, 'new.example')
  assert.equal(f.store.dirty, true)
})

test('canceling refresh keeps the settings draft without reading the API', async () => {
  const f = fixture()
  f.store.configDraft.cfst.threads = 123
  f.store.dirty = true
  f.context.confirmAction = () => Promise.resolve(false)
  f.context.api = () => { throw new Error('refresh should not request data') }
  await f.handleClick({ target: { closest: selector => selector === '[data-action]' ? { dataset: { action: 'refresh' } } : null } })
  assert.equal(f.store.configDraft.cfst.threads, 123)
  assert.equal(f.store.dirty, true)
})

test('domain save failures produce a toast instead of an unhandled rejection', async () => {
  const f = fixture()
  const pending = deferred()
  f.context.saveDomain = () => Promise.reject(new Error('duplicate domain'))
  f.context.toast = (title, message, type) => pending.resolve({ title, message, type })
  f.listeners.click({ target: { closest: selector => selector === '[data-save-domain]' ? { dataset: { saveDomain: 'new' } } : null } })
  const result = await pending.promise
  assert.equal(result.message, 'duplicate domain')
  assert.equal(result.type, 'error')
})

test('opening a page lists paused domains safely; polling does not reopen the notice', async () => {
  const f = fixture()
  const modals = []
  f.context.openModal = html => modals.push(html)
  f.store.state.maintenanceFailureLimit = 30
  f.store.state.maintenancePaused = [{ host: '<offline.example>', failureStreak: 131 }]
  vm.runInContext('showMaintenancePauseNotice()', f.context)
  assert.equal(modals.length, 1)
  assert.match(modals[0], /部分站点已暂停维护/)
  assert.match(modals[0], /&lt;offline.example&gt;/)
  assert.match(modals[0], /131/)
  f.context.api = () => Promise.resolve(f.store.state)
  await f.refreshStatus()
  await f.refreshStatus()
  assert.equal(modals.length, 1)
  // A fresh page opening calls the notice again, without a persistent dismissal.
  vm.runInContext('showMaintenancePauseNotice()', f.context)
  assert.equal(modals.length, 2)
})

test('no paused domains means no popup; resume requires explicit confirmation', async () => {
  const f = fixture()
  let popups = 0, requests = 0
  f.context.openModal = () => popups++
  vm.runInContext('showMaintenancePauseNotice()', f.context)
  assert.equal(popups, 0)
  f.store.config.domains = [{ host: 'offline.example', enabled: true }]
  f.store.state.maintenancePaused = [{ host: 'offline.example', failureStreak: 30 }]
  f.context.confirmAction = () => Promise.resolve(false)
  f.context.api = () => { requests++; return Promise.resolve({}) }
  await vm.runInContext('resumeDomainMaintenance(0)', f.context)
  assert.equal(requests, 0)
  f.context.confirmAction = () => Promise.resolve(true)
  f.context.toast = () => {}
  f.context.api = (path, options) => {
    requests++
    if (path === '/api/domain-resume') {
      assert.equal(options.method, 'POST')
      assert.deepEqual(JSON.parse(options.body), { host: 'offline.example' })
      return Promise.resolve({ status: 'resumed' })
    }
    return Promise.resolve({ maintenancePaused: [], domainHealth: { 'offline.example': { failureStreak: 0 } } })
  }
  await vm.runInContext('resumeDomainMaintenance(0)', f.context)
  assert.equal(requests, 2)
  assert.equal(f.store.state.maintenancePaused.length, 0)
})
