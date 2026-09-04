import { describe, it, expect } from 'vitest';
import { pageTitle } from './pagetitle.js';

const nav = [
  { id: 'dashboard', label: 'Dashboard' },
  { id: 'pluginmgr', label: 'Plugins' },
];
const pluginPages = [{ id: 'plugin:weather', label: 'Weather' }];

describe('pageTitle', () => {
  it('uses the nav label', () => {
    expect(pageTitle('dashboard', nav)).toBe('Soulacy — Dashboard');
  });
  it('resolves plugin pages', () => {
    expect(pageTitle('plugin:weather', nav, pluginPages)).toBe('Soulacy — Weather');
  });
  it('falls back to the product name', () => {
    expect(pageTitle('nope', nav, pluginPages)).toBe('Soulacy');
    expect(pageTitle('', [])).toBe('Soulacy');
  });
});
