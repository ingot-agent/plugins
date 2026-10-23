import type { Turn } from '../protocol'

export function turnSnapshot(overrides: Partial<Turn> = {}): Turn {
  return {
    id: 'web', sessionId: 's', sdkTurnId: 'sdk', revision: 1, sequence: 1,
    status: 'running', started: true, persisted: true, input: 'hello',
    reasoning: '', output: '', blocks: [], ...overrides,
  }
}
