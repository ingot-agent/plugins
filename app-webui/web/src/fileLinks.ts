export interface FileLink {
  path: string
  line?: number
  endLine?: number
  column?: number
  fragment?: string
}

function hasControlCharacters(value: string) {
  return Array.from(value).some(character => character.charCodeAt(0) < 32 || character.charCodeAt(0) === 127)
}

export function fileFragment(fragment: string): Omit<FileLink, 'path'> {
  const match = /^L(\d+)(?:C(\d+))?(?:-L?(\d+))?$/i.exec(fragment)
  if (!match) return fragment ? { fragment } : {}
  const line = Number(match[1])
  const column = match[2] ? Number(match[2]) : undefined
  const endLine = match[3] ? Number(match[3]) : undefined
  if (![line, column, endLine].every(value => value === undefined || (Number.isSafeInteger(value) && value > 0))) return {}
  return { line, column, endLine: endLine && endLine >= line ? endLine : undefined }
}

// Parse the raw Markdown destination, before the browser resolves relative
// hrefs against the WebUI URL. Decode paths once, after separating fragments.
export function parseFileLink(href: string): FileLink | undefined {
  const value = href.trim()
  if (!value || hasControlCharacters(value) || value.startsWith('#') || value.startsWith('//') || value.startsWith('\\\\')) return
  const windows = /^[a-z]:(?:[\\/]|%5c|%2f)/i.test(value)
  const file = /^file:/i.test(value)
  const filePosition = /^[^:/]+\.[^:/]+:\d+(?::\d+)?$/.test(value)
  if (!file && !windows && !filePosition && /^[a-z][a-z\d+.-]*:/i.test(value)) return
  let path: string
  let fragment: string
  try {
    if (file) {
      const url = new URL(value)
      if (url.protocol !== 'file:' || (url.hostname && url.hostname !== 'localhost') || url.search) return
      path = decodeURIComponent(url.pathname)
      if (/^\/[a-z]:\//i.test(path)) path = path.slice(1)
      fragment = decodeURIComponent(url.hash.slice(1))
    } else {
      const hash = value.indexOf('#')
      const destination = hash < 0 ? value : value.slice(0, hash)
      if (destination.includes('?')) return
      path = decodeURIComponent(destination)
      fragment = hash < 0 ? '' : decodeURIComponent(value.slice(hash + 1))
    }
  } catch { return }
  if (!path || hasControlCharacters(path) || path.startsWith('//') || path.startsWith('\\\\')) return
  const position = /:(\d+)(?::(\d+))?$/.exec(path)
  if (!fragment && position) {
    const line = Number(position[1])
    const column = position[2] ? Number(position[2]) : undefined
    if (Number.isSafeInteger(line) && line > 0 && (column === undefined || (Number.isSafeInteger(column) && column > 0))) {
      return { path: path.slice(0, position.index), line, column }
    }
  }
  return { path, ...fileFragment(fragment) }
}
