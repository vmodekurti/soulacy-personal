import { describe, it, expect } from 'vitest';
import { displayValue, rowsFromSettings, parseValue, settingsPatchFromRows } from './pluginsettings.js';

describe('rowsFromSettings / displayValue', () => {
  it('renders scalars plainly and objects as JSON', () => {
    const rows = rowsFromSettings({ units: 'metric', retries: 3, nested: { a: 1 } });
    expect(rows).toContainEqual({ key: 'units', value: 'metric' });
    expect(rows).toContainEqual({ key: 'retries', value: '3' });
    expect(rows).toContainEqual({ key: 'nested', value: '{"a":1}' });
  });
});

describe('parseValue', () => {
  it('parses JSON-typed values and keeps plain strings', () => {
    expect(parseValue('3')).toBe(3);
    expect(parseValue('true')).toBe(true);
    expect(parseValue('{"a":1}')).toEqual({ a: 1 });
    expect(parseValue('metric')).toBe('metric');
    expect(parseValue('')).toBe('');
  });
});

describe('settingsPatchFromRows (Story 18)', () => {
  const original = { units: 'metric', api_key: '***', version: '1.10', retries: 3 };

  it('unchanged rows reuse the original value — no type coercion', () => {
    const rows = rowsFromSettings(original);
    const patch = settingsPatchFromRows(original, rows);
    // '1.10' must stay the STRING '1.10', not become the number 1.1
    expect(patch.version).toBe('1.10');
    expect(patch.retries).toBe(3);
    // the redacted placeholder round-trips verbatim (server skips it)
    expect(patch.api_key).toBe('***');
  });

  it('edited rows parse type-aware', () => {
    const rows = rowsFromSettings(original).map((r) =>
      r.key === 'retries' ? { ...r, value: '5' } : r);
    expect(settingsPatchFromRows(original, rows).retries).toBe(5);
  });

  it('removed rows become null (delete) and new rows are added', () => {
    const rows = [
      { key: 'units', value: 'imperial' },
      { key: 'brand_new', value: 'hello' },
    ];
    const patch = settingsPatchFromRows(original, rows);
    expect(patch.units).toBe('imperial');
    expect(patch.brand_new).toBe('hello');
    expect(patch.api_key).toBeNull();
    expect(patch.version).toBeNull();
    expect(patch.retries).toBeNull();
  });

  it('blank keys are ignored', () => {
    const patch = settingsPatchFromRows({}, [{ key: ' ', value: 'x' }]);
    expect(Object.keys(patch)).toHaveLength(0);
  });
});
