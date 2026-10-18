#!/usr/bin/env node
import { mkdtempSync, mkdirSync, statSync, writeFileSync, copyFileSync, readdirSync } from 'node:fs'

import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'
import { tmpdir } from 'node:os'
import { basename, join } from 'node:path'
import { spawn } from 'node:child_process'

async function loadPlaywright() {
  if (process.env.SOULACY_PLAYWRIGHT_REQUIRE_FROM) {
    try {
      const require = createRequire(join(process.env.SOULACY_PLAYWRIGHT_REQUIRE_FROM, 'package.json'))
      return require('playwright')
    } catch {
      // Fall back to normal resolution below.
    }
  }
  try {
    return await import('playwright')
  } catch (err) {
    console.log('skip browser render smoke: playwright is not installed')
    process.exit(77)
  }
}

function sleep(ms) {
  return new Promise(resolve => setTimeout(resolve, ms))
}

async function waitFor(url, apiKey) {
  for (let i = 0; i < 80; i += 1) {
    try {
      const res = await fetch(`${url}/api/v1/health`, { headers: { Authorization: `Bearer ${apiKey}` } })
      if (res.ok) return
    } catch {
      // keep waiting
    }
    await sleep(250)
  }
  throw new Error(`gateway did not become healthy at ${url}`)
}

const root = fileURLToPath(new URL('..', import.meta.url))
const bin = process.env.SOULACY_BROWSER_RENDER_BIN || join(root, 'bin', 'soulacy')
const host = process.env.SOULACY_BROWSER_RENDER_HOST || '127.0.0.1'
const port = process.env.SOULACY_BROWSER_RENDER_PORT || '18894'
const apiKey = process.env.SOULACY_BROWSER_RENDER_API_KEY || 'sy_browser_render_smoke'
const baseURL = `http://${host}:${port}`
const workspace = process.env.SOULACY_BROWSER_RENDER_WORKSPACE || mkdtempSync(join(tmpdir(), 'soulacy-browser-render-'))

const outDir = process.env.SOULACY_BROWSER_RENDER_OUT || join(workspace, 'screenshots')
mkdirSync(join(workspace, 'agents'), { recursive: true })
mkdirSync(join(workspace, 'logs'), { recursive: true })
mkdirSync(outDir, { recursive: true })

// Copy sample embedded agent templates into workspace so the GUI is populated with rich workflows & configurations
const embeddedDir = join(root, 'internal', 'templates', 'embedded')
try {
  const files = process.env.SOULACY_BROWSER_RENDER_SKIP_TEMPLATES === '1' ? [] : readdirSync(embeddedDir)
  for (const file of files) {
    if (file.endsWith('.yaml')) {
      copyFileSync(join(embeddedDir, file), join(workspace, 'agents', file))
    }
  }
} catch (e) {
  console.log('could not copy embedded templates:', e.message)
}


writeFileSync(join(workspace, 'config.yaml'), `server:
  host: "${host}"
  port: ${port}
  api_key: "${apiKey}"
  gui_enabled: true
llm:
  default_provider: ollama
  providers:
    ollama:
      base_url: "http://localhost:11434"
      model: "llama3.2"
agent_dirs:
  - "${workspace}/agents"
log:
  file: "${workspace}/logs/soulacy.log"
`)

const child = spawn(bin, ['serve'], {
  cwd: root,
  env: {
    ...process.env,
    SOULACY_WORKSPACE: workspace,
    SOULACY_CONFIG_PATH: join(workspace, 'config.yaml'),
  },
  stdio: ['ignore', 'pipe', 'pipe'],
})

let stderr = ''
child.stderr.on('data', chunk => {
  stderr += chunk.toString()
})

let browser
try {
  await waitFor(baseURL, apiKey)
  const { chromium } = await loadPlaywright()
  browser = await chromium.launch({ headless: true, ...(process.env.SOULACY_BROWSER_EXECUTABLE ? { executablePath: process.env.SOULACY_BROWSER_EXECUTABLE } : {}) })
  const viewport = { width: Number(process.env.SOULACY_BROWSER_WIDTH || 1440), height: Number(process.env.SOULACY_BROWSER_HEIGHT || 960) }
  const page = await browser.newPage({ viewport })
  await page.addInitScript(key => {
    window.localStorage.setItem('soulacy_api_key', key)
    window.localStorage.setItem('soulacy-onboarding-seen', '1')
  }, apiKey)
  const consoleErrors = []
  page.on('console', msg => {
    if (msg.type() === 'error') consoleErrors.push(msg.text())
  })
  page.on('pageerror', err => {
    consoleErrors.push(err.message)
  })
  const networkErrors = []
  page.on('response', response => {
    const status = response.status()
    const pathname = new URL(response.url()).pathname
    const expectedEmptyMetrics = status === 404 &&
      response.request().method() === 'GET' &&
      /^\/api\/v1\/runs\/[^/]+\/metrics$/.test(pathname)
    if (status >= 400 && !/\/favicon(?:\.|$)/i.test(pathname) && !expectedEmptyMetrics) {
      networkErrors.push(`HTTP ${status} ${response.request().method()} ${response.url()}`)
    }
  })

  const routes = [
    ['dashboard', '/'],
    ['studio', '/#studio'],
    ['studio_workflow', '/#studio/github-issue-triage'],
    ['agents', '/#agents'],
    ['agent_config', '/#agents/github-issue-triage'],
    ['chat', '/#chat'],
    ['channels', '/#channels'],
    ['knowledge', '/#knowledge'],
    ['schedule', '/#schedule'],
    ['providers', '/#providers'],
    ['activity', '/#activity'],
    ['skills', '/#skills'],
    ['config', '/#config'],
  ]


  const manifest = {
    generated_at: new Date().toISOString(),
    base_url: baseURL,
    viewport,
    routes: [],
  }

  for (const [name, path] of routes) {
    consoleErrors.length = 0
    networkErrors.length = 0
    await page.goto(`${baseURL}${path}`, { waitUntil: 'networkidle', timeout: 30000 })
    // Route imports and initial data loads can begin after networkidle.
    // A loading shell is not evidence that the destination works.
    await page.waitForTimeout(800)
    if (name === 'config') await page.locator('#default-provider').waitFor({ state: 'visible', timeout: 30000 })
    if (name === 'chat') await page.locator('.empty-title, .msg-row').first().waitFor({ state: 'visible', timeout: 15000 })
    const screenshotPath = join(outDir, `${name}.png`)
    await page.screenshot({ path: screenshotPath, fullPage: true })
    const text = await page.locator('body').innerText({ timeout: 10000 })
    if (!text || text.length < 20) {
      throw new Error(`${name} rendered too little text`)
    }
    const serious = [
      ...networkErrors,
      ...consoleErrors.filter(line =>
        !/favicon|ResizeObserver loop|Failed to load resource: the server responded with a status of 404/i.test(line)
      ),
    ]
    if (serious.length) {
      throw new Error(`${name} console error: ${serious[0]}`)
    }
    const file = basename(screenshotPath)
    const stats = statSync(screenshotPath)
    manifest.routes.push({
      name,
      path,
      screenshot: file,
      bytes: stats.size,
      text_length: text.length,
    })
  }
  if (process.env.SOULACY_BROWSER_INTERACTIONS === '1') {
    // Deterministic UI fixture: no model calls, credentials, or external delivery.
    const agentID = 'portal-review-fixture'
    const created = await page.request.post(`${baseURL}/api/v1/agents`, {
      headers: { Authorization: `Bearer ${apiKey}` },
      data: { id: agentID, name: 'Portal review', enabled: true, trigger: 'channel', channels: ['http'],
        system_prompt: 'Answer clearly.', builtins: [], llm: { provider: 'ollama', model: 'llama3.2' } },
    })
    if (!created.ok()) throw new Error(`Could not create UI fixture: ${created.status()} ${await created.text()}`)
    const fixtureReply = 'Partial result with math $x^2$ and a diagram:\n\n```mermaid\ngraph LR\n A[Start] --> B[Review]\n```'
    await page.route(`${baseURL}/api/v1/chat`, async route => {
      const body = route.request().postDataJSON()
      await route.fulfill({ json: { reply: fixtureReply, session_id: body.session_id, run_id: 'ui-fixture',
        metadata: { outcome: 'incomplete', reasoning_degraded: 'true', outcome_summary: 'The source is temporarily unavailable.' } } })
    })
    await page.goto(`${baseURL}/#chat`, { waitUntil: 'networkidle' })
    const input = page.locator('textarea:visible').first()
    await input.fill('Check the result')
    await page.getByRole('button', { name: 'Send message', exact: true }).click()
    await page.getByText('Work is incomplete', { exact: true }).waitFor()
    await page.locator('.katex').first().waitFor()
    await page.locator('.mermaid-chart svg').first().waitFor({ timeout: 15000 })
    for (const label of ['Start', 'Review']) {
      await page.locator('.mermaid-chart svg text').filter({ hasText: label }).first().waitFor()
    }
    await page.screenshot({ path: join(outDir, 'chat-incomplete.png'), fullPage: true })
    await page.reload({ waitUntil: 'networkidle' })
    await page.getByText('Work is incomplete', { exact: true }).waitFor()
    await page.goto(`${baseURL}/#config`, { waitUntil: 'networkidle' })
    await page.locator('#default-provider').waitFor()
    if (await page.locator('#max-turns').count()) throw new Error('Advanced controls visible in Everyday settings')
    await page.screenshot({ path: join(outDir, 'settings-everyday.png'), fullPage: true })
    await page.getByRole('group', { name: 'Settings level', exact: true }).getByRole('button', { name: 'Advanced', exact: true }).click()
    await page.locator('#max-turns').fill('512')
    await page.getByRole('group', { name: 'Settings level', exact: true }).getByRole('button', { name: 'Everyday', exact: true }).click()
    await page.getByRole('group', { name: 'Settings level', exact: true }).getByRole('button', { name: 'Advanced', exact: true }).click()
    if (await page.locator('#max-turns').inputValue() !== '512') throw new Error('Switching settings views lost pending edits')
    // Save one basic choice against the real API and check unrelated settings on disk.
    await page.locator('#max-turns').fill('256')
    await page.getByRole('group', { name: 'Settings level', exact: true }).getByRole('button', { name: 'Everyday', exact: true }).click()
    const beforeSettings = await (await page.request.get(`${baseURL}/api/v1/config`, { headers: { Authorization: `Bearer ${apiKey}` } })).json()
    await page.getByText('Remember useful details from conversations', { exact: true }).click()
    const [configSaved] = await Promise.all([
      page.waitForResponse(response => response.url() === `${baseURL}/api/v1/config` && response.request().method() === 'PATCH'),
      page.getByRole('button', { name: 'Save changes', exact: true }).click(),
    ])
    if (!configSaved.ok()) throw new Error('Everyday settings did not save')
    const settingsPatch = configSaved.request().postDataJSON()
    if (JSON.stringify(Object.keys(settingsPatch)) !== '["memory"]') throw new Error('Everyday save rewrote unrelated settings')
    const afterSettings = await (await page.request.get(`${baseURL}/api/v1/config`, { headers: { Authorization: `Bearer ${apiKey}` } })).json()
    for (const section of ['runtime', 'costs', 'llm', 'executor']) {
      if (JSON.stringify(beforeSettings[section]) !== JSON.stringify(afterSettings[section])) throw new Error(`Everyday save changed ${section}`)
    }
    await page.getByRole('group', { name: 'Settings level', exact: true }).getByRole('button', { name: 'Advanced', exact: true }).click()
    const frequency = page.getByRole('combobox', { name: 'Update check frequency' })
    await frequency.waitFor()
    if (await frequency.inputValue() !== '6h') throw new Error('Default update frequency is blank or incorrect')
    if (await page.locator('.json-view').count()) throw new Error('Technical configuration is expanded by default')
    await page.getByRole('button', { name: 'Show technical details' }).click()
    await page.locator('.json-view').waitFor()
    await page.getByRole('button', { name: 'Hide technical details' }).click()
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1)
    if (overflow) throw new Error('Configuration overflows the viewport')
    if (viewport.width < 640) {
      const columns = await page.locator('.form-col .field-row').first().evaluate(node => getComputedStyle(node).gridTemplateColumns.split(' ').length)
      if (columns !== 1) throw new Error('Mobile configuration fields are not stacked')
    }
    await page.goto(`${baseURL}/#agents`, { waitUntil: 'networkidle' })
    await page.getByRole('button', { name: '+ New Agent', exact: true }).click()
    const defaults = await (await page.request.get(`${baseURL}/api/v1/providers`, { headers: { Authorization: `Bearer ${apiKey}` } })).json()
    const providerSelect = page.locator('.fields select[data-tooltip]').filter({ has: page.locator('option', { hasText: defaults.default_provider }) }).first()
    if (await providerSelect.inputValue() !== defaults.default_provider) throw new Error('New agent did not use workspace provider')
    const defaultModel = defaults.providers[defaults.default_provider].model
    const modelSelect = page.locator('.fields select').filter({ has: page.locator(`option[value="${defaultModel}"]`) }).first()
    if (await modelSelect.inputValue() !== defaultModel) throw new Error('New agent did not use configured default model')
    await page.locator('.agent-card').filter({ hasText: 'Portal review' }).click()
    if (await page.locator('.fields input[data-tooltip^="0 inherits the workspace default"]').count()) throw new Error('Agent editor did not start in Everyday mode')
    await page.getByRole('group', { name: 'Settings level', exact: true }).getByRole('button', { name: 'Advanced', exact: true }).click()
    const turns = page.locator('.fields input[data-tooltip^="0 inherits the workspace default"]')
    const output = page.locator('.fields input[data-tooltip^="0 inherits the workspace output allowance"]')
    await turns.fill('512')
    await output.fill('32768')
    await page.getByRole('group', { name: 'Settings level', exact: true }).getByRole('button', { name: 'Everyday', exact: true }).click()
    await page.getByRole('group', { name: 'Settings level', exact: true }).getByRole('button', { name: 'Advanced', exact: true }).click()
    if (await turns.inputValue() !== '512' || await output.inputValue() !== '32768') throw new Error('Switching agent views lost edits')
    if (!await turns.evaluate(node => node.checkValidity()) || !await output.evaluate(node => node.checkValidity())) {
      throw new Error('Agent editor still imposes a hidden turn/output ceiling')
    }
    const [saved] = await Promise.all([
      page.waitForResponse(response => response.url() === `${baseURL}/api/v1/agents/${agentID}` && response.request().method() === 'PUT', { timeout: 15000 }),
      page.locator('.editor-actions .save-action').click({ timeout: 10000 }),
    ]).catch(async error => {
      await page.screenshot({ path: join(outDir, 'agent-save-failure.png'), fullPage: true })
      writeFileSync(join(outDir, 'agent-save-failure.txt'), await page.locator('body').innerText())
      throw error
    })
    if (!saved.ok()) throw new Error(`Agent limit settings could not be saved: ${await saved.text()}`)
    const persisted = await page.request.get(`${baseURL}/api/v1/agents/${agentID}`, { headers: { Authorization: `Bearer ${apiKey}` } })
    const definition = await persisted.json()
    const agent = definition.agent || definition
    if (agent.max_turns !== 512 || agent.llm.max_tokens !== 32768) throw new Error('Agent limits did not persist')
    await page.getByLabel('Run allowances').waitFor({ timeout: 15000 })
    await page.getByLabel('Run allowances').scrollIntoViewIfNeeded()
    if (viewport.width < 640) {
      const visible = await page.getByLabel('Run allowances').evaluate(node => {
        const r = node.getBoundingClientRect()
        return node.contains(document.elementFromPoint(r.left + r.width / 2, Math.max(65, Math.min(innerHeight - 70, r.top + r.height / 2))))
      })
      if (!visible) throw new Error('Mobile editor actions cover the run allowances')
    }
    await page.screenshot({ path: join(outDir, 'agent-allowances.png'), fullPage: true })
    manifest.interactions = ['incomplete reply notice', 'notice after reload', 'math rendering', 'diagram rendering', 'update frequency', 'technical details toggle', 'configuration viewport fit', 'edit and persist generous agent limits', 'run allowances panel', 'everyday settings by default', 'pending edits survive view changes', 'basic saves preserve advanced configuration', 'new agents use configured model defaults']
  }
  writeFileSync(join(outDir, 'manifest.json'), `${JSON.stringify(manifest, null, 2)}\n`)
  writeFileSync(join(outDir, 'index.md'), renderMarkdownIndex(manifest))
  console.log(`browser render smoke passed; screenshots: ${outDir}`)
} finally {
  if (browser) await browser.close()
  child.kill('SIGTERM')
  await new Promise(resolve => {
    child.once('exit', resolve)
    setTimeout(resolve, 1500)
  })
}

if (stderr.includes('panic:') || stderr.includes('fatal error:')) {
  throw new Error(`gateway emitted fatal stderr: ${stderr.slice(-1000)}`)
}

function renderMarkdownIndex(manifest) {
  const rows = manifest.routes.map(route =>
    `| ${route.name} | \`${route.path}\` | [${route.screenshot}](${route.screenshot}) | ${route.bytes} | ${route.text_length} |`
  ).join('\n')
  return `# Soulacy GUI Screenshot Evidence

Generated: ${manifest.generated_at}

Viewport: ${manifest.viewport.width}x${manifest.viewport.height}

| Route | Path | Screenshot | Bytes | Text length |
|---|---|---|---:|---:|
${rows}
`
}
