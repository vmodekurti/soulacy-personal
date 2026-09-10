import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const schedule = readFileSync(fileURLToPath(new URL('./Schedule.svelte', import.meta.url)), 'utf8')

describe('automation run history', () => {
  it('requests the automation-only durable ledger globally and per agent', () => {
    expect(schedule).toContain("api.runs.ledger({ scope: 'automation'")
    expect(schedule).toContain("api.runs.ledger({ agentId: a.id, scope: 'automation'")
  })

  it('labels scheduled and manually started executions distinctly', () => {
    expect(schedule).toContain('Automation history')
    expect(schedule).toContain("return 'Manual · Run now'")
    expect(schedule).toContain("return 'Scheduled · Cron'")
    expect(schedule).toContain('No scheduled or manually started automation runs have been recorded yet.')
  })

  it('keeps explicit scheduled failures in the compatibility history path', () => {
    expect(schedule).toContain("e.type === 'schedule.run_failed'")
    expect(schedule).toContain('scheduleFailure.error')
  })

  it('lets operators inspect the retained result and jump to the exact logs', () => {
    expect(schedule).toContain("expandedRecentRuns")
    expect(schedule).toContain("'View result'")
    expect(schedule).toContain('Open logs')
    expect(schedule).toContain('{run.output}')
  })
})
