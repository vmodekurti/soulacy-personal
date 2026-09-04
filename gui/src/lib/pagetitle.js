// pageTitle keeps the browser tab title in sync with the active page
// (Story 15 polish). Plugin pages resolve through their nav entries so
// third-party panels read natively (e.g. "Soulacy — Weather").
export function pageTitle(pageId, navItems = [], pluginPages = []) {
  const item = navItems.find((p) => p.id === pageId) || pluginPages.find((p) => p.id === pageId);
  return item?.label ? `Soulacy — ${item.label}` : 'Soulacy';
}
