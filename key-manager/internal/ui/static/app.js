'use strict';

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------
let allModels = [];

// ---------------------------------------------------------------------------
// DOM helpers
// ---------------------------------------------------------------------------
function $(id) { return document.getElementById(id); }

function showError(message) {
  const banner = $('error-banner');
  banner.textContent = message;
  banner.classList.remove('hidden');
}

function clearError() {
  $('error-banner').classList.add('hidden');
}

function showFieldError(id, message) {
  const el = $(id);
  el.textContent = message;
  el.classList.remove('hidden');
}

function clearFieldError(id) {
  $(id).classList.add('hidden');
}

function escapeHtml(str) {
  return String(str)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

// ---------------------------------------------------------------------------
// Dialog controller
// ---------------------------------------------------------------------------
function openDialog(id) {
  $(id).classList.remove('hidden');
}

function closeDialog(id) {
  $(id).classList.add('hidden');
}

// Close buttons (× and Cancel) carry data-close="<dialog-id>".
document.querySelectorAll('[data-close]').forEach((el) => {
  el.addEventListener('click', () => closeDialog(el.getAttribute('data-close')));
});

// Close on overlay click and Escape.
document.querySelectorAll('.dialog-overlay').forEach((overlay) => {
  overlay.addEventListener('click', (e) => {
    if (e.target === overlay) overlay.classList.add('hidden');
  });
});

document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') {
    document.querySelectorAll('.dialog-overlay:not(.hidden)').forEach((o) => o.classList.add('hidden'));
    closeAllMenus();
  }
});

// ---------------------------------------------------------------------------
// API helpers
// ---------------------------------------------------------------------------
async function apiFetch(method, path, body) {
  const opts = {
    method,
    headers: { 'Content-Type': 'application/json' },
  };
  if (body !== undefined) {
    opts.body = JSON.stringify(body);
  }
  const resp = await fetch(path, opts);
  if (!resp.ok) {
    const text = await resp.text();
    throw new Error(`${method} ${path} failed (${resp.status}): ${text.trim()}`);
  }
  if (resp.status === 204) return null;
  return resp.json();
}

// ---------------------------------------------------------------------------
// Models (populate the Create dialog's select only)
// ---------------------------------------------------------------------------
async function loadModels() {
  try {
    const data = await apiFetch('GET', '/api/models');
    allModels = data.models || [];
    populateModelSelect(allModels);
  } catch (err) {
    showError('Failed to load models: ' + err.message);
  }
}

function populateModelSelect(models) {
  const sel = $('model-select');
  while (sel.options.length > 1) sel.remove(1);

  for (const m of models) {
    const name = m.Name || m.name || '';
    const ns = m.Namespace || m.namespace || '';
    const opt = document.createElement('option');
    opt.value = name;
    opt.textContent = ns ? `${ns}/${name}` : name;
    sel.appendChild(opt);
  }
}

// ---------------------------------------------------------------------------
// Keys table
// ---------------------------------------------------------------------------
async function loadKeys() {
  $('keys-loading').classList.remove('hidden');
  $('keys-container').classList.add('hidden');
  try {
    const data = await apiFetch('GET', '/api/keys');
    renderKeys(data.keys || []);
  } catch (err) {
    $('keys-loading').classList.add('hidden');
    $('keys-container').classList.remove('hidden');
    $('keys-container').innerHTML = '<p class="field-error">Failed to load keys: ' + escapeHtml(err.message) + '</p>';
  }
}

function renderKeys(keys) {
  $('keys-loading').classList.add('hidden');
  const container = $('keys-container');
  container.classList.remove('hidden');

  if (keys.length === 0) {
    container.innerHTML = '<p class="empty-state">No API keys yet.</p>';
    return;
  }

  const table = document.createElement('table');
  table.className = 'keys-table';
  table.innerHTML = `
    <thead>
      <tr>
        <th>Name / Description</th>
        <th>Client ID</th>
        <th>Model</th>
        <th>Created</th>
        <th class="cell-action">Action</th>
      </tr>
    </thead>
  `;

  const tbody = document.createElement('tbody');
  for (const k of keys) {
    const clientId = k.clientId || '';
    const modelName = k.modelName || '';
    const ns = k.namespace || '';
    const desc = k.description || '';
    const created = k.createdAt ? formatDate(k.createdAt) : '—';

    const tr = document.createElement('tr');
    tr.innerHTML = `
      <td class="cell-name">${escapeHtml(desc || '—')}</td>
      <td class="cell-mono">${escapeHtml(clientId)}</td>
      <td>${escapeHtml(modelName)}</td>
      <td class="cell-muted">${escapeHtml(created)}</td>
      <td class="cell-action"></td>
    `;
    tr.cells[4].appendChild(buildActionMenu({ ns, modelName, clientId, desc }));
    tbody.appendChild(tr);
  }
  table.appendChild(tbody);

  container.innerHTML = '';
  container.appendChild(table);
}

function formatDate(iso) {
  const d = new Date(iso);
  if (isNaN(d)) return '—';
  return d.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' });
}

// ---------------------------------------------------------------------------
// Row action menu (kebab dropdown)
// ---------------------------------------------------------------------------
function closeAllMenus() {
  document.querySelectorAll('.menu').forEach((m) => m.remove());
}

function buildActionMenu(key) {
  const wrap = document.createElement('div');
  wrap.className = 'menu-wrap';

  const btn = document.createElement('button');
  btn.type = 'button';
  btn.className = 'btn-icon';
  btn.setAttribute('aria-label', 'Key actions');
  btn.innerHTML = `
    <svg class="icon" width="18" height="18" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
      <circle cx="12" cy="5" r="1.6"/><circle cx="12" cy="12" r="1.6"/><circle cx="12" cy="19" r="1.6"/>
    </svg>
  `;
  btn.addEventListener('click', (e) => {
    e.stopPropagation();
    const isOpen = wrap.querySelector('.menu');
    closeAllMenus();
    if (!isOpen) wrap.appendChild(buildMenu(key));
  });

  wrap.appendChild(btn);
  return wrap;
}

function buildMenu(key) {
  const menu = document.createElement('div');
  menu.className = 'menu';
  menu.innerHTML = '<div class="menu-label">Danger</div>';

  const revoke = document.createElement('button');
  revoke.type = 'button';
  revoke.className = 'menu-item destructive';
  revoke.innerHTML = `
    <svg class="icon" width="16" height="16" viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <path d="M6 6l12 12M18 6L6 18" stroke="currentColor" stroke-width="2" stroke-linecap="round"/>
    </svg>
    Revoke
  `;
  revoke.addEventListener('click', () => {
    closeAllMenus();
    openRevokeDialog(key);
  });

  menu.appendChild(revoke);
  return menu;
}

// Close any open menu when clicking elsewhere.
document.addEventListener('click', closeAllMenus);

// ---------------------------------------------------------------------------
// Create key flow
// ---------------------------------------------------------------------------
$('create-key-btn').addEventListener('click', () => {
  clearFieldError('create-error');
  $('model-select').value = '';
  $('description-input').value = '';
  openDialog('create-dialog');
});

$('create-key-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  clearFieldError('create-error');
  clearError();

  const modelName = $('model-select').value;
  const description = $('description-input').value.trim();

  if (!modelName) {
    showFieldError('create-error', 'Please select a model.');
    return;
  }

  const btn = $('create-submit-btn');
  btn.disabled = true;
  btn.textContent = 'Creating…';

  try {
    const result = await apiFetch('POST', '/api/keys', { modelName, description });
    closeDialog('create-dialog');
    showCreatedDialog(result.apiKey, result.clientId);
    loadKeys();
  } catch (err) {
    showFieldError('create-error', 'Failed to create key: ' + err.message);
  } finally {
    btn.disabled = false;
    btn.textContent = 'Create';
  }
});

// ---------------------------------------------------------------------------
// API Key Created dialog
// ---------------------------------------------------------------------------
function showCreatedDialog(apiKey, clientId) {
  $('created-client-id').value = clientId;
  $('created-api-key').value = apiKey;
  $('copy-btn').textContent = 'Copy';
  openDialog('created-dialog');
}

$('copy-btn').addEventListener('click', async () => {
  const key = $('created-api-key').value;
  try {
    await navigator.clipboard.writeText(key);
    $('copy-btn').textContent = 'Copied!';
    setTimeout(() => { $('copy-btn').textContent = 'Copy'; }, 2000);
  } catch {
    $('created-api-key').select();
  }
});

$('download-btn').addEventListener('click', () => {
  const clientId = $('created-client-id').value;
  const apiKey = $('created-api-key').value;
  const contents = `Client ID: ${clientId}\nAPI Key: ${apiKey}\n`;
  const blob = new Blob([contents], { type: 'text/plain' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = `${clientId || 'api-key'}.txt`;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
});

$('created-done-btn').addEventListener('click', () => {
  closeDialog('created-dialog');
  $('created-client-id').value = '';
  $('created-api-key').value = '';
});

// ---------------------------------------------------------------------------
// Revoke flow
// ---------------------------------------------------------------------------
let pendingRevoke = null;

function openRevokeDialog(key) {
  pendingRevoke = key;
  const name = key.desc || key.clientId;
  $('revoke-message').textContent =
    `This permanently disables "${name}". Any application using this key will immediately lose access. This can't be undone.`;
  openDialog('revoke-dialog');
}

$('revoke-confirm-btn').addEventListener('click', async () => {
  if (!pendingRevoke) return;
  const { ns, modelName, clientId } = pendingRevoke;
  const btn = $('revoke-confirm-btn');
  btn.disabled = true;
  try {
    await apiFetch('DELETE', `/api/keys/${encodeURIComponent(ns)}/${encodeURIComponent(modelName)}/${encodeURIComponent(clientId)}`);
    closeDialog('revoke-dialog');
    pendingRevoke = null;
    loadKeys();
  } catch (err) {
    closeDialog('revoke-dialog');
    showError('Failed to revoke key: ' + err.message);
  } finally {
    btn.disabled = false;
  }
});

// ---------------------------------------------------------------------------
// Initialise
// ---------------------------------------------------------------------------
loadModels();
loadKeys();
