import { describe, it, expect } from 'vitest'
import { pickGenieAgent, genieRequest, routingWords, GENIE_SUGGESTIONS } from './genie.js'

const agents = [
  { id: 'planner', name: 'Planner', description: 'Plans your day and calendar' },
  { id: 'weather-watch', name: 'Weather', description: 'Local forecast and alerts', tags: ['forecast'] },
  { id: 'cio', name: 'Tech advisor', description: 'Software architecture and cloud' },
]

describe('pickGenieAgent', () => {
  it('prefers a gateway Genie whenever one exists', () => {
    const withGenie = [...agents, { id: 'genie', name: 'Genie' }]
    expect(pickGenieAgent(withGenie, 'will it rain tomorrow?').id).toBe('genie')
  })
  it('routes by vocabulary overlap, aliases included', () => {
    expect(pickGenieAgent(agents, 'Will it rain tomorrow?').id).toBe('weather-watch')
    expect(pickGenieAgent(agents, 'How should I structure the software?').id).toBe('cio')
    expect(pickGenieAgent(agents, 'What is on my calendar?').id).toBe('planner')
  })
  it('falls back to the first agent when nothing matches, and null when there are none', () => {
    expect(pickGenieAgent(agents, 'hello').id).toBe('planner')
    expect(pickGenieAgent([], 'hello')).toBeNull()
  })
})

describe('genieRequest', () => {
  it('trims and rejects empty text', () => {
    expect(genieRequest('  ')).toBeNull()
    expect(genieRequest(' hi ')?.text).toBe('hi')
  })
})

describe('routingWords', () => {
  it('drops short tokens and punctuation', () => {
    expect([...routingWords('Is it OK, weather-wise?')]).toEqual(['weather', 'wise'])
  })
  it('ships the same suggestions as the iPhone quick question', () => {
    expect(GENIE_SUGGESTIONS).toHaveLength(3)
  })
})
