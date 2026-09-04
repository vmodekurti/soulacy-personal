import { describe, it, expect } from 'vitest';
import { explainShellCommand, explainConfirmRequest } from './explainCommand.js';

describe('explainShellCommand', () => {
  it('describes the screenshot example command', () => {
    const cmd = 'cd ~/.soulacy/soulspace/vendor/stock-screener && ./venv/bin/python run_optimized_scan.py --test-mode 2>&1 | tail -60';
    const steps = explainShellCommand(cmd);
    expect(steps[0]).toMatch(/change directory to ~\/\.soulacy\/soulspace\/vendor\/stock-screener/);
    expect(steps.some((s) => /run the Python script\/command run_optimized_scan\.py/.test(s))).toBe(true);
    expect(steps.some((s) => /show the last lines/.test(s))).toBe(true);
    expect(steps).toContain('merge error output into normal output');
  });

  it('flags forced recursive delete', () => {
    const steps = explainShellCommand('rm -rf build/');
    expect(steps[0]).toMatch(/recursively delete build\//);
    expect(steps[0]).toMatch(/forced/);
  });

  it('handles env assignments and ./path binaries', () => {
    const steps = explainShellCommand('FOO=bar ./bin/node app.js');
    expect(steps[0]).toMatch(/run the Node\.js script app\.js/);
  });

  it('notes append redirection', () => {
    const steps = explainShellCommand('echo hi >> log.txt');
    expect(steps).toContain('append output to a file');
  });

  it('describes a curl to a URL', () => {
    const steps = explainShellCommand('curl https://example.com/data');
    expect(steps[0]).toMatch(/make a network request to https:\/\/example\.com\/data/);
  });

  it('returns empty for blank input', () => {
    expect(explainShellCommand('')).toEqual([]);
    expect(explainShellCommand(null)).toEqual([]);
  });
});

describe('explainConfirmRequest', () => {
  it('explains a shell_exec request with timeout', () => {
    const out = explainConfirmRequest({
      tool: 'shell_exec',
      args: { command: 'git pull --ff-only', timeout_seconds: 600 },
    });
    expect(out.summary).toMatch(/runs a shell command/);
    expect(out.steps[0]).toMatch(/run git pull/);
    expect(out.timeout).toMatch(/600s/);
  });

  it('falls back gracefully for non-shell tools', () => {
    const out = explainConfirmRequest({
      tool: 'web_search',
      args: { query: 'ollama', limit: 5 },
    });
    expect(out.summary).toMatch(/calls the web_search tool using query, limit/);
    expect(out.steps).toEqual([]);
  });

  it('handles empty/garbage input', () => {
    expect(explainConfirmRequest(null)).toEqual({ summary: '', steps: [] });
  });
});
