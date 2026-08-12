// Minimal browser shims so stores.js (localStorage) and api.js (fetch,
// location) can be imported under Node.
//
// Only fill in what is genuinely missing. Under the jsdom environment both of
// these already exist and are richer than the stand-ins — the `location` shim
// in particular has no `hash`, so overwriting jsdom's would make any code that
// reads `location.hash` throw inside a test that has nothing to do with it.
if (typeof globalThis.localStorage === 'undefined') {
  const backing = new Map()
  globalThis.localStorage = {
    getItem: (k) => (backing.has(k) ? backing.get(k) : null),
    setItem: (k, v) => backing.set(k, String(v)),
    removeItem: (k) => backing.delete(k),
    clear: () => backing.clear(),
  }
}
if (typeof globalThis.location === 'undefined') {
  globalThis.location = { protocol: 'http:', host: 'localhost:8080' }
}

// jsdom intentionally does not implement layout observers. Svelte 5 uses one
// for element-size bindings, so component tests need a no-op observer just as
// they would need a layout engine in a real browser.
if (typeof globalThis.ResizeObserver === 'undefined') {
  globalThis.ResizeObserver = class ResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
}
