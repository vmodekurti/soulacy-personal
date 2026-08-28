// Convert Studio's live inventory responses into the typed catalog accepted by
// the Go compiler. Exact MCP callable names must survive this boundary: their
// short display names cannot be resolved by the runtime.
export function catalogSkills(cat) {
  const value = cat && cat.skills
  if (Array.isArray(value)) return value
  if (value && Array.isArray(value.skills)) return value.skills
  return []
}

export function catalogSkillNames(cat) {
  return catalogSkills(cat)
    .map((skill) => typeof skill === 'string' ? skill : (skill && (skill.name || skill.id)))
    .filter(Boolean)
}

export function compactCatalog(cat) {
  if (!cat) return undefined

  const tools = []
  const seenTools = new Set()
  const pushTool = (value) => {
    const name = String(value == null ? '' : value).trim()
    if (!name || seenTools.has(name)) return
    seenTools.add(name)
    tools.push(name)
  }

  const rawTools = cat.tools || {}
  for (const entry of rawTools.python_tools || []) pushTool(entry && entry.name)
  for (const entry of rawTools.builtins || []) pushTool(entry && entry.name)

  const mcpByServer = new Map()
  for (const entry of rawTools.mcp_tools || []) {
    if (!entry) continue
    const name = String(entry.full_name || entry.name || '').trim()
    const server = String(entry.server || 'mcp').trim() || 'mcp'
    if (!name) continue
    pushTool(name)
    if (!mcpByServer.has(server)) mcpByServer.set(server, [])
    mcpByServer.get(server).push({
      name,
      description: entry.description || '',
      params: entry.params || '',
      risk: entry.risk || '',
    })
  }

  const agents = []
  const seenAgents = new Set()
  const pushAgent = (value) => {
    const name = String(value == null ? '' : value).trim()
    if (!name || seenAgents.has(name)) return
    seenAgents.add(name)
    agents.push(name)
  }
  for (const agent of (cat.agents && cat.agents.agents) || []) {
    pushAgent(agent && agent.id)
    pushAgent(agent && agent.name)
  }

  const skills = catalogSkills(cat)
    .filter(Boolean)
    .map((skill) => typeof skill === 'string'
      ? { name: skill }
      : { name: skill.name || skill.id || '', description: skill.description || '' })
    .filter((skill) => skill.name)

  const channels = ((cat.channels && cat.channels.channels) || [])
    .filter((channel) => channel && (channel.enabled || channel.always_on))
    .map((channel) => channel.id || channel.name)
    .filter(Boolean)

  return {
    tools,
    agents,
    providers: Object.keys((cat.providers && cat.providers.providers) || {}),
    skills,
    mcp: Array.from(mcpByServer, ([server, serverTools]) => ({ server, tools: serverTools })),
    channels,
  }
}
