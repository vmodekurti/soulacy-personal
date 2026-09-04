import { describe, it, expect } from 'vitest';
import { sourceKind, needsChecksum, permissionLines, credentialLines, statusInfo, riskSummary, securityVerdict, securityFindingLines, migrationLines } from './pluginmanage.js';

describe('sourceKind', () => {
  it('classifies sources', () => {
    expect(sourceKind('https://github.com/acme/plug.git')).toBe('git');
    expect(sourceKind('git@github.com:acme/plug.git')).toBe('git');
    expect(sourceKind('https://example.com/plug-1.0.tar.gz')).toBe('archive');
    expect(sourceKind('/home/me/plug.zip')).toBe('archive');
    expect(sourceKind('/home/me/my-plugin')).toBe('dir');
    expect(sourceKind('')).toBe('');
  });
  it('requires checksums only for archives', () => {
    expect(needsChecksum('/x/p.tar.gz')).toBe(true);
    expect(needsChecksum('https://github.com/a/b.git')).toBe(false);
  });
});

describe('permissionLines', () => {
  it('renders scoped grants', () => {
    const lines = permissionLines([{ cap: 'vector.search', agents: ['assistant'] }]);
    expect(lines[0]).toContain('vector.search');
    expect(lines[0]).toContain('agents: assistant');
  });
  it('flags unscoped grants loudly', () => {
    expect(permissionLines([{ cap: 'channel.send' }])[0]).toContain('UNSCOPED');
  });
  it('handles empty', () => {
    expect(permissionLines(undefined)).toEqual([]);
  });
});

describe('credentialLines', () => {
  it('renders vault refs', () => {
    expect(credentialLines([{ key: 'API_TOKEN', from: 'p/api_token' }])[0])
      .toBe('API_TOKEN ← vault: p/api_token');
  });
});

describe('statusInfo', () => {
  it('prioritises re-approval over enabled state', () => {
    expect(statusInfo({ enabled: true, needs_reapproval: true }).cls).toBe('warn');
    expect(statusInfo({ enabled: false }).cls).toBe('muted');
    expect(statusInfo({ enabled: true }).cls).toBe('ok');
  });
});

describe('riskSummary', () => {
  it('summarises requests', () => {
    const s = riskSummary({ permissions: [{ cap: 'a.b' }], credentials: [{ key: 'K' }], has_gui: true });
    expect(s).toContain('1 capability');
    expect(s).toContain('1 credential');
    expect(s).toContain('GUI panel');
  });
  it('benign when nothing requested', () => {
    expect(riskSummary({})).toContain('no capabilities');
  });
});

describe('securityVerdict (E20)', () => {
  it('returns null without a report', () => {
    expect(securityVerdict(null)).toBeNull();
    expect(securityVerdict(undefined)).toBeNull();
  });
  it('maps verdicts to badges', () => {
    expect(securityVerdict({ verdict: 'pass' }).cls).toBe('ok');
    expect(securityVerdict({ verdict: 'caution' }).cls).toBe('warn');
    expect(securityVerdict({ verdict: 'danger' }).cls).toBe('danger');
  });
});

describe('securityFindingLines (E20)', () => {
  it('sorts critical first and formats file:line', () => {
    const lines = securityFindingLines({ findings: [
      { check: 'llm_audit', severity: 'info', message: 'audit skipped: no LLM available' },
      { check: 'static', severity: 'critical', file: 'tool.py', line: 3, message: 'eval()' },
      { check: 'dry_run', severity: 'warning', message: 'wrote files' },
    ]});
    expect(lines[0]).toContain('CRITICAL');
    expect(lines[0]).toContain('tool.py:3');
    expect(lines[1]).toContain('WARNING');
    expect(lines[2]).toContain('audit skipped');
  });
  it('empty report renders nothing', () => {
    expect(securityFindingLines({})).toEqual([]);
  });
});

describe('migrationLines (Story 17)', () => {
  it('formats name + collapsed sql with truncation', () => {
    const lines = migrationLines([
      { name: '001_items', up_sql: 'CREATE TABLE\n  plugin_x_items (id INTEGER)' },
      { name: '002_big', up_sql: 'CREATE TABLE plugin_x_big (' + 'col TEXT, '.repeat(30) + 'z TEXT)' },
    ]);
    expect(lines[0]).toBe('001_items: CREATE TABLE plugin_x_items (id INTEGER)');
    expect(lines[1].endsWith('…')).toBe(true);
  });
  it('empty input renders nothing', () => {
    expect(migrationLines(undefined)).toEqual([]);
  });
});
