import { describe, it, expect } from 'vitest';
import { diffLines, diffStats, sourceBadge } from './rulediff.js';

describe('diffLines (E23)', () => {
  it('marks unchanged, added, and removed lines', () => {
    const d = diffLines('a\nb\nc', 'a\nx\nc');
    expect(d).toEqual([
      { type: 'same', text: 'a' },
      { type: 'del', text: 'b' },
      { type: 'add', text: 'x' },
      { type: 'same', text: 'c' },
    ]);
  });
  it('handles pure additions and removals', () => {
    expect(diffLines('', 'a').filter(d => d.type === 'add').length).toBeGreaterThan(0);
    expect(diffLines('a\nb', 'a')).toContainEqual({ type: 'del', text: 'b' });
  });
  it('identical inputs produce only same lines', () => {
    expect(diffLines('a\nb', 'a\nb').every(d => d.type === 'same')).toBe(true);
  });
});

describe('diffStats', () => {
  it('counts adds and removes', () => {
    const s = diffStats(diffLines('a\nb\nc', 'a\nx\ny\nc'));
    expect(s).toEqual({ added: 2, removed: 1 });
  });
});

describe('sourceBadge', () => {
  it('maps provenance to badges', () => {
    expect(sourceBadge('auto_update').cls).toBe('auto');
    expect(sourceBadge('rollback').cls).toBe('roll');
    expect(sourceBadge('manual').cls).toBe('manual');
    expect(sourceBadge('anything-else').cls).toBe('manual');
  });
});
