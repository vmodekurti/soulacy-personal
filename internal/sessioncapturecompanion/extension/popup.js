document.querySelector('#connect').addEventListener('click', async () => {
  const status = document.querySelector('#status')
  try {
    const [tab] = await chrome.tabs.query({ active: true, currentWindow: true })
    const page = new URL(tab?.url || '')
    const trusted = page.hostname === 'localhost' || page.hostname === '127.0.0.1' || page.hostname === 'soulacy.io' || page.hostname.endsWith('.soulacy.io')
    if (!trusted) throw new Error('This companion accepts soulacy.io and local Soulacy tabs. A custom deployment must be allowlisted by its administrator.')
    await chrome.scripting.executeScript({ target: { tabId: tab.id }, files: ['content-script.js'] })
    status.textContent = 'Connected. Return to Soulacy and continue.'
  } catch (error) { status.textContent = error?.message || String(error) }
})
