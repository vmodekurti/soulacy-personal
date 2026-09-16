import { describe, it, expect } from 'vitest'
import { classifyRequest, isQuestion } from './intent.js'

// The product used to make the person choose between "ask" and "build" before
// they had typed anything. These pin the rule that replaced that choice:
// recurrence, not vocabulary.
describe('what a request actually is', () => {
  it('builds something when it should keep happening', () => {
    for (const text of [
      'Every morning, summarise the news in my industry and send it to me.',
      'Each Friday, write a short summary of what I worked on.',
      'Send me a market recap daily.',
      'Check a web page regularly and tell me when the price changes.',
      'From now on, flag calendar conflicts.',
    ]) {
      expect(classifyRequest(text).kind, text).toBe('build')
    }
  })

  it('builds something for a standing instruction', () => {
    for (const text of [
      'Remind me to follow up on unanswered emails.',
      'Alert me when the build breaks.',
      'Watch my calendar and warn me about clashes.',
      'Let me know when the price drops.',
    ]) {
      expect(classifyRequest(text).kind, text).toBe('build')
    }
  })

  it('just answers a question', () => {
    for (const text of [
      'What happened in the market today?',
      'How do I connect Telegram?',
      'Summarise this article for me',
      'Who is on my calendar tomorrow?',
      'Explain what Studio does',
    ]) {
      expect(classifyRequest(text).kind, text).toBe('ask')
    }
  })

  // A time inside a question is not a standing order. Getting this wrong would
  // build an agent for someone who asked what time the market closes.
  it('does not mistake a time in a question for a schedule', () => {
    for (const text of [
      'What time does the market close at 4pm or 5pm?',
      'Is the market open on Friday?',
      'Are you free at 3?',
    ]) {
      expect(classifyRequest(text).kind, text).toBe('ask')
    }
  })

  it('treats a bare time with an instruction as a schedule', () => {
    expect(classifyRequest('Send me a market summary at 5pm').kind).toBe('build')
    expect(classifyRequest('Give me a Friday recap of my week').kind).toBe('build')
  })

  it('says why it decided, so the person can correct it', () => {
    expect(classifyRequest('Every morning, brief me').reason).toBe('every')
    expect(classifyRequest('Remind me to call Sam').reason).toBe('remind me')
  })

  it('treats nothing as a question rather than building on silence', () => {
    expect(classifyRequest('').kind).toBe('ask')
    expect(classifyRequest('   ').kind).toBe('ask')
    expect(classifyRequest(null).kind).toBe('ask')
  })

  it('recognises questions by shape as well as punctuation', () => {
    expect(isQuestion('what is this')).toBe(true)
    expect(isQuestion('tell me about my week')).toBe(true)
    expect(isQuestion('build me a thing')).toBe(false)
  })
})
