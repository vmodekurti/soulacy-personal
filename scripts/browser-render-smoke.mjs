#!/usr/bin/env node
import { mkdtempSync, mkdirSync, statSync, writeFileSync, copyFileSync, readdirSync } from 'node:fs'

import { createRequire } from 'node:module'
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

const root = new URL('..', import.meta.url).pathname
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
  const files = readdirSync(embeddedDir)
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
  browser = await chromium.launch({ headless: true })
  const page = await browser.newPage({ viewport: { width: 1440, height: 960 } })
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
    if (status >= 400 && !/\/favicon(?:\.|$)/i.test(new URL(response.url()).pathname)) {
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
    viewport: { width: 1440, height: 960 },
    routes: [],
  }

  for (const [name, path] of routes) {
    consoleErrors.length = 0
    networkErrors.length = 0
    await page.goto(`${baseURL}${path}`, { waitUntil: 'networkidle', timeout: 30000 })
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
