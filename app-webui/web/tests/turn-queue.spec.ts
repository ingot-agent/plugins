import { expect, test, type Page } from '@playwright/test'

async function ready(page: Page) {
  await page.goto('/#/new')
  await expect(page.getByText('Connected', { exact: true })).toBeVisible()
}
async function send(page: Page, input: string) {
  const composer = page.getByRole('textbox', { name: 'Message your agent…', exact: true })
  await composer.fill(input)
  await page.getByRole('button', { name: 'Send message', exact: true }).click()
  await expect(composer).toHaveValue('')
  await expect(page).toHaveURL(/#\/sessions\//)
}
async function approve(page: Page) {
  await page.getByRole('radio', { name: 'Yes', exact: true }).check()
  await page.getByRole('button', { name: 'Submit response', exact: true }).click()
}

test('coalesces a 500-chunk response across an input boundary and restores it without duplicated text', async ({ page }) => {
  await page.addInitScript(() => {
    const original = window.fetch.bind(window)
    const observed: { type: string; data: { outputDelta?: string; turns?: unknown[] } }[] = []
    Object.assign(window, { runtimeUpdates: observed, progressConnections: 0 })
    window.fetch = async (input, init) => {
      const response = await original(input, init)
      if (!String(input).startsWith('/api/events?') || !response.body) return response
      ;(window as unknown as { progressConnections: number }).progressConnections++
      let buffer = ''
      const decoder = new TextDecoder()
      return new Response(response.body.pipeThrough(new TransformStream<Uint8Array, Uint8Array>({
        transform(chunk, controller) {
          buffer += decoder.decode(chunk, { stream: true })
          let end: number
          while ((end = buffer.indexOf('\n\n')) !== -1) {
            const frame = buffer.slice(0, end)
            buffer = buffer.slice(end + 2)
            const data = frame.split('\n').find(line => line.startsWith('data:'))
            if (data) {
              const event = JSON.parse(data.slice(5))
              if (event.type.startsWith('agent.runtime.')) observed.push(event)
            }
          }
          controller.enqueue(chunk)
        },
      })), { status: response.status, headers: response.headers })
    }
  })
  await ready(page)
  await send(page, 'approve before streaming burst')
  await expect(page.getByText('Allow workspace inspection?', { exact: true })).toBeVisible()
  await send(page, 'burst after boundary')
  await approve(page)
  const output = page.locator('.transcript .turn-content > .markdown').filter({ hasText: '字' })
  await expect(output).toHaveText('字'.repeat(500))
  await expect(page.locator('.transcript .message-user .markdown')).toHaveText(['approve before streaming burst', 'burst after boundary'])
  const stats = await page.evaluate(() => {
    const events = (window as unknown as { runtimeUpdates: { type: string; data: { outputDelta?: string; turns?: unknown[] } }[] }).runtimeUpdates
    const progress = events.filter(event => event.type === 'agent.runtime.progress' && event.data.outputDelta)
    return { progress: progress.length, text: progress.map(event => event.data.outputDelta).join(''), snapshots: events.filter(event => event.type === 'agent.runtime.updated').length, noSnapshotsInProgress: progress.every(event => !event.data.turns), connections: (window as unknown as { progressConnections: number }).progressConnections }
  })
  expect(stats.progress).toBeGreaterThan(0)
  expect(stats.progress).toBeLessThan(20)
  expect(stats.snapshots).toBeLessThan(40)
  expect(stats.text).toBe('字'.repeat(500))
  expect(stats.noSnapshotsInProgress).toBe(true)
  expect(stats.connections).toBe(1)
  await page.reload()
  await expect(output).toHaveText('字'.repeat(500))
  await page.getByRole('button', { name: 'Stop execution', exact: true }).click()
  await expect(page.locator('.transcript .execution-link')).toHaveCount(1)
  const stopped = await (await page.request.get('/api/state')).json()
  expect(stopped.turns.find((turn: { input: string }) => turn.input === 'burst after boundary').output).toBe('字'.repeat(500))
  const delivered = await page.evaluate(() => (window as unknown as { runtimeUpdates: { data: { turns?: { input: string; output: string; revision: number }[] } }[] }).runtimeUpdates.flatMap(event => event.data.turns || []).filter(turn => turn.input === 'burst after boundary').at(-1))
  expect(delivered?.output).toBe('字'.repeat(500))
  await expect(output).toHaveText('字'.repeat(500))
})

for (const failure of ['stalled stream', 'missing runtime updates']) {
  test(`recovers ${failure} without refreshing, losing drafts, or resending inputs`, async ({ page }) => {
    // Keep the real Go execution and HTTP responses; suppress only delivery of
    // selected SSE frames after the approval has made the running state visible.
    await page.addInitScript(mode => {
      const original = window.fetch.bind(window)
      window.fetch = async (input, init) => {
        const response = await original(input, init)
        if (!String(input).startsWith('/api/events?') || !response.body) return response
        let buffer = ''
        const decoder = new TextDecoder()
        const encoder = new TextEncoder()
        return new Response(response.body.pipeThrough(new TransformStream<Uint8Array, Uint8Array>({
          transform(chunk, controller) {
            buffer += decoder.decode(chunk, { stream: true })
            let end: number
            while ((end = buffer.indexOf('\n\n')) !== -1) {
              const frame = buffer.slice(0, end + 2)
              buffer = buffer.slice(end + 2)
              if (document.documentElement.dataset.suppressEvents &&
                (mode === 'stalled stream' || frame.includes('"type":"agent.runtime.updated"'))) continue
              controller.enqueue(encoder.encode(frame))
            }
          },
        })), { status: response.status, headers: response.headers })
      }
    }, failure)
    let submissions = 0
    page.on('request', request => { if (request.method() === 'POST' && request.url().endsWith('/api/turns')) submissions++ })
    await ready(page)
    await send(page, 'approve recovery head')
    await expect(page.getByText('Allow workspace inspection?', { exact: true })).toBeVisible()
    await send(page, 'recovered next round')
    const sessionId = page.url().split('/sessions/')[1]
    await expect(page.getByRole('region', { name: 'Message queue', exact: true }).locator('li')).toHaveCount(1)
    await page.evaluate(() => { document.documentElement.dataset.suppressEvents = 'true' })
    await approve(page)
    const composer = page.getByRole('textbox', { name: 'Message your agent…', exact: true })
    await composer.fill('Keep this unsent draft')
    await expect.poll(async () => {
      const state = await (await page.request.get('/api/state')).json()
      return state.turns.filter((turn: { sessionId: string }) => turn.sessionId === sessionId).map((turn: { status: string }) => turn.status)
    }).toEqual(['succeeded', 'succeeded'])
    await expect(page.locator('.transcript .execution-link')).toHaveCount(1, { timeout: 15000 })
    await expect(page.getByRole('button', { name: 'Stop execution', exact: true })).toHaveCount(0)
    await expect(page.getByRole('region', { name: 'Message queue', exact: true })).toHaveCount(0)
    await expect(page.locator('.transcript .message-user .markdown')).toHaveText(['approve recovery head', 'recovered next round'])
    await expect(page.getByText('Your workspace is ready. We can take the next step together.', { exact: true })).toHaveCount(1)
    await expect(composer).toHaveValue('Keep this unsent draft')
    expect(submissions).toBe(2)
  })
}

test('queues during approvals, removes pending input, and restores the shared queue', async ({ page, context }, testInfo) => {
  await ready(page)
  await send(page, 'approve queue head')
  await expect(page.getByText('Allow workspace inspection?', { exact: true })).toBeVisible()
  await send(page, 'removed queued input')
  await send(page, 'queued follow-on')
  const queue = page.getByRole('region', { name: 'Message queue', exact: true })
  await expect(queue.locator('li')).toHaveCount(2)
  await expect(page.locator('.transcript .message-user')).toHaveCount(1)
  await queue.getByRole('button', { name: 'Remove queued message 1', exact: true }).click()
  await expect(queue.locator('li')).toHaveCount(1)
  await expect(queue).toContainText('queued follow-on')
  await send(page, 'second boundary input')
  await expect(queue.locator('li')).toHaveCount(2)
  await page.screenshot({ path: testInfo.outputPath('queue-desktop.png') })

  const other = await context.newPage()
  await other.goto(page.url())
  await expect(other.getByText('Connected', { exact: true })).toBeVisible()
  await expect(other.getByRole('region', { name: 'Message queue', exact: true })).toContainText('queued follow-on')
  await page.reload()
  await expect(page.getByText('Connected', { exact: true })).toBeVisible()
  await expect(queue).toContainText('queued follow-on')
  await approve(page)
  await expect(page.getByRole('button', { name: 'Stop execution', exact: true })).toHaveCount(0)
  await expect(queue).toHaveCount(0)
  await expect(page.locator('.transcript .message-user .markdown')).toHaveText(['approve queue head', 'queued follow-on', 'second boundary input'])
  await expect(other.locator('.transcript .message-user .markdown')).toHaveText(['approve queue head', 'queued follow-on', 'second boundary input'])
  await expect(page.locator('.transcript .execution-link')).toHaveCount(1)
  await expect(page.locator('.transcript .live-message')).toHaveCount(1)
  await expect(other.locator('.transcript .execution-link')).toHaveCount(1)
  const snapshot = await (await page.request.get('/api/state')).json()
  const admitted = snapshot.turns.filter((turn: { sessionId: string; persisted: boolean }) => turn.sessionId === page.url().split('/sessions/')[1] && turn.persisted)
  expect(admitted).toHaveLength(3)
  expect(admitted[0].sdkTurnId).toBeTruthy()
  expect(new Set(admitted.map((turn: { sdkTurnId: string }) => turn.sdkTurnId)).size).toBe(1)
  await page.reload()
  await expect(page.locator('.transcript .message-user .markdown')).toHaveText(['approve queue head', 'queued follow-on', 'second boundary input'])
  await expect(page.locator('.transcript .execution-link')).toHaveCount(1)
  const response = await page.request.get('/api/sessions/' + page.url().split('/sessions/')[1] + '/history')
  expect(JSON.stringify(await response.json())).not.toContain('removed queued input')
  await send(page, 'a separate lifecycle')
  await expect(page.getByRole('button', { name: 'Stop execution', exact: true })).toHaveCount(0)
  await expect(page.locator('.transcript .execution-link')).toHaveCount(2)
  await page.reload()
  await expect(page.locator('.transcript .execution-link')).toHaveCount(2)
  await other.close()
})

test('shows one running indicator and one canceled summary for multiple inputs in one Turn', async ({ page }) => {
  await ready(page)
  await send(page, 'approve before boundary')
  await expect(page.getByText('Allow workspace inspection?', { exact: true })).toBeVisible()
  await send(page, 'middle boundary input')
  await send(page, 'hold after boundary')
  await approve(page)
  await expect(page.getByText('Partial response', { exact: true })).toBeVisible()
  await expect(page.locator('.transcript .message-user .markdown')).toHaveText(['approve before boundary', 'middle boundary input', 'hold after boundary'])
  await expect(page.locator('.transcript .thinking-dots')).toHaveCount(1)
  await expect(page.locator('.transcript .message-byline')).toHaveCount(1)
  await page.reload()
  await expect(page.getByText('Partial response', { exact: true })).toBeVisible()
  await expect(page.locator('.transcript .thinking-dots')).toHaveCount(1)
  await expect(page.locator('.transcript .message-byline')).toHaveCount(1)
  await page.getByRole('button', { name: 'Stop execution', exact: true }).click()
  await expect(page.locator('.transcript .execution-link')).toHaveCount(1)
  await expect(page.locator('.transcript .execution-link')).toContainText('Canceled')
  await expect(page.locator('.transcript .thinking-dots')).toHaveCount(0)
})

test('stopping pauses pending input until resumed, including across navigation and refresh', async ({ page }, testInfo) => {
  await ready(page)
  await send(page, 'hold queue head')
  await expect(page.getByText('Partial response', { exact: true })).toBeVisible()
  await send(page, 'queued next step with a long description that should wrap without moving the queue controls on a narrow screen')
  const sessionUrl = page.url()
  await page.getByRole('button', { name: 'Stop execution', exact: true }).click()
  const queue = page.getByRole('region', { name: 'Message queue', exact: true })
  await expect(queue.getByText('Paused', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Stop execution', exact: true })).toHaveCount(0)
  await page.goto('/#/new')
  await page.goto(sessionUrl)
  await page.reload()
  await expect(page.getByText('Connected', { exact: true })).toBeVisible()
  await expect(queue.getByText('Paused', { exact: true })).toBeVisible()
  await page.setViewportSize({ width: 360, height: 780 })
  await page.screenshot({ path: testInfo.outputPath('queue-mobile.png') })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await queue.getByRole('button', { name: 'Resume queue', exact: true }).click()
  await expect(queue).toHaveCount(0)
  await expect(page.getByText('Your workspace is ready. We can take the next step together.', { exact: true })).toHaveCount(1)
  await expect(page.locator('.transcript .message-user .markdown')).toHaveCount(2)
})

test('a failed attachment pauses later input while the current Turn finishes, and resume does not retry it', async ({ page }) => {
  await ready(page)
  await send(page, 'approve before queued error')
  await expect(page.getByText('Allow workspace inspection?', { exact: true })).toBeVisible()
  const sessionId = page.url().split('/sessions/')[1]
  const submitted = await page.request.post('/api/turns', { data: {
    sessionId, input: 'failed attachment input', requestId: 'missing-' + sessionId,
    attachments: [{ kind: 'file', name: 'missing.txt', path: '/ingot-missing-fixture/missing.txt' }],
  } })
  expect(submitted.status()).toBe(202)
  await send(page, 'later queued step')
  await approve(page)
  const queue = page.getByRole('region', { name: 'Message queue', exact: true })
  await expect(queue.getByText('Paused', { exact: true })).toBeVisible()
  await expect(queue.locator('li')).toHaveCount(1)
  await expect(queue).toContainText('later queued step')
  await expect(page.getByText(/inspect attachment.*missing.txt/)).toBeVisible()
  await expect(page.getByRole('button', { name: 'Stop execution', exact: true })).toHaveCount(0)
  const before = await (await page.request.get('/api/state')).json()
  const messages = before.turns.filter((turn: { sessionId: string }) => turn.sessionId === sessionId)
  expect(messages.map((turn: { status: string }) => turn.status)).toEqual(['succeeded', 'failed', 'queued'])
  await expect(page.locator('.transcript .execution-link')).toHaveCount(2)
  await page.reload()
  await expect(page.getByText(/inspect attachment.*missing.txt/)).toBeVisible()
  await queue.getByRole('button', { name: 'Resume queue', exact: true }).click()
  await expect(queue).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Stop execution', exact: true })).toHaveCount(0)
  await expect(page.locator('.transcript .message-user .markdown')).toHaveText(['approve before queued error', 'failed attachment input', 'later queued step'])
  const history = await (await page.request.get('/api/sessions/' + sessionId + '/history')).json()
  expect(JSON.stringify(history)).not.toContain('failed attachment input')
})

test('follow-up notes queue independently and preserve a draft typed before acknowledgment', async ({ page }, testInfo) => {
  await ready(page)
  await send(page, 'source for queued follow-up')
  const answer = page.locator('.message-assistant[data-message-index] .markdown').last()
  await expect(answer).toContainText('workspace is ready')
  await answer.evaluate(element => {
    const node = element.querySelector('p')!.firstChild!
    const start = node.textContent!.indexOf('workspace is ready')
    const range = document.createRange()
    range.setStart(node, start)
    range.setEnd(node, start + 'workspace is ready'.length)
    window.getSelection()!.removeAllRanges()
    window.getSelection()!.addRange(range)
    element.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }))
  })
  await page.getByRole('button', { name: 'Follow up', exact: true }).click()
  const note = page.getByRole('dialog', { name: 'Follow up', exact: true })
  const composer = note.getByRole('textbox', { name: 'Ask about this passage…', exact: true })
  await composer.fill('hold follow-up')
  await note.getByRole('button', { name: 'Send message', exact: true }).click()
  await expect(composer).toHaveValue('')
  await expect(note.getByText('Partial response', { exact: true })).toBeVisible()

  let acknowledge!: () => void
  const acknowledgment = new Promise<void>(resolve => { acknowledge = resolve })
  await page.route('**/api/turns', async route => {
    const response = await route.fetch()
    await acknowledgment
    await route.fulfill({ response })
  })
  await composer.fill('queued follow-up')
  await note.getByRole('button', { name: 'Send message', exact: true }).click()
  const queue = note.getByRole('region', { name: 'Message queue', exact: true })
  await expect(queue).toContainText('queued follow-up')
  await composer.fill('keep this note draft')
  acknowledge()
  await expect(composer).toHaveValue('keep this note draft')
  await expect(note.getByRole('button', { name: 'Send message', exact: true })).toBeEnabled()
  await note.getByRole('button', { name: 'Stop execution', exact: true }).click()
  await expect(queue.getByText('Paused', { exact: true })).toBeVisible()
  await expect(note.getByRole('button', { name: 'Stop execution', exact: true })).toHaveCount(0)
  await page.setViewportSize({ width: 390, height: 740 })
  await page.screenshot({ path: testInfo.outputPath('queue-followup-mobile.png') })
  await queue.getByRole('button', { name: 'Resume queue', exact: true }).click()
  await expect(queue).toHaveCount(0)
  await expect(note.getByText('Your workspace is ready. We can take the next step together.', { exact: true })).toBeVisible()
  await expect(note.locator('.followup-user .markdown')).toHaveText(['hold follow-up', 'queued follow-up'])
  await expect(composer).toHaveValue('keep this note draft')
  await expect(page.locator('.transcript .message-user')).toHaveCount(1)
})
