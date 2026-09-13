// @vitest-environment jsdom
import { describe, it, expect, vi, afterEach } from 'vitest'
import { mount, unmount } from 'svelte'
import Reports from './Reports.svelte'
import { api } from '../lib/api.js'
import { apiKey } from '../lib/stores.js'
import { reportCSV, reportMarkdown, reportAttention, validateReport, downloadReport } from '../lib/operationsReport.js'
vi.mock('../lib/api.js', () => ({ api: { operationsReport: vi.fn() } }))
vi.mock('../lib/operationsReport.js', async original => ({ ...await original(), downloadReport: vi.fn() }))

const activity = { events: 10, requests: 4, replies: 2, error_events: 1, dead_letters: 0, tool_calls: 1, sessions: 3, reply_complete_sessions: 1, error_sessions: 1, unresolved_sessions: 1 }
const usage = { requests: 5, attempts: 6, failed: 1, rejected: 1, unknown_priced: 2, total_tokens: 1600, cost_micros: 1250000 }
function fixture(window = '24h') {
  return {
    schema_version: 1, window, status: 'ready', generated_at: '2026-09-12T12:00:01Z', start: '2026-09-11T12:00:00Z', end: '2026-09-12T12:00:00Z',
    sources: { activity: { status: 'ready', detail: 'Retained activity' }, usage: { status: 'ready', detail: 'Recorded estimates' } },
    activity: { totals: { ...activity }, by_agent: [{ agent_id: 'research', ...activity }], single_request_reply_latency: { samples: 1, avg_ms: 2000, p95_ms: 2000 } },
    usage: { totals: { ...usage }, by_agent: [{ agent_id: 'research', provider: '', model: '', ...usage }], by_model: [{ agent_id: '', provider: 'p', model: 'm', ...usage }] },
    notes: ['Retained records only.', 'Costs are estimates; unknown pricing is not free.'],
  }
}
let component, target
const wait = () => new Promise(resolve => setTimeout(resolve, 0))
async function render() {
  target = document.createElement('div'); document.body.append(target)
  component = mount(Reports, { target }); await wait()
}
const button = text => [...target.querySelectorAll('button')].find(b => b.textContent === text)
afterEach(async () => {
  if (component) await unmount(component)
  component = null; target?.remove(); target = null
  apiKey.set(''); vi.resetAllMocks()
})

describe('Operations reports', () => {
  it('shows truthful session/request/cost counts and exports the displayed snapshot', async () => {
    const report = fixture(); api.operationsReport.mockResolvedValue(report)
    await render()
    expect(target.textContent).toContain('$1.2500')
    expect(target.textContent).toContain('4')
    expect(target.textContent).toContain('unknown pricing')
    expect(target.querySelector('.cards article strong').textContent).toBe('4')
    expect(target.textContent).toContain('6 provider attempts')
  })
  it('exports without making another request', async () => {
    const report = fixture(); api.operationsReport.mockResolvedValue(report); await render()
    button('CSV').click(); await wait()
    expect(downloadReport).toHaveBeenCalledWith(report, 'csv')
    expect(api.operationsReport).toHaveBeenCalledTimes(1)
  })
  it('does not show missing activity as zero or a cost-only report as complete', async () => {
    const report = fixture(); report.activity = null; report.status = 'partial'
    report.sources.activity = { status: 'unavailable', detail: 'Activity disabled' }
    api.operationsReport.mockResolvedValue(report); await render()
    expect(target.textContent).toContain('Partial data')
    expect(target.textContent).toContain('Activity disabled')
    expect(target.querySelector('.cards article strong').textContent).toBe('Unavailable')
    expect(button('CSV').disabled).toBe(false)
  })
  it('shows an empty ready database differently from unavailable data', async () => {
    const report = fixture()
    for (const key of Object.keys(report.activity.totals)) report.activity.totals[key] = 0
    for (const key of Object.keys(report.usage.totals)) report.usage.totals[key] = 0
    report.activity.by_agent = []; report.activity.single_request_reply_latency = null
    report.usage.by_agent = []; report.usage.by_model = []
    api.operationsReport.mockResolvedValue(report); await render()
    expect(target.textContent).toContain('No retained activity records')
    expect(target.textContent).toContain('No recorded model usage')
    expect(target.textContent).toContain('no qualifying samples')
    expect(target.textContent).not.toContain('NaN')
  })
  it.each([403, 404, 503])('explains HTTP %i and disables exports', async status => {
    api.operationsReport.mockRejectedValue(Object.assign(new Error('Gateway unavailable'), { status })); await render()
    expect(target.querySelector('[role=alert]')).not.toBeNull()
    expect(button('CSV').disabled).toBe(true)
    expect(target.querySelector('table')).toBeNull()
  })
  it('fails closed on malformed successful responses', async () => {
    api.operationsReport.mockResolvedValue({}); await render()
    expect(target.textContent).toContain('invalid operations report')
    expect(button('CSV').disabled).toBe(true)
  })
  it('ignores an old request after the selected window changes', async () => {
    let finishOld
    api.operationsReport.mockReturnValueOnce(new Promise(resolve => { finishOld = resolve })).mockResolvedValueOnce(fixture('7d'))
    await render()
    const select = target.querySelector('select'); select.value = '7d'; select.dispatchEvent(new Event('change')); await wait()
    finishOld(fixture()); await wait()
    button('JSON').click(); await wait()
    expect(downloadReport.mock.calls[0][0].window).toBe('7d')
    expect(api.operationsReport.mock.calls.map(args => args[0])).toEqual(['24h', '7d'])
  })
  it('clears private state on sign-in change and blocks a pending response from restoring it', async () => {
    let finish
    api.operationsReport.mockReturnValueOnce(new Promise(resolve => { finish = resolve }))
    await render(); apiKey.set('different-principal'); await wait(); finish(fixture()); await wait()
    expect(target.textContent).toContain('sign-in changed')
    expect(target.textContent).not.toContain('research')
    expect(button('CSV').disabled).toBe(true)
  })
  it('does not restore data after unmount', async () => {
    let finish
    api.operationsReport.mockReturnValueOnce(new Promise(resolve => { finish = resolve }))
    await render(); await unmount(component); component = null; finish(fixture()); await wait()
    expect(target.textContent).toBe('')
  })
  it('keeps names and notes inert', async () => {
    const report = fixture(); report.activity.by_agent[0].agent_id = '<img src="/private" onerror="bad()">'
    report.notes.push('<script>bad()</script>')
    api.operationsReport.mockResolvedValue(report); await render()
    expect(target.querySelector('img,script')).toBeNull()
    expect(target.textContent).toContain('<script>bad()</script>')
  })
})

describe('Report exports and validation', () => {
  it('flags orphan or boundary-crossing errors even without a matching started session', () => {
    const report = fixture(); report.activity.totals.error_sessions = 0
    expect(reportAttention(report).join(' ')).toContain('No matching session start')
  })
  it('retains source limitations and period in every human-readable export', () => {
    const report = fixture()
    for (const exported of [reportCSV(report), reportMarkdown(report)]) {
      expect(exported).toContain(report.start); expect(exported).toContain(report.end)
      expect(exported).toContain('Retained records only.')
      expect(exported).toContain('Recorded estimates')
    }
  })
  it('neutralizes formulas, quotes, commas, and newlines in CSV identifiers', () => {
    const report = fixture(); report.usage.by_agent[0].agent_id = ' \t=HYPERLINK("https://evil.invalid","x")\nextra'
    const csv = reportCSV(report)
    expect(csv).toContain('"\' \t=HYPERLINK(""https://evil.invalid"",""x"")\nextra"')
  })
  it('escapes Markdown media and table delimiters', () => {
    const report = fixture(); report.activity.by_agent[0].agent_id = '![track](https://evil.invalid) | <img>'
    const md = reportMarkdown(report)
    expect(md).toContain('\\!\\[track\\]\\(https://evil.invalid\\) \\| \\<img\\>')
    expect(md).not.toContain('![track](')
  })
  it.each(['wrong window', 'negative count', 'non-finite', 'missing model', 'mismatched status', 'unavailable with data'])('rejects %s', problem => {
    const report = fixture()
    if (problem === 'wrong window') report.window = '7d'
    if (problem === 'negative count') report.activity.totals.requests = -1
    if (problem === 'non-finite') report.usage.totals.cost_micros = Infinity
    if (problem === 'missing model') report.usage.by_model = [{}]
    if (problem === 'mismatched status') report.status = 'partial'
    if (problem === 'unavailable with data') report.sources.activity.status = 'unavailable'
    expect(() => validateReport(report, '24h')).toThrow()
  })
})
