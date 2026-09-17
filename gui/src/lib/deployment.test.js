import { describe, it, expect } from 'vitest'
import { deploymentFrom, limitsOf, blockersFor, summaryOf } from './deployment.js'

const paas = {
  deployment: {
    platform: 'Railway',
    platform_kind: 'paas',
    workspace: '/data/soulspace',
    capabilities: [
      { id: 'agent_shell', name: 'Agents can run shell commands', available: false,
        detail: 'no agent holds the system grant',
        workaround: 'install Skills and MCP servers with package_install' },
      { id: 'node_runtime', name: 'Node-based MCP servers', available: true, detail: 'v20.20.2 at /usr/local/bin/node' },
      { id: 'browser_automation', name: 'Browser automation', available: false,
        detail: 'the image does not carry Chromium’s shared libraries',
        workaround: 'they need root — until then, use --cdp-endpoint' },
    ],
  },
}

describe('deploymentFrom', () => {
  it('reads the report and indexes it by capability', () => {
    const rep = deploymentFrom(paas)
    expect(rep.platform).toBe('Railway')
    expect(rep.byID.node_runtime.available).toBe(true)
  })

  // An older gateway does not report this section. The screen must render
  // without it rather than break on a missing key.
  it('is null when the gateway does not report one', () => {
    expect(deploymentFrom({})).toBeNull()
    expect(deploymentFrom(null)).toBeNull()
    expect(deploymentFrom({ deployment: {} })).toBeNull()
  })
})

describe('limitsOf', () => {
  it('returns only what is unavailable, since that is what needs saying', () => {
    const ids = limitsOf(deploymentFrom(paas)).map(c => c.id)
    expect(ids).toEqual(['agent_shell', 'browser_automation'])
  })

  it('is empty for a deployment with no report', () => {
    expect(limitsOf(null)).toEqual([])
  })
})

describe('blockersFor', () => {
  const rep = deploymentFrom(paas)

  it('blocks a template whose requirement this deployment lacks', () => {
    const tpl = { id: 'browser', requires: ['node_runtime', 'browser_automation'] }
    const blocked = blockersFor(tpl, rep)
    expect(blocked.map(b => b.id)).toEqual(['browser_automation'])
    // The screen shows the deployment's own words rather than inventing any.
    expect(blocked[0].workaround).toContain('--cdp-endpoint')
  })

  // The remote-browser template is the point of the exercise: it needs nothing
  // local, so it must stay available exactly where the local one does not.
  it('does not block the remote browser template on the same deployment', () => {
    expect(blockersFor({ id: 'browser_remote', requires: ['node_runtime'] }, rep)).toEqual([])
  })

  it('treats an unknown requirement as not a blocker', () => {
    // An older gateway that reports fewer capabilities should not grey out
    // every template on a guess.
    expect(blockersFor({ id: 'x', requires: ['telepathy'] }, rep)).toEqual([])
  })

  it('has nothing to say without a report', () => {
    expect(blockersFor({ requires: ['node_runtime'] }, null)).toEqual([])
  })
})

describe('summaryOf', () => {
  it('counts what works', () => {
    expect(summaryOf(deploymentFrom(paas))).toBe('Railway · 1 of 3 available')
  })
})
