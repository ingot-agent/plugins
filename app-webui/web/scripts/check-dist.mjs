import { execFileSync } from 'node:child_process'
import { URL } from 'node:url'

const path = 'app-webui/app/webdist'
const root = new URL('../../../', import.meta.url)
const git = (...args) => execFileSync('git', args, { cwd: root, encoding: 'utf8' })
const changes = [
  git('diff', '--name-only', '--', path),
  git('diff', '--cached', '--name-only', '--', path),
  git('ls-files', '--others', '--exclude-standard', '--', path),
].filter(Boolean).join('')
if (changes.trim()) {
  throw new Error('Embedded Web UI is out of date. Run npm run build and include webdist changes.\n' + changes)
}
