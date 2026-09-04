// Line-level diff for rulebook versions (Story E23). Classic LCS over
// lines — rulebooks are small (a few KB of Markdown), so O(n·m) is fine.

// diffLines returns [{type: 'same'|'add'|'del', text}] turning `from`
// into `to`.
export function diffLines(from, to) {
  const a = (from ?? '').split('\n');
  const b = (to ?? '').split('\n');
  const n = a.length, m = b.length;

  // LCS table
  const lcs = Array.from({ length: n + 1 }, () => new Array(m + 1).fill(0));
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      lcs[i][j] = a[i] === b[j]
        ? lcs[i + 1][j + 1] + 1
        : Math.max(lcs[i + 1][j], lcs[i][j + 1]);
    }
  }

  const out = [];
  let i = 0, j = 0;
  while (i < n && j < m) {
    if (a[i] === b[j]) {
      out.push({ type: 'same', text: a[i] });
      i++; j++;
    } else if (lcs[i + 1][j] >= lcs[i][j + 1]) {
      out.push({ type: 'del', text: a[i] });
      i++;
    } else {
      out.push({ type: 'add', text: b[j] });
      j++;
    }
  }
  while (i < n) out.push({ type: 'del', text: a[i++] });
  while (j < m) out.push({ type: 'add', text: b[j++] });
  return out;
}

// diffStats summarises a diffLines result for badges: {added, removed}.
export function diffStats(diff) {
  let added = 0, removed = 0;
  for (const d of diff) {
    if (d.type === 'add') added++;
    else if (d.type === 'del') removed++;
  }
  return { added, removed };
}

// sourceBadge maps a version's provenance to a GUI badge.
export function sourceBadge(source) {
  switch (source) {
    case 'auto_update': return { label: 'auto', cls: 'auto' };
    case 'rollback':    return { label: 'rollback', cls: 'roll' };
    default:            return { label: 'manual', cls: 'manual' };
  }
}
