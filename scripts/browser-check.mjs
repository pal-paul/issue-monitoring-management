import assert from 'node:assert/strict';

export default async function run(page, ui) {
  const securityErrors = [];
  page.on('console', message => { if (message.text().includes('Content Security Policy')) securityErrors.push(message.text()); });
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await page.setViewportSize({ width: 1440, height: 1000 });
  assert.ok(['127.0.0.1', 'localhost'].includes(new URL(page.url()).hostname), 'Only run against a local test instance');
  const session = await page.evaluate(async () => (await fetch('/api/session')).json());
  assert.equal(session.local, true, 'Local mode required');
  await page.waitForSelector('#access-screen:not([hidden])');
  if (session.setupRequired) {
    assert.ok(process.env.TEST_DATABASE_URL, 'Set TEST_DATABASE_URL to an empty local test database');
    await page.locator('#setup-database').fill(process.env.TEST_DATABASE_URL);
    await page.locator('#setup-name').fill('Browser Administrator');
    await page.locator('#setup-confirm').fill('browser-testing-only-password');
  }
  await page.locator('#access-username').fill('browser-admin');
  await page.locator('#access-password').fill('browser-testing-only-password');
  await page.locator('#access-submit').click();
  await page.waitForSelector('#issue-rows tr');
  const initialSnapshot = await ui.snapshot();
  assert.ok(initialSnapshot.includes('Search issues'));
  const baseline = await page.evaluate(async () => (await fetch('/api/issues?limit=1')).json());
  assert.ok(baseline.total > 25, 'Browser checks need the sample dataset');
  await page.getByRole('button', { name: 'Next page', exact: true }).click();
  await page.waitForFunction(pages => document.getElementById('page-number').textContent === `Page 2 of ${pages}`, Math.ceil(baseline.total / 25));
  assert.equal(await page.locator('#issue-rows tr').count(), Math.min(25, baseline.total - 25));
  await page.getByRole('button', { name: 'Previous page', exact: true }).click();
  await page.waitForFunction(pages => document.getElementById('page-number').textContent === `Page 1 of ${pages}`, Math.ceil(baseline.total / 25));

  await page.getByRole('searchbox', { name: 'Search issues' }).fill('High CPU');
  await page.waitForFunction(() => {
    const titles = [...document.querySelectorAll('.issue-title')];
    return titles.length > 0 && titles.every(item => item.textContent.includes('CPU'));
  });
  await page.getByRole('combobox', { name: 'Filter by severity' }).selectOption('info');
  await page.waitForSelector('#empty-state:not([hidden])');
  assert.equal(await page.locator('#issue-rows tr').count(), 0);
  await page.getByRole('button', { name: 'Reset filters', exact: true }).click();
  await page.waitForFunction(() => document.querySelectorAll('#issue-rows tr').length === 25);

  await page.getByRole('tab', { name: /^Open / }).click();
  await page.waitForFunction(() => {
    const badges = [...document.querySelectorAll('#issue-rows .badge.status')];
    return badges.length > 0 && badges.every(item => item.classList.contains('open'));
  });
  const firstID = await page.locator('#issue-rows tr').first().getAttribute('data-id');
  await page.locator('#issue-rows .issue-link').first().click();
  await page.getByRole('button', { name: 'Acknowledge issue', exact: true }).waitFor();
  assert.equal((await page.locator('.stable-issue-id').innerText()).trim(), firstID);
  await page.getByRole('button', { name: 'Acknowledge issue', exact: true }).click();
  await page.getByRole('button', { name: 'Remove acknowledgement', exact: true }).waitFor();
  const acknowledged = await page.evaluate(async id => (await (await fetch(`/api/issues/${id}`)).json()).issue.status, firstID);
  assert.equal(acknowledged, 'acknowledged');
  await page.locator('#detail-content > .raw-payload summary').click();
  assert.ok((await page.locator('#detail-content > .raw-payload pre').textContent()).includes('incident_id'));
  await page.screenshot({ path: '/tmp/signaldesk-detail.png', fullPage: false });
  await page.getByRole('button', { name: 'Remove acknowledgement', exact: true }).click();
  await page.getByRole('button', { name: 'Acknowledge issue', exact: true }).waitFor();
  assert.equal(await page.locator('#save-work-log').isDisabled(), true);
  const workLog = `Investigated CPU pressure ${Date.now()}.\n<script>window.workLogExecuted = true</script>`;
  await page.getByRole('textbox', { name: 'Work Log', exact: true }).fill(workLog);
  const workLogSource = await page.locator('#work-log-body').inputValue();
  await page.getByRole('combobox', { name: 'New status', exact: true }).selectOption('acknowledged');
  await Promise.all([
    page.waitForResponse(response => response.url().endsWith('/api/overview')),
    page.evaluate(() => document.getElementById('refresh').click())
  ]);
  assert.equal(await page.locator('#work-log-body').inputValue(), workLogSource, 'Live refresh discarded draft');
  assert.equal(await page.locator('#work-log-status').inputValue(), 'acknowledged', 'Live refresh discarded selected status');
  await page.route('**/api/issues/*/worklogs', route => route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: 'Test work log interruption' }) }));
  await page.getByRole('button', { name: 'Add Work Log', exact: true }).click();
  await page.waitForSelector('#work-log-error:not([hidden])');
  assert.equal(await page.locator('#work-log-body').inputValue(), workLogSource, 'Failed save discarded draft');
  assert.equal(await page.locator('#work-log-status').inputValue(), 'acknowledged', 'Failed save discarded selected status');
  await page.unroute('**/api/issues/*/worklogs');
  await page.getByRole('button', { name: 'Add Work Log', exact: true }).click();
  await page.locator('#issue-timeline .work-log-body').filter({ hasText: workLog }).waitFor();
  assert.equal(await page.locator('#work-log-body').inputValue(), '');
  const loggedDetail = await page.evaluate(async id => (await fetch(`/api/issues/${id}`)).json(), firstID);
  const logged = loggedDetail.events.find(event => event.body === workLogSource);
  assert.equal(logged.actor, 'browser-admin');
  assert.equal(logged.kind, 'work_log');
  assert.equal(logged.bodyFormat, 'markdown');
  assert.equal(loggedDetail.issue.status, 'acknowledged');
  assert.ok(loggedDetail.events.some(event => event.kind === 'status_changed' && event.body === 'Status changed from open to acknowledged' && event.actor === 'browser-admin'));
  assert.ok(logged.receivedAt && logged.eventAt);
  assert.ok(loggedDetail.events.some(event => event.kind === 'acknowledged'));
  assert.ok(loggedDetail.events.some(event => event.kind === 'unacknowledged'));
  assert.equal(await page.locator('#issue-timeline script').count(), 0, 'Work log rendered executable markup');
  await page.locator('#work-log-section').scrollIntoViewIfNeeded();
  await page.screenshot({ path: '/tmp/signaldesk-work-log-desktop.png', fullPage: false });
  await page.reload();
  await page.waitForSelector('#issue-rows tr');
  await page.locator(`#issue-rows tr[data-id="${firstID}"] .issue-link`).click();
  await page.locator('#issue-timeline .work-log-body').filter({ hasText: workLog }).waitFor();
  await page.getByRole('textbox', { name: 'Work Log', exact: true }).fill('Investigation complete; resolving the issue.');
  await page.getByRole('combobox', { name: 'New status', exact: true }).selectOption('resolved');
  await page.getByRole('button', { name: 'Add Work Log', exact: true }).click();
  await page.locator('.detail-badges .badge.status.resolved').waitFor();
  await page.locator('#issue-timeline [data-event-kind="status_changed"]').filter({ hasText: 'Status changed from acknowledged to resolved' }).first().waitFor();
  assert.equal(await page.locator('#acknowledge').count(), 0);
  await page.getByRole('textbox', { name: 'Work Log', exact: true }).fill('Follow-up required; reopening the issue.');
  await page.getByRole('combobox', { name: 'New status', exact: true }).selectOption('open');
  await page.getByRole('button', { name: 'Add Work Log', exact: true }).click();
  await page.locator('.detail-badges .badge.status.open').waitFor();
  await page.locator('#issue-timeline [data-event-kind="status_changed"]').filter({ hasText: 'Status changed from resolved to open' }).first().waitFor();
  assert.equal(await page.locator('#work-log-editor').getAttribute('contenteditable'), 'true');
  const richEditor = page.getByRole('textbox', { name: 'Work Log', exact: true });
  const selectEditor = async () => { await richEditor.focus(); await richEditor.press('ControlOrMeta+a'); };
  const resetEditor = async () => {
    await selectEditor();
    await page.getByRole('button', { name: 'Clear formatting', exact: true }).click();
    await richEditor.fill('Toolbar check');
    await selectEditor();
  };
  for (const [name, selector] of [['Bold', 'strong'], ['Italic', 'em'], ['Strikethrough', 's'], ['Bullet list', 'ul'], ['Numbered list', 'ol'], ['Block quote', 'blockquote'], ['Inline code', 'code'], ['Code block', 'pre']]) {
    await resetEditor();
    await page.getByRole('button', { name, exact: true }).click();
    assert.equal(await richEditor.locator(selector).count(), 1, `${name} did not format the selection`);
  }
  await resetEditor();
  await page.getByRole('combobox', { name: 'Text style', exact: true }).selectOption('2');
  assert.equal(await richEditor.locator('h2').count(), 1, 'Heading selector did not create Heading 2');
  await page.getByRole('button', { name: 'Clear formatting', exact: true }).click();
  assert.equal(await richEditor.locator('h2,strong,em,s,ul,ol,blockquote,code,pre').count(), 0);
  const beforeUndo = await page.locator('#work-log-body').inputValue();
  await page.getByRole('button', { name: 'Undo', exact: true }).click();
  assert.notEqual(await page.locator('#work-log-body').inputValue(), beforeUndo, 'Undo did not change the document');
  await page.getByRole('button', { name: 'Redo', exact: true }).click();
  assert.equal(await page.locator('#work-log-body').inputValue(), beforeUndo, 'Redo did not restore the document');
  await resetEditor();
  await page.getByRole('button', { name: 'Insert or edit link', exact: true }).click();
  await page.getByRole('textbox', { name: 'Link URL', exact: true }).fill('javascript:alert(1)');
  await page.getByRole('button', { name: 'Apply link', exact: true }).click();
  assert.equal(await page.locator('.work-log-link-panel [role="alert"]').isVisible(), true);
  assert.equal(await richEditor.locator('a').count(), 0);
  await page.getByRole('textbox', { name: 'Link URL', exact: true }).fill('https://example.com/runbook');
  await page.getByRole('button', { name: 'Apply link', exact: true }).click();
  assert.equal(await richEditor.locator('a').getAttribute('href'), 'https://example.com/runbook');
  await page.getByRole('button', { name: 'Remove link', exact: true }).click();
  assert.equal(await richEditor.locator('a').count(), 0);
  await resetEditor();
  await richEditor.fill('');
  const richTitle = `Rich investigation ${Date.now()}`;
  await richEditor.evaluate((element, title) => {
    const clipboardData = new DataTransfer();
    clipboardData.setData('text/html', `<h2>${title}</h2><p><strong>CPU</strong> is <em>stable</em>; <s>retry</s> complete.</p><ul><li><p>Checked metrics</p></li></ul><ol><li><p>Restarted worker</p></li></ol><blockquote><p>Recovery confirmed</p></blockquote><p>Inspect <code>worker_count</code> in the <a href="https://example.com/runbook">runbook</a>.</p><pre><code>return nil</code></pre>`);
    element.dispatchEvent(new ClipboardEvent('paste', { bubbles: true, cancelable: true, clipboardData }));
  }, richTitle);
  assert.equal(await richEditor.locator('h2,strong,em,s,ul,ol,blockquote,pre,a').count(), 9);
  const richSource = await page.locator('#work-log-body').inputValue();
  assert.ok(richSource.includes('**CPU**') && richSource.includes('## ') && richSource.includes('```'));
  await page.locator('#work-log-section').scrollIntoViewIfNeeded();
  await page.screenshot({ path: '/tmp/signaldesk-rich-editor-desktop.png', fullPage: false });
  await page.getByRole('button', { name: 'Add Work Log', exact: true }).click();
  await page.locator('#issue-timeline .rich-text h2').filter({ hasText: richTitle }).waitFor();
  const richSecurity = await page.evaluate(async id => {
    const { csrf } = await (await fetch('/api/session')).json();
    const plain = `Legacy **literal** <script>window.unsafeWorkLog = true</script> ${Date.now()}`;
    const malicious = `Safety ${Date.now()}\n\n<script>window.unsafeWorkLog = true</script>\n\n<img src=x onerror="window.unsafeWorkLog=true"><svg onload="window.unsafeWorkLog=true"></svg><iframe srcdoc="bad"></iframe>\n\n[unsafe](javascript:alert(1)) [data](data:text/html,bad) [safe](https://example.com/safe)`;
    for (const note of [{ body: plain }, { body: malicious, format: 'markdown' }]) {
      const response = await fetch(`/api/issues/${id}/worklogs`, { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf }, body: JSON.stringify(note) });
      if (!response.ok) throw new Error(`Security fixture failed: ${response.status}`);
    }
    return { plain, malicious };
  }, firstID);
  await page.reload();
  await page.waitForSelector('#issue-rows tr');
  await page.locator(`#issue-rows tr[data-id="${firstID}"] .issue-link`).click();
  const richBody = page.locator('#issue-timeline .work-log-body').filter({ has: page.locator('h2', { hasText: richTitle }) });
  await richBody.waitFor();
  assert.equal(await richBody.locator('h2,strong,em,del,ul,ol,blockquote,pre,a').count(), 9);
  assert.equal(await richBody.locator('a').getAttribute('rel'), 'noopener noreferrer nofollow');
  assert.equal(await richBody.locator('strong').evaluate(element => getComputedStyle(element).display), 'inline');
  assert.equal(await richBody.locator('ul > li').evaluate(element => getComputedStyle(element, '::before').content), 'none');
  const richPersisted = await page.evaluate(async id => (await (await fetch(`/api/issues/${id}`)).json()).events, firstID);
  assert.ok(richPersisted.some(event => event.body === richSource.trim() && event.bodyFormat === 'markdown'));
  const legacyBody = page.locator('#issue-timeline .work-log-body').filter({ hasText: richSecurity.plain });
  assert.equal(await legacyBody.count(), 1);
  assert.equal(await legacyBody.locator('strong,script').count(), 0);
  assert.equal(await page.locator('#issue-timeline .work-log-body').locator('script,img,svg,iframe').count(), 0);
  assert.equal(await page.locator('#issue-timeline a[href^="javascript:"],#issue-timeline a[href^="data:"]').count(), 0);
  const safetyBody = page.locator('#issue-timeline .work-log-body').filter({ hasText: richSecurity.malicious.split('\n')[0] });
  assert.equal(await safetyBody.locator('a[href="https://example.com/safe"]').count(), 1);
  assert.equal(await page.evaluate(() => Boolean(window.unsafeWorkLog)), false);
  assert.deepEqual(securityErrors, [], 'Rendering untrusted Markdown attempted to execute HTML');
  await richBody.scrollIntoViewIfNeeded();
  await page.screenshot({ path: '/tmp/signaldesk-rich-timeline-desktop.png', fullPage: false });
  await page.setViewportSize({ width: 390, height: 844 });
  await richBody.scrollIntoViewIfNeeded();
  const richBounds = await richBody.boundingBox();
  assert.ok(richBounds.x >= 0 && richBounds.x + richBounds.width <= 390, 'Formatted timeline overflow');
  await page.screenshot({ path: '/tmp/signaldesk-rich-timeline-mobile.png', fullPage: false });
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.getByRole('button', { name: 'Close issue details', exact: true }).click();

  await page.getByRole('button', { name: 'Reset filters', exact: true }).click();
  await page.getByRole('combobox', { name: 'Filter by project' }).selectOption('platform-production');
  await page.waitForFunction(() => {
    const projects = [...document.querySelectorAll('#issue-rows .project-name')];
    return projects.length > 0 && projects.every(item => item.textContent === 'platform-production');
  });
  await page.getByRole('combobox', { name: 'Filter by triggered time' }).selectOption('custom');
  await page.locator('#from').fill('2099-01-01T00:00');
  await page.getByRole('button', { name: 'Apply dates', exact: true }).click();
  await page.waitForSelector('#empty-state:not([hidden])');
  await page.getByRole('button', { name: 'Reset filters', exact: true }).click();
  await page.waitForFunction(() => document.querySelectorAll('#issue-rows tr').length === 25);

  await page.getByRole('button', { name: 'Connections', exact: true }).click();
  await page.waitForSelector('#connection-details dd');
  assert.ok((await page.locator('#connection-details').textContent()).includes('Consumer-managed PostgreSQL'));
  assert.ok((await page.locator('#connection-details').textContent()).includes('Pub/Sub pull'));
  await page.getByRole('button', { name: 'Settings', exact: true }).click();
  await page.locator('#setting-subscription').fill('projects/example-project/subscriptions/local-test-alerts');
  await page.locator('#setting-enabled').uncheck();
  await page.getByRole('button', { name: 'Save configuration', exact: true }).click();
  await page.waitForFunction(() => document.getElementById('settings-status').textContent === 'Saved');
  const savedSettings = await page.evaluate(async () => (await fetch('/api/settings')).json());
  assert.equal(savedSettings.subscription, 'projects/example-project/subscriptions/local-test-alerts');
  assert.equal(savedSettings.enabled, false);
  await page.screenshot({ path: '/tmp/signaldesk-settings.png', fullPage: false });
  const testUsers = [];
  for (const role of ['viewer', 'operator']) {
    const username = `browser-${role}-${Date.now()}`;
    await page.getByRole('button', { name: 'Users', exact: true }).click();
    await page.getByRole('button', { name: 'Add user', exact: true }).click();
    await page.locator('#user-name').fill(`Browser ${role}`);
    await page.locator('#user-username').fill(username);
    await page.locator('#user-password').fill('browser-testing-only-password');
    await page.locator('#user-role').selectOption(role);
    await page.getByRole('button', { name: 'Save user', exact: true }).click();
    await page.waitForSelector('#user-dialog:not([open])', { state: 'attached' });
    await page.getByRole('button', { name: `Edit ${username}`, exact: true }).waitFor();
    testUsers.push({ role, username });
  }
  await page.screenshot({ path: '/tmp/signaldesk-users.png', fullPage: false });
  for (const user of testUsers) {
    await page.getByRole('button', { name: 'Sign out', exact: true }).click();
    await page.waitForSelector('#access-screen:not([hidden])');
    await page.locator('#access-username').fill(user.username);
    await page.locator('#access-password').fill('browser-testing-only-password');
    await page.locator('#access-submit').click();
    await page.waitForSelector('#issue-rows tr');
    assert.equal(await page.locator('[data-view="users"]').isVisible(), false);
    assert.equal(await page.locator('[data-view="settings"]').isVisible(), false);
    assert.equal(await page.evaluate(async () => (await fetch('/api/users')).status), 403);
    await page.getByRole('tab', { name: /^Open / }).click();
    await page.waitForFunction(() => [...document.querySelectorAll('#issue-rows .badge.status')].every(item => item.classList.contains('open')));
    await page.locator('#issue-rows .issue-link').first().click();
    await page.waitForSelector('#detail-title');
    await page.waitForFunction(() => document.querySelector('#detail-title')?.textContent !== 'Loading issue');
    assert.equal(await page.locator('#acknowledge').count(), user.role === 'viewer' ? 0 : 1);
    assert.equal(await page.locator('#work-log-form').count(), user.role === 'viewer' ? 0 : 1);
    if (user.role === 'operator') {
      await page.getByRole('textbox', { name: 'Work Log', exact: true }).fill('Operator investigation recorded.');
      await page.getByRole('button', { name: 'Add Work Log', exact: true }).click();
      await page.locator('#issue-timeline .work-log-body').filter({ hasText: 'Operator investigation recorded.' }).first().waitFor();
    }
    await page.getByRole('button', { name: 'Close issue details', exact: true }).click();
  }
  await page.getByRole('button', { name: 'Sign out', exact: true }).click();
  await page.waitForSelector('#access-screen:not([hidden])');
  await page.locator('#access-username').fill('browser-admin');
  await page.locator('#access-password').fill('browser-testing-only-password');
  await page.locator('#access-submit').click();
  await page.waitForSelector('#issue-rows tr');
  await page.getByRole('button', { name: 'Reset filters', exact: true }).click();
  await page.locator('[data-view="issues"]').click();
  await page.route('**/api/issues?**', route => route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: 'Test connection interruption' }) }));
  await page.getByRole('button', { name: 'Refresh', exact: true }).click();
  await page.waitForSelector('#error:not([hidden])');
  assert.ok((await page.locator('#error').textContent()).includes('Test connection interruption'));
  await page.unroute('**/api/issues?**');
  await page.getByRole('button', { name: 'Refresh', exact: true }).click();
  await page.waitForSelector('#error[hidden]', { state: 'attached' });
  await page.screenshot({ path: '/tmp/signaldesk-desktop.png', fullPage: false });

  const viewports = [];
  for (const width of [768, 390, 320]) {
    await page.setViewportSize({ width, height: 844 });
    const dimensions = await page.evaluate(() => ({ viewport: window.innerWidth, document: document.documentElement.scrollWidth, overflowing: [...document.querySelectorAll('body *')].filter(item => { const bounds = item.getBoundingClientRect(); return bounds.width > 0 && bounds.right > window.innerWidth + 1 && getComputedStyle(item).position !== 'fixed'; }).slice(0, 8).map(item => ({ tag: item.tagName, class: item.className, width: item.getBoundingClientRect().width })) }));
    assert.ok(dimensions.document <= dimensions.viewport, `Page overflow at ${width}px: ${JSON.stringify(dimensions)}`);
    await page.getByRole('combobox', { name: 'Filter by project' }).waitFor({ state: 'visible' });
    viewports.push(dimensions);
    if (width === 390) {
      await page.screenshot({ path: '/tmp/signaldesk-mobile.png', fullPage: false });
      await page.locator('#issue-rows .issue-link').first().click();
      await page.waitForSelector('#work-log-form');
      await page.locator('#work-log-section').scrollIntoViewIfNeeded();
      const workLogBounds = await page.locator('#work-log-form').boundingBox();
      assert.ok(workLogBounds.x >= 0 && workLogBounds.x + workLogBounds.width <= width, 'Work log editor overflow');
      await page.screenshot({ path: '/tmp/signaldesk-mobile-detail.png', fullPage: false });
      await page.getByRole('button', { name: 'Close issue details', exact: true }).click();
    }
  }
  const recurrence = await page.evaluate(async () => {
    const { csrf } = await (await fetch('/api/session')).json();
    const nonce = String(Date.now());
    const started = Math.floor(Date.now() / 1000) - 120;
    const post = async (path, body) => {
      const response = await fetch(path, { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf }, body: JSON.stringify(body) });
      if (!response.ok) throw new Error(`Recurrence request failed: ${response.status} ${await response.text()}`);
      return response.json();
    };
    const notify = async (occurrence, state, offset, ended, message) => post('/api/dev/messages', {
      subscription: 'projects/browser-test/subscriptions/alerts',
      message: { messageId: `${nonce}-${message}`, data: btoa(JSON.stringify({ incident: {
        incident_id: `${nonce}-${occurrence}`, state, started_at: started + offset, ended_at: ended ? started + ended : 0,
        policy_id: 'browser-recurrence-policy', policy_name: 'Browser recurrence check',
        condition: { name: 'browser-cpu-condition' }, metric: { type: 'cpu' },
        resource_name: 'browser recurrence instance', resource: { type: 'gce_instance', labels: { project_id: 'browser-test', instance_id: nonce, zone: 'europe-west1-b' } }
      } })) }
    });
    const first = await notify('first', 'open', 0, 0, 'open');
    await post(`/api/issues/${first.id}/worklogs`, { body: 'Investigation retained across automatic recurrence.' });
    await notify('first', 'closed', 0, 30, 'close');
    const second = await notify('second', 'open', 60, 0, 'reopen');
    await notify('first', 'closed', 0, 90, 'delayed-close');
    const detail = await (await fetch(`/api/issues/${second.id}`)).json();
    return { firstID: first.id, secondID: second.id, gcpID: `${nonce}-second`, detail };
  });
  assert.equal(recurrence.firstID, recurrence.secondID, 'Recurrence created another issue');
  assert.equal(recurrence.detail.issue.status, 'open', 'Old occurrence resolved the reopened issue');
  assert.equal(recurrence.detail.issue.incidentId, recurrence.gcpID);
  assert.ok(recurrence.detail.events.some(event => event.kind === 'work_log' && event.body === 'Investigation retained across automatic recurrence.'));
  assert.equal(recurrence.detail.events[0].kind, 'stale_delivery');
  assert.ok(recurrence.detail.events.some(event => event.kind === 'reopened'));
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.reload();
  await page.getByRole('searchbox', { name: 'Search issues' }).fill('Browser recurrence check');
  await page.locator(`#issue-rows tr[data-id="${recurrence.firstID}"] .issue-link`).click();
  await page.getByRole('button', { name: 'Copy issue ID', exact: true }).waitFor();
  assert.equal((await page.locator('.stable-issue-id').innerText()).trim(), recurrence.firstID);
  assert.ok((await page.locator('.detail-metadata').innerText()).includes(recurrence.gcpID));
  await page.screenshot({ path: '/tmp/signaldesk-recurring-detail.png', fullPage: false });
  await page.locator('#issue-timeline [data-event-kind="reopened"]').scrollIntoViewIfNeeded();
  assert.ok((await page.locator('#issue-timeline [data-event-kind="reopened"]').innerText()).includes('Issue reopened'));
  await page.screenshot({ path: '/tmp/signaldesk-reopened-timeline.png', fullPage: false });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.locator('#detail-title').scrollIntoViewIfNeeded();
  const identityBounds = await page.locator('.stable-issue-id').boundingBox();
  assert.ok(identityBounds.x >= 0 && identityBounds.x + identityBounds.width <= 390, 'Stable issue ID overflow');
  await page.screenshot({ path: '/tmp/signaldesk-recurring-mobile.png', fullPage: false });
  return { passed: ['first-admin setup or login', 'user creation', 'role permissions', 'sign out', 'settings saved with pull paused', 'pagination', 'text search', 'severity filter', 'status filter', 'project filter', 'custom timestamp range', 'detail', 'acknowledgement persisted', 'raw payload', 'connections', 'network error recovery', 'responsive layout', 'work log persisted after reload', 'work log author and timestamps', 'draft retained during refresh and failed save', 'work log XSS escaping', 'operator work logs and viewer read-only access', 'action timeline', 'stable issue ID across GCP occurrences', 'old closure cannot resolve new occurrence', 'work logs retained after automatic reopening', 'stable and GCP IDs displayed separately'], total: baseline.total, viewports, snapshot: await ui.snapshot() };
}
