// Story 4 — Logs UI: ANSI stripping and severity classification.
import { describe, it, expect } from 'vitest'
import { stripAnsi, logLevel } from './logutils.js'

describe('stripAnsi', () => {
  it('strips SGR color codes', () => {
    expect(stripAnsi('\x1b[31mERROR\x1b[0m something failed'))
      .toBe('ERROR something failed')
  })

  it('strips 256-color and bold sequences', () => {
    expect(stripAnsi('\x1b[1;38;5;196mfatal\x1b[0m')).toBe('fatal')
  })

  it('strips cursor/erase CSI sequences', () => {
    expect(stripAnsi('\x1b[2K\x1b[1Gprogress 50%')).toBe('progress 50%')
  })

  it('strips OSC title sequences', () => {
    expect(stripAnsi('\x1b]0;my-title\x07hello')).toBe('hello')
  })

  it('leaves plain lines untouched', () => {
    const line = '2026-06-06T10:00:00Z\tINFO\tgateway started'
    expect(stripAnsi(line)).toBe(line)
  })
})

describe('logLevel', () => {
  it('detects zap JSON levels', () => {
    expect(logLevel('{"level":"error","msg":"boom"}')).toBe('error')
    expect(logLevel('{"level":"warn","msg":"careful"}')).toBe('warn')
    expect(logLevel('{"level":"info","msg":"ok"}')).toBe('info')
    expect(logLevel('{"level":"debug","msg":"verbose"}')).toBe('debug')
  })

  it('detects zap console (tab-separated) levels', () => {
    expect(logLevel('2026-06-06T10:00:00Z\tERROR\tgateway\tboom')).toBe('error')
    expect(logLevel('2026-06-06T10:00:00Z\tINFO\tgateway\tstarted')).toBe('info')
  })

  it('detects logfmt levels', () => {
    expect(logLevel('ts=2026-06-06 level=warn msg="disk almost full"')).toBe('warn')
  })

  it('detects bracketed levels', () => {
    expect(logLevel('[ERROR] connection refused')).toBe('error')
  })

  it('classifies ANSI-colored lines by their stripped content', () => {
    expect(logLevel('\x1b[31m{"level":"error","msg":"red"}\x1b[0m')).toBe('error')
  })

  it('returns other for unrecognised lines', () => {
    expect(logLevel('plain stdout output with no level')).toBe('other')
    // Mentions of the word in message bodies must not misclassify
    expect(logLevel('request to /api/v1/agents completed in 12ms')).toBe('other')
  })
})
