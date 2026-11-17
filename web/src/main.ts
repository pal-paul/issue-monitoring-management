import '@fontsource/manrope/latin-400.css';
import '@fontsource/manrope/latin-500.css';
import '@fontsource/manrope/latin-600.css';
import '@fontsource/manrope/latin-700.css';
import '@fontsource/manrope/latin-800.css';
import '@fontsource/ibm-plex-mono/latin-400.css';
import { createIcons, Activity, Layers2, Inbox, Cable, Database, PanelsTopLeft, RefreshCw, CircleDot, TriangleAlert, ArrowUpRight, Eye, CircleCheck, RadioTower, Search, ListFilter, Folder, ChevronDown, CalendarDays, FilterX, ArrowDown, ChevronLeft, ChevronRight, ArrowRight, X, Server, ExternalLink, Copy, Check, Clock, Circle, Undo2, Code } from 'lucide';
import { Users, Settings2, LogIn, LogOut, Save, Pencil, UserPlus, ShieldAlert, CheckCheck, FlaskConical, NotebookPen } from 'lucide';
import type { Issue, IssueList, Overview, Connection, Detail, User, Session, Settings } from './types';
import { createWorkLogEditor, renderWorkLog, workLogIcons } from './work-log';
import './style.css';
import './console.css';

const icons = { Activity, Layers2, Inbox, Cable, Database, PanelsTopLeft, RefreshCw, CircleDot, TriangleAlert, ArrowUpRight, Eye, CircleCheck, RadioTower, Search, ListFilter, Folder, ChevronDown, CalendarDays, FilterX, ArrowDown, ChevronLeft, ChevronRight, ArrowRight, X, Server, ExternalLink, Copy, Check, Clock, Circle, Undo2, Code };
const paintIcons = () => createIcons({ icons: { ...icons, ...workLogIcons, Users, Settings2, LogIn, LogOut, Save, Pencil, UserPlus, ShieldAlert, CheckCheck, FlaskConical, NotebookPen }, attrs: { 'stroke-width': 1.7 } });
const element = <T extends HTMLElement = HTMLElement>(id: string) => document.getElementById(id) as T;
const input = (id: string) => element<HTMLInputElement>(id);
const select = (id: string) => element<HTMLSelectElement>(id);
const escape = (text: unknown) => String(text ?? '').replace(/[&<>"']/g, character => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[character]!);
const title = (text: string) => text.charAt(0).toUpperCase() + text.slice(1);
const relative = (date: string) => {
  const minutes = Math.max(0, Math.floor((Date.now() - new Date(date).getTime()) / 60000));
  return minutes < 1 ? 'Just now' : minutes < 60 ? `${minutes}m ago` : minutes < 1440 ? `${Math.floor(minutes / 60)}h ${minutes % 60}m ago` : `${Math.floor(minutes / 1440)}d ago`;
};
const dateTime = (date: string | null) => date ? new Date(date).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'medium' }) : 'Not set';
const badge = (value: string, type: string) => `<span class="badge ${type} ${escape(value)}"><span class="tiny-dot ${escape(value)}"></span>${escape(title(value))}</span>`;

let page = 1;
let status = 'all';
let total = 0;
let pageSize = 25;
let activeDetail: string | null = null;
let listController: AbortController | undefined;
let detailController: AbortController | undefined;
let searchTimer: ReturnType<typeof setTimeout>;
let lastRefresh = 0;
let currentView = 'issues';
let refreshing = false;
let detailLoading = false;
let activeUser: User | null = null;
let csrfToken = '';
let setupRequired = false;
let databaseConfigured = false;
let localMode = false;
let editingUser: string | null = null;
let cachedUsers: User[] = [];
const workLogDrafts = new Map<string, { body: string; status: string }>();
const hasWorkLogDraft = (id: string) => Boolean(workLogDrafts.get(id)?.body || workLogDrafts.get(id)?.status);
const savingWorkLogs = new Set<string>();
let workLogEditor: ReturnType<typeof createWorkLogEditor> | undefined;
const can = (permission: string) => activeUser?.permissions.includes(permission) ?? false;

async function api<T>(path: string, options: RequestInit = {}): Promise<T> {
  const response = await fetch(path, { ...options, headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken, ...options.headers } });
  if (!response.ok) {
    if (response.status === 401 && activeUser) showAccess(false);
    let message = `Request failed (${response.status})`;
    try { message = (await response.json()).error || message; } catch {}
    throw new Error(message);
  }
  return response.json();
}

function showError(error: unknown) {
  if (error instanceof DOMException && error.name === 'AbortError') return;
  element('error').textContent = error instanceof Error ? error.message : 'Something went wrong';
  element('error').hidden = false;
  element('system-status').innerHTML = '<span class="tiny-dot critical"></span><span>Connection interrupted</span>';
  element('db-state').textContent = 'Connection interrupted';
}

function toast(message: string) {
  element('toast').textContent = message;
  element('toast').hidden = false;
  setTimeout(() => { element('toast').hidden = true; }, 3500);
}

function query() {
  const params = new URLSearchParams({ page: String(page), limit: String(pageSize), status, severity: select('severity').value });
  if (input('search').value.trim()) params.set('q', input('search').value.trim());
  if (select('project').value) params.set('project', select('project').value);
  const range = select('range').value;
  if (range === 'custom') {
    if (input('from').value) params.set('from', new Date(input('from').value).toISOString());
    if (input('to').value) params.set('to', new Date(input('to').value).toISOString());
  } else if (range !== 'all') params.set('from', new Date(Date.now() - Number(range) * 3600000).toISOString());
  return params;
}

function updateTabs() {
  document.querySelectorAll<HTMLButtonElement>('[data-status]').forEach(button => {
    const active = button.dataset.status === status;
    button.classList.toggle('active', active);
    button.setAttribute('aria-selected', String(active));
  });
}

function renderRows(issues: Issue[]) {
  element('issue-rows').innerHTML = issues.map(issue => `<tr data-id="${escape(issue.id)}"><td class="issue-column"><button class="issue-link" data-open="${escape(issue.id)}"><span class="issue-title">${escape(issue.policyName)}</span><span class="resource-line"><i data-lucide="${issue.resourceType === 'gce_instance' ? 'server' : 'layers-2'}"></i>${escape(issue.resourceName)}</span></button></td><td>${badge(issue.severity, 'severity')}</td><td>${badge(issue.status, 'status')}</td><td><span class="project-name" title="${escape(issue.project)}">${escape(issue.project)}</span></td><td><time class="trigger-time" datetime="${escape(issue.startedAt)}" title="${escape(dateTime(issue.startedAt))}">${relative(issue.startedAt)}</time></td><td><button class="icon-button row-open" data-open="${escape(issue.id)}" aria-label="View ${escape(issue.policyName)}" title="View issue"><i data-lucide="chevron-right"></i></button></td></tr>`).join('');
  element('empty-state').hidden = issues.length > 0;
  element('empty-message').textContent = total === 0 && !input('search').value && status === 'all' && select('severity').value === 'all' && !select('project').value && select('range').value === 'all' ? 'No incidents have been received.' : 'No issues match the current filters.';
  element('page-summary').textContent = total ? `Showing ${(page - 1) * pageSize + 1}–${Math.min(page * pageSize, total)} of ${total} issues` : '0 issues';
  element('result-label').textContent = `${total} ${total === 1 ? 'issue' : 'issues'}`;
  element('page-number').textContent = `Page ${page} of ${Math.max(1, Math.ceil(total / pageSize))}`;
  element<HTMLButtonElement>('previous').disabled = page <= 1;
  element<HTMLButtonElement>('next').disabled = page * pageSize >= total;
  element('issue-rows').querySelectorAll<HTMLButtonElement>('[data-open]').forEach(button => button.addEventListener('click', () => void openDetail(button.dataset.open!)));
  paintIcons();
}

function renderOverview(data: Overview) {
  element('coverage-rate').textContent = `${data.total ? Math.round((data.acknowledged + data.resolved) / data.total * 100) : 0}%`;
  element('coverage-total').textContent = `${data.total} incidents`;
  element('coverage-ack').style.width = `${data.total ? data.acknowledged / data.total * 100 : 0}%`;
  element('coverage-resolved').style.width = `${data.total ? data.resolved / data.total * 100 : 0}%`;
  for (const key of ['open', 'critical', 'acknowledged', 'resolved'] as const) element(`metric-${key}`).textContent = String(data[key]);
  for (const key of ['open', 'acknowledged', 'resolved'] as const) element(`tab-${key}`).textContent = String(data[key]);
  element('tab-all').textContent = String(data.total);
  element('nav-count').textContent = String(data.open + data.acknowledged);
  const projects = select('project');
  const selectedProject = projects.value;
  projects.innerHTML = '<option value="">All projects</option>' + data.projects.map(project => `<option value="${escape(project)}">${escape(project)}</option>`).join('');
  projects.value = selectedProject;
  const highest = Math.max(3, ...data.hours.map(bucket => bucket.critical + bucket.warning + bucket.info));
  const activityCount = data.hours.reduce((count, bucket) => count + bucket.critical + bucket.warning + bucket.info, 0);
  element('activity-total').textContent = String(activityCount);
  element('activity-chart').setAttribute('aria-label', `${activityCount} incidents in the last 24 hours`);
  element('activity-chart').innerHTML = data.hours.map(bucket => {
    const sum = bucket.critical + bucket.warning + bucket.info;
    return `<div class="chart-column" title="${escape(dateTime(bucket.hour))}: ${sum} incidents"><div class="bar-stack" style="height:${Math.max(sum ? 5 : 2, sum / highest * 100)}%">${sum ? (['info', 'warning', 'critical'] as const).map(severity => bucket[severity] ? `<span class="bar ${severity}" style="flex:${bucket[severity]}"></span>` : '').join('') : '<span class="bar zero"></span>'}</div></div>`;
  }).join('');
  element('last-received').textContent = data.lastReceived ? `Last delivery ${relative(data.lastReceived)}` : 'Waiting for first alert';
}

async function refresh() {
  listController?.abort();
  const controller = new AbortController();
  listController = controller;
  refreshing = true;
  element('refresh').classList.add('refreshing');
  element('refresh-label').textContent = 'Updating';
  try {
    const [list, overview] = await Promise.all([
      api<IssueList>(`/api/issues?${query()}`, { signal: controller.signal }),
      api<Overview>('/api/overview', { signal: controller.signal })
    ]);
    if (controller.signal.aborted) return;
    total = list.total;
    pageSize = list.limit;
    if (page > 1 && (page - 1) * pageSize >= total) { page = Math.max(1, Math.ceil(total / pageSize)); void refresh(); return; }
    renderRows(list.issues);
    renderOverview(overview);
    lastRefresh = Date.now();
    element('error').hidden = true;
    element('system-status').innerHTML = '<span class="connection-dot"></span><span>Database connected</span>';
    element('db-state').textContent = 'Connected';
  } catch (error) { showError(error); }
  finally {
    if (listController === controller) {
      refreshing = false;
      element('refresh').classList.remove('refreshing');
      element('refresh-label').textContent = 'Live updates';
    }
  }
}

const dialog = element<HTMLDialogElement>('issue-dialog');
dialog.addEventListener('close', () => { workLogEditor?.destroy(); workLogEditor = undefined; });

function renderDetail(detail: Detail) {
  workLogEditor?.destroy();
  workLogEditor = undefined;
  const { issue, events } = detail;
  const documentation = issue.payload?.incident?.documentation?.content;
  const labels = issue.payload?.incident?.resource?.labels || {};
  element('detail-content').innerHTML = `<div class="detail-heading"><div class="detail-badges">${badge(issue.severity, 'severity')}${badge(issue.status, 'status')}</div><h2 id="detail-title">${escape(issue.policyName)}</h2><p class="detail-resource"><i data-lucide="layers-2"></i>${escape(issue.resourceName)}</p></div>
    <div class="detail-actions">${issue.status !== 'resolved' ? `<button class="button primary" id="acknowledge"><i data-lucide="${issue.status === 'acknowledged' ? 'undo-2' : 'check'}"></i>${issue.status === 'acknowledged' ? 'Remove acknowledgement' : 'Acknowledge issue'}</button>` : '<span class="resolved-label"><i data-lucide="circle-check"></i>Resolved</span>'}${issue.monitoringUrl ? `<a class="button secondary" href="${escape(issue.monitoringUrl)}" target="_blank" rel="noopener noreferrer"><i data-lucide="external-link"></i>Monitoring</a>` : ''}</div>
    <section class="detail-section"><h3>Summary</h3><p class="summary-text">${escape(issue.summary || 'No summary provided.')}</p></section>
    <dl class="detail-metadata"><div><dt>Issue ID</dt><dd class="mono incident-id stable-issue-id"><span>${escape(issue.id)}</span><button class="icon-button small" id="copy-id" title="Copy issue ID" aria-label="Copy issue ID"><i data-lucide="copy"></i></button></dd></div><div><dt>GCP incident ID</dt><dd class="mono incident-id">${escape(issue.incidentId)}</dd></div><div><dt>Project</dt><dd>${escape(issue.project)}</dd></div><div><dt>Resource type</dt><dd class="mono">${escape(issue.resourceType || 'Not specified')}</dd></div><div><dt>Triggered</dt><dd>${escape(dateTime(issue.startedAt))}</dd></div>${issue.acknowledgedAt ? `<div><dt>Acknowledged</dt><dd>${escape(dateTime(issue.acknowledgedAt))}</dd></div>` : ''}${issue.endedAt ? `<div><dt>Resolved</dt><dd>${escape(dateTime(issue.endedAt))}</dd></div>` : ''}<div><dt>Last updated</dt><dd>${escape(dateTime(issue.updatedAt))}</dd></div></dl>
    ${documentation ? `<section class="detail-section"><h3>Documentation</h3><p class="summary-text documentation">${escape(documentation)}</p></section>` : ''}
    ${Object.keys(labels).length ? `<section class="detail-section"><h3>Resource labels</h3><div class="resource-labels">${Object.entries(labels).map(([key, value]) => `<span><b>${escape(key)}</b>${escape(value)}</span>`).join('')}</div></section>` : ''}
    <section class="detail-section" id="issue-timeline"></section>
    <details class="raw-payload"><summary><i data-lucide="code"></i>Raw notification<i data-lucide="chevron-down"></i></summary><pre>${escape(JSON.stringify(issue.payload, null, 2))}</pre></details>`;
  const activitySection = element('issue-timeline');
  activitySection.innerHTML = `<h3>Timeline <span class="section-count">${events.length}</span></h3>
    <ol class="timeline">${events.map((event, index) => {
      const eventTitle = ({ open: index === events.length - 1 ? 'Incident opened' : 'Monitoring update received', reopened: 'Issue reopened', closed: 'Incident resolved', acknowledged: 'Acknowledged', unacknowledged: 'Acknowledgement removed', stale_delivery: 'Earlier notification received', work_log: 'Work log added', status_changed: 'Status changed' } as Record<string, string>)[event.kind] || event.kind;
      const icon = event.kind === 'work_log' ? 'notebook-pen' : event.kind === 'status_changed' ? 'arrow-right' : event.kind === 'closed' ? 'circle-check' : event.kind === 'acknowledged' ? 'eye' : 'circle-dot';
      return `<li data-event-kind="${escape(event.kind)}"><span class="timeline-dot ${escape(event.kind)}"><i data-lucide="${icon}"></i></span><div>
        <strong>${escape(eventTitle)}</strong><span>${escape(event.actor || 'Cloud Monitoring')} · <time datetime="${escape(event.receivedAt)}">${escape(dateTime(event.receivedAt))}</time></span>
        ${event.body ? `<div class="work-log-body ${event.bodyFormat === 'markdown' ? 'rich-text' : ''}">${event.bodyFormat === 'markdown' ? renderWorkLog(event.body) : escape(event.body)}</div>` : ''}
        ${event.messageId ? `<small>Occurred ${escape(dateTime(event.eventAt))}</small><small class="mono">Message ${escape(event.messageId)}</small>` : ''}
        ${event.payload ? `<details class="raw-payload event-payload"><summary>Notification details<i data-lucide="chevron-down"></i></summary><pre>${escape(JSON.stringify(event.payload, null, 2))}</pre></details>` : ''}
      </div></li>`;
    }).join('')}</ol>`;
  if (can('issues.worklog')) {
    activitySection.insertAdjacentHTML('beforebegin', `<section class="detail-section" id="work-log-section"><form id="work-log-form">
      <label id="work-log-label">Work Log</label><div id="work-log-editor-shell"></div><input type="hidden" id="work-log-body">
      <label for="work-log-status">New status</label><select id="work-log-status"><option value="">Keep current status (${escape(title(issue.status))})</option><option value="open">Open</option><option value="acknowledged">Acknowledged</option><option value="resolved">Resolved</option></select>
      <div class="work-log-footer"><span id="work-log-count"></span><button class="button primary" type="submit" id="save-work-log"><i data-lucide="notebook-pen"></i>Add Work Log</button></div>
      <p class="work-log-error" id="work-log-error" role="alert" hidden></p>
    </form></section>`);
    const source = input('work-log-body');
    source.value = workLogDrafts.get(issue.id)?.body || '';
    const statusSelect = select('work-log-status');
    statusSelect.value = workLogDrafts.get(issue.id)?.status || '';
    statusSelect.disabled = savingWorkLogs.has(issue.id);
    const saveButton = element<HTMLButtonElement>('save-work-log');
    const updateDraft = () => {
      workLogDrafts.set(issue.id, { body: source.value, status: statusSelect.value });
      const length = [...source.value].length;
      element('work-log-count').textContent = `${length.toLocaleString()} / 10,000`;
      saveButton.disabled = !source.value.trim() || workLogEditor?.isEmpty() === true || length > 10000 || savingWorkLogs.has(issue.id);
    };
    workLogEditor = createWorkLogEditor(element('work-log-editor-shell'), source.value, markdown => {
      source.value = markdown;
      updateDraft();
    });
    workLogEditor.setEditable(!savingWorkLogs.has(issue.id));
    updateDraft();
    statusSelect.addEventListener('change', updateDraft);
    element('work-log-form').addEventListener('submit', async event => {
      event.preventDefault();
      if (savingWorkLogs.has(issue.id) || !source.value.trim() || workLogEditor?.isEmpty() || [...source.value].length > 10000) return;
      savingWorkLogs.add(issue.id);
      detailController?.abort();
      saveButton.disabled = true;
      workLogEditor?.setEditable(false);
      statusSelect.disabled = true;
      element('work-log-error').hidden = true;
      try {
        const updated = await api<Detail>(`/api/issues/${encodeURIComponent(issue.id)}/worklogs`, { method: 'POST', body: JSON.stringify({ body: source.value, status: statusSelect.value, format: 'markdown' }) });
        workLogDrafts.delete(issue.id);
        if (activeDetail === issue.id && dialog.open) renderDetail(updated);
        toast('Work log added');
        void refresh();
      } catch (error) {
        const errorElement = element('work-log-error');
        if (activeDetail === issue.id && dialog.open && errorElement) {
          errorElement.textContent = error instanceof Error ? error.message : 'Unable to save work log';
          errorElement.hidden = false;
        }
      } finally {
        savingWorkLogs.delete(issue.id);
        const currentSaveButton = element<HTMLButtonElement>('save-work-log');
        if (activeDetail === issue.id && currentSaveButton) {
          workLogEditor?.setEditable(true);
          select('work-log-status').disabled = false;
          const currentSource = input('work-log-body').value;
          currentSaveButton.disabled = !currentSource.trim() || workLogEditor?.isEmpty() === true || [...currentSource].length > 10000;
        }
      }
    });
  }
  if (!can('issues.acknowledge')) element('acknowledge')?.remove();
  element('acknowledge')?.addEventListener('click', async () => {
    const button = element<HTMLButtonElement>('acknowledge');
    button.disabled = true;
    button.textContent = 'Saving...';
    try {
      const updated = await api<Detail>(`/api/issues/${encodeURIComponent(issue.id)}`, { method: 'PATCH', body: JSON.stringify({ acknowledged: issue.status !== 'acknowledged' }) });
      if (activeDetail === issue.id && dialog.open) renderDetail(updated);
      toast(updated.issue.status === 'acknowledged' ? 'Issue acknowledged' : 'Acknowledgement removed');
      void refresh();
    } catch (error) { showError(error); if (activeDetail === issue.id && dialog.open) renderDetail(detail); }
  });
  element('copy-id').addEventListener('click', async () => {
    try { await navigator.clipboard.writeText(issue.id); toast('Issue ID copied'); }
    catch { toast('Clipboard access unavailable'); }
  });
  paintIcons();
}

async function openDetail(id: string, silent = false) {
  if (silent && (detailLoading || hasWorkLogDraft(id) || savingWorkLogs.has(id))) return;
  detailController?.abort();
  const controller = new AbortController();
  detailController = controller;
  detailLoading = true;
  activeDetail = id;
  if (!dialog.open) {
    element('detail-content').innerHTML = '<div class="detail-loading"><span class="loading-spinner"></span><h2 id="detail-title">Loading issue</h2></div>';
    dialog.showModal();
  }
  try {
    const detail = await api<Detail>(`/api/issues/${encodeURIComponent(id)}`, { signal: controller.signal });
    if (!controller.signal.aborted && dialog.open && activeDetail === id &&
      (!silent || (!hasWorkLogDraft(id) && !savingWorkLogs.has(id)))) renderDetail(detail);
  } catch (error) {
    if (!controller.signal.aborted) { showError(error); if (!silent) dialog.close(); }
  } finally { if (detailController === controller) detailLoading = false; }
}

function resetFilters() {
  status = 'all'; page = 1;
  input('search').value = '';
  select('severity').value = 'all'; select('project').value = ''; select('range').value = 'all';
  input('from').value = ''; input('to').value = '';
  element('custom-dates').hidden = true;
  updateTabs(); void refresh();
}

async function loadConnection() {
  try {
    const data = await api<Connection>('/api/connection');
    element('connection-details').innerHTML = [ ['Source', data.source], ['Transport', data.transport], ['Subscription', data.subscription || 'Not configured'], ['Consumer', data.enabled ? data.worker.state || 'Starting' : 'Paused'], ['Last pull', data.worker.lastPoll ? dateTime(data.worker.lastPoll) : 'Not yet'], ['Last message', data.worker.lastMessage ? dateTime(data.worker.lastMessage) : 'Not yet'], ['Storage', 'Consumer-managed PostgreSQL'], ['Configuration', 'Private persistent bootstrap volume'], ['Dataset', data.demo ? 'Sample incidents' : 'Live notifications'], ...(data.worker.error ? [['Consumer error', data.worker.error]] : []) ].map(([key, value]) => `<div><dt>${escape(key)}</dt><dd>${escape(value)}</dd></div>`).join('');
    element('send-test').hidden = !localMode || !can('issues.acknowledge');
    const notice = element('environment-notice');
    notice.hidden = !data.demo;
    if (data.demo) { notice.innerHTML = '<span class="demo-dot"></span><strong>Sample workspace</strong><span>Demonstration incidents</span>'; notice.classList.add('demo-notice'); }
  } catch (error) { showError(error); }
}

element('today').textContent = new Date().toLocaleDateString(undefined, { weekday: 'long', month: 'long', day: 'numeric', year: 'numeric' });
element('refresh').addEventListener('click', () => { void refresh(); if (activeDetail) void openDetail(activeDetail, true); void loadConnection(); });
input('search').addEventListener('input', () => { clearTimeout(searchTimer); searchTimer = setTimeout(() => { page = 1; void refresh(); }, 280); });
for (const id of ['severity', 'project', 'range']) select(id).addEventListener('change', () => { page = 1; element('custom-dates').hidden = select('range').value !== 'custom'; if (select('range').value !== 'custom' || id !== 'range') void refresh(); });
element('filters').addEventListener('submit', event => { event.preventDefault(); page = 1; void refresh(); });
for (const id of ['reset-filters', 'empty-reset']) element(id).addEventListener('click', resetFilters);
document.querySelectorAll<HTMLButtonElement>('[data-status]').forEach(button => button.addEventListener('click', () => { status = button.dataset.status!; page = 1; updateTabs(); void refresh(); }));
document.querySelectorAll<HTMLButtonElement>('[data-metric]').forEach(button => button.addEventListener('click', () => {
  const metric = button.dataset.metric!;
  status = metric === 'critical' ? 'open' : metric;
  select('severity').value = metric === 'critical' ? 'critical' : 'all';
  page = 1; updateTabs(); void refresh(); element('filters').scrollIntoView({ behavior: 'smooth', block: 'center' });
}));
element('previous').addEventListener('click', () => { if (page > 1) { page--; void refresh(); } });
element('next').addEventListener('click', () => { if (page * pageSize < total) { page++; void refresh(); } });
element('close-detail').addEventListener('click', () => dialog.close());
dialog.addEventListener('click', event => { if (event.target === dialog) { const rect = dialog.getBoundingClientRect(); if (event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom) dialog.close(); } });
dialog.addEventListener('close', () => { activeDetail = null; detailController?.abort(); });
document.querySelectorAll<HTMLButtonElement>('[data-view]').forEach(button => button.addEventListener('click', () => {
  currentView = button.dataset.view!;
  document.querySelectorAll('[data-view]').forEach(item => item.classList.toggle('active', item === button));
  for (const view of ['issues', 'connections', 'users', 'settings']) element(`${view}-view`).hidden = currentView !== view;
  element('breadcrumb-view').textContent = title(currentView);
  document.title = `${title(currentView)} · Signal Desk`;
  if (currentView === 'connections') void loadConnection();
  if (currentView === 'users') void loadUsers();
  if (currentView === 'settings') void loadSettings();
}));
document.addEventListener('keydown', event => { if (event.key === '/' && !dialog.open && currentView === 'issues' && !(event.target instanceof HTMLInputElement) && !(event.target instanceof HTMLSelectElement)) { event.preventDefault(); input('search').focus(); } });
setInterval(() => {
  if (activeUser && !document.hidden && !refreshing && Date.now() - lastRefresh > 30000) { void refresh(); void loadConnection(); if (activeDetail) void openDetail(activeDetail, true); }
}, 5000);
paintIcons();
void boot();

function showAccess(setup: boolean) {
  activeUser = null; csrfToken = ''; setupRequired = setup;
  listController?.abort(); detailController?.abort(); if (dialog.open) dialog.close();
  element('issue-rows').replaceChildren(); element('detail-content').replaceChildren(); element('user-rows').replaceChildren(); cachedUsers = []; workLogDrafts.clear();
  element<HTMLDialogElement>('user-dialog').close();
  element('app').hidden = true; element('access-screen').hidden = false;
  element('access-title').textContent = setup ? 'Create your workspace' : 'Welcome back';
  element('access-eyebrow').textContent = setup ? 'FIRST ADMINISTRATOR' : 'WORKSPACE ACCESS';
  element('setup-database-label').hidden = !setup || databaseConfigured;
  input('setup-database').required = setup && !databaseConfigured;
  for (const id of ['setup-name-label', 'setup-confirm-label']) element(id).hidden = !setup;
  input('setup-name').required = setup; input('setup-confirm').required = setup;
  input('access-password').minLength = setup ? 12 : 1;
  input('access-password').autocomplete = setup ? 'new-password' : 'current-password';
  element('access-submit').innerHTML = `<i data-lucide="${setup ? 'user-plus' : 'log-in'}"></i><span>${setup ? 'Create administrator' : 'Sign in'}</span>`;
  input('access-password').value = ''; input('setup-confirm').value = '';
  element('access-error').hidden = true;
  paintIcons();
}

async function enterWorkspace(user: User, csrf: string) {
  activeUser = user; csrfToken = csrf;
  element('access-screen').hidden = true; element('app').hidden = false;
  input('access-password').value = ''; input('setup-confirm').value = ''; input('setup-database').value = '';
  document.querySelectorAll<HTMLElement>('[data-admin]').forEach(item => { item.hidden = user.role !== 'admin'; });
  const initials = user.name.split(/\s+/).map(part => part[0]).join('').slice(0, 2).toUpperCase();
  for (const id of ['operator-avatar', 'session-avatar']) { element(id).textContent = initials; element(id).title = `${user.name} · ${title(user.role)}`; }
  document.querySelector<HTMLButtonElement>('[data-view="issues"]')!.click();
  await Promise.all([refresh(), loadConnection()]);
}

async function boot() {
  try {
    const session = await api<Session>('/api/session');
    databaseConfigured = session.databaseConfigured; localMode = session.local;
    if (session.user) await enterWorkspace(session.user, session.csrf);
    else showAccess(session.setupRequired);
  } catch (error) { showAccess(false); element('access-error').textContent = error instanceof Error ? error.message : 'Server unavailable'; element('access-error').hidden = false; }
}

element('access-form').addEventListener('submit', async event => {
  event.preventDefault(); const button = element<HTMLButtonElement>('access-submit'); button.disabled = true;
  try {
    if (setupRequired && input('access-password').value !== input('setup-confirm').value) throw new Error('Passwords do not match');
    const body = { username: input('access-username').value, password: input('access-password').value, ...(setupRequired ? { name: input('setup-name').value, databaseUrl: input('setup-database').value } : {}) };
    const result = await api<{ user: User; csrf: string }>(setupRequired ? '/api/setup' : '/api/login', { method: 'POST', body: JSON.stringify(body) });
    await enterWorkspace(result.user, result.csrf);
  } catch (error) { element('access-error').textContent = error instanceof Error ? error.message : 'Unable to sign in'; element('access-error').hidden = false; }
  finally { button.disabled = false; }
});

element('logout').addEventListener('click', async () => {
  try { await api('/api/logout', { method: 'POST', body: '{}' }); showAccess(false); }
  catch (error) { showError(error); }
});

async function loadUsers() {
  try {
    cachedUsers = await api<User[]>('/api/users');
    element('user-rows').innerHTML = cachedUsers.map(user => `<tr><td><span class="user-name">${escape(user.name)}</span></td><td>${escape(user.username)}</td><td>${user.role === 'admin' ? 'Administrator' : title(user.role)}</td><td><span class="user-permissions">${user.permissions.map(escape).join(' · ')}</span></td><td><span class="user-state ${user.enabled ? '' : 'disabled'}">${user.enabled ? 'Enabled' : 'Disabled'}</span></td><td><button class="icon-button" data-edit-user="${escape(user.id)}" title="Edit ${escape(user.username)}" aria-label="Edit ${escape(user.username)}"><i data-lucide="pencil"></i></button></td></tr>`).join('');
    element('user-rows').querySelectorAll<HTMLButtonElement>('[data-edit-user]').forEach(button => button.addEventListener('click', () => editUser(cachedUsers.find(user => user.id === button.dataset.editUser))));
    paintIcons();
  } catch (error) { showError(error); }
}

function renderPermissions() {
  const role = select('user-role').value;
  element('role-permissions').textContent = role === 'admin' ? 'Read incidents · Acknowledge incidents · Add work logs · Manage users · Manage settings' : role === 'operator' ? 'Read incidents · Acknowledge incidents · Add work logs' : 'Read incidents';
}

function editUser(user?: User) {
  editingUser = user?.id || null;
  element('user-dialog-title').textContent = user ? 'Edit user' : 'Add user';
  input('user-name').value = user?.name || ''; input('user-username').value = user?.username || '';
  input('user-password').value = ''; input('user-password').required = !user;
  input('user-password').placeholder = user ? 'Unchanged' : 'At least 12 characters';
  select('user-role').value = user?.role || 'viewer'; input('user-enabled').checked = user?.enabled ?? true;
  element('user-error').hidden = true; renderPermissions(); element<HTMLDialogElement>('user-dialog').showModal();
}

element('add-user').addEventListener('click', () => editUser());
element('close-user').addEventListener('click', () => element<HTMLDialogElement>('user-dialog').close());
select('user-role').addEventListener('change', renderPermissions);
element('user-form').addEventListener('submit', async event => {
  event.preventDefault(); const button = element<HTMLButtonElement>('user-form').querySelector<HTMLButtonElement>('button[type=submit]')!; button.disabled = true;
  try {
    const id = editingUser;
    await api(id ? `/api/users/${encodeURIComponent(id)}` : '/api/users', { method: id ? 'PATCH' : 'POST', body: JSON.stringify({ username: input('user-username').value, name: input('user-name').value, password: input('user-password').value, role: select('user-role').value, enabled: input('user-enabled').checked }) });
    element<HTMLDialogElement>('user-dialog').close(); input('user-password').value = '';
    if (id === activeUser?.id) { showAccess(false); return; }
    await loadUsers(); toast('User saved');
  } catch (error) { element('user-error').textContent = error instanceof Error ? error.message : 'Unable to save user'; element('user-error').hidden = false; }
  finally { button.disabled = false; }
});

async function loadSettings() {
  const controls = element('settings-form').querySelectorAll<HTMLInputElement | HTMLButtonElement>('input,button');
  controls.forEach(control => { control.disabled = true; });
  try { const settings = await api<Settings>('/api/settings'); input('setting-subscription').value = settings.subscription; input('setting-enabled').checked = settings.enabled; }
  catch (error) { showError(error); }
  finally { controls.forEach(control => { control.disabled = false; }); }
}

element('settings-form').addEventListener('submit', async event => {
  event.preventDefault(); const button = element('settings-form').querySelector<HTMLButtonElement>('button')!; button.disabled = true;
  try { await api('/api/settings', { method: 'PUT', body: JSON.stringify({ subscription: input('setting-subscription').value, enabled: input('setting-enabled').checked }) }); element('settings-status').textContent = 'Saved'; await loadConnection(); }
  catch (error) { element('settings-status').textContent = error instanceof Error ? error.message : 'Save failed'; }
  finally { button.disabled = false; }
});
element('database-form').addEventListener('submit', async event => {
  event.preventDefault(); const button = element('database-form').querySelector<HTMLButtonElement>('button')!; button.disabled = true;
  try { await api('/api/database', { method: 'PUT', body: JSON.stringify({ databaseUrl: input('setting-database').value }) }); input('setting-database').value = ''; element('database-status').textContent = 'Saved · Restart the service to apply'; }
  catch (error) { element('database-status').textContent = error instanceof Error ? error.message : 'Save failed'; }
  finally { button.disabled = false; }
});
element('send-test').addEventListener('click', async () => {
  const button = element<HTMLButtonElement>('send-test'); button.disabled = true;
  try {
    const incident = { incident_id: `local-${Date.now()}`, state: 'open', started_at: Math.floor(Date.now() / 1000), severity: 'WARNING', policy_name: 'Local test notification', resource_name: 'Local development', resource: { type: 'test', labels: { project_id: 'local-example' } }, summary: 'Test notification received successfully.' };
    await api('/api/dev/messages', { method: 'POST', body: JSON.stringify({ subscription: 'projects/local-example/subscriptions/test-notifications', message: { messageId: crypto.randomUUID(), data: btoa(JSON.stringify({ incident })) } }) });
    toast('Test incident received'); await refresh(); await loadConnection();
  } catch (error) { showError(error); }
  finally { button.disabled = false; }
});
