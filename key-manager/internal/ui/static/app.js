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
// Models section
// ---------------------------------------------------------------------------
async function loadModels() {
  try {
    const data = await apiFetch('GET', '/api/models');
    allModels = data.models || [];
    renderModels(allModels);
    populateModelSelect(allModels);
  } catch (err) {
    $('models-loading').classList.add('hidden');
    $('models-container').classList.remove('hidden');
    $('models-container').innerHTML = '<p class="field-error">Failed to load models: ' + escapeHtml(err.message) + '</p>';
  }
}

function renderModels(models) {
  $('models-loading').classList.add('hidden');
  const container = $('models-container');
  container.classList.remove('hidden');

  if (models.length === 0) {
    container.innerHTML = '<p class="empty-state">No models accessible to your account.</p>';
    return;
  }

  // Group by namespace
  const byNs = {};
  for (const m of models) {
    const ns = m.Namespace || m.namespace || 'default';
    if (!byNs[ns]) byNs[ns] = [];
    byNs[ns].push(m);
  }

  const grid = document.createElement('div');
  grid.className = 'models-grid';

  for (const [ns, nsModels] of Object.entries(byNs)) {
    const group = document.createElement('div');
    group.className = 'namespace-group';

    const heading = document.createElement('h3');
    heading.textContent = ns;
    group.appendChild(heading);

    const list = document.createElement('ul');
    list.className = 'model-list';

    for (const m of nsModels) {
      const li = document.createElement('li');
      const name = m.Name || m.name || '';
      const isPublic = m.Public || m.public || false;
      li.className = 'model-tag' + (isPublic ? ' public' : '');
      li.textContent = name;
      list.appendChild(li);
    }

    group.appendChild(list);
    grid.appendChild(group);
  }

  container.innerHTML = '';
  container.appendChild(grid);
}

function populateModelSelect(models) {
  const sel = $('model-select');
  // Remove all options except the placeholder
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
// Keys section
// ---------------------------------------------------------------------------
async function loadKeys() {
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
    container.innerHTML = '<p class="empty-state">No API keys yet. Create one above.</p>';
    return;
  }

  const table = document.createElement('table');
  table.innerHTML = `
    <thead>
      <tr>
        <th>Client ID</th>
        <th>Model</th>
        <th>Namespace</th>
        <th>Description</th>
        <th>Created</th>
        <th>Action</th>
      </tr>
    </thead>
  `;

  const tbody = document.createElement('tbody');
  for (const k of keys) {
    const tr = document.createElement('tr');
    const clientId = k.clientId || '';
    const modelName = k.modelName || '';
    const ns = k.namespace || '';
    const desc = k.description || '';
    const created = k.createdAt ? new Date(k.createdAt).toLocaleDateString() : '';

    tr.innerHTML = `
      <td><code>${escapeHtml(clientId)}</code></td>
      <td>${escapeHtml(modelName)}</td>
      <td>${escapeHtml(ns)}</td>
      <td>${escapeHtml(desc)}</td>
      <td>${escapeHtml(created)}</td>
      <td></td>
    `;

    const revokeBtn = document.createElement('button');
    revokeBtn.className = 'btn-danger';
    revokeBtn.textContent = 'Revoke';
    revokeBtn.addEventListener('click', () => revokeKey(ns, modelName, clientId, tr));
    tr.cells[5].appendChild(revokeBtn);

    tbody.appendChild(tr);
  }
  table.appendChild(tbody);

  container.innerHTML = '';
  container.appendChild(table);
}

async function revokeKey(namespace, modelName, clientId, rowEl) {
  if (!confirm(`Revoke key "${clientId}" for model "${modelName}"? This cannot be undone.`)) {
    return;
  }
  try {
    await apiFetch('DELETE', `/api/keys/${encodeURIComponent(namespace)}/${encodeURIComponent(modelName)}/${encodeURIComponent(clientId)}`);
    rowEl.remove();
    // If the table body is now empty, re-render the empty state
    const tbody = document.querySelector('#keys-container tbody');
    if (tbody && tbody.rows.length === 0) {
      $('keys-container').innerHTML = '<p class="empty-state">No API keys yet. Create one above.</p>';
    }
  } catch (err) {
    showError('Failed to revoke key: ' + err.message);
  }
}

// ---------------------------------------------------------------------------
// Create key form
// ---------------------------------------------------------------------------
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

  const btn = $('create-btn');
  btn.disabled = true;
  btn.textContent = 'Creating...';

  try {
    const result = await apiFetch('POST', '/api/keys', { modelName, description });
    showKeyModal(result.apiKey, result.clientId);
    // Reload the keys list after creating
    $('keys-loading').classList.remove('hidden');
    $('keys-container').classList.add('hidden');
    loadKeys();
    // Reset form
    $('model-select').value = '';
    $('description-input').value = '';
  } catch (err) {
    showFieldError('create-error', 'Failed to create key: ' + err.message);
  } finally {
    btn.disabled = false;
    btn.textContent = 'Create Key';
  }
});

// ---------------------------------------------------------------------------
// Key modal
// ---------------------------------------------------------------------------
function showKeyModal(apiKey, clientId) {
  $('modal-key-value').textContent = apiKey;
  $('modal-client-id').textContent = 'Client ID: ' + clientId;
  $('key-modal').classList.remove('hidden');
}

$('copy-btn').addEventListener('click', async () => {
  const key = $('modal-key-value').textContent;
  try {
    await navigator.clipboard.writeText(key);
    $('copy-btn').textContent = 'Copied!';
    setTimeout(() => { $('copy-btn').textContent = 'Copy'; }, 2000);
  } catch {
    // Fallback: select the text
    const range = document.createRange();
    range.selectNode($('modal-key-value'));
    window.getSelection().removeAllRanges();
    window.getSelection().addRange(range);
  }
});

$('modal-close-btn').addEventListener('click', () => {
  $('key-modal').classList.add('hidden');
  $('modal-key-value').textContent = '';
});

// Close modal on overlay click
$('key-modal').addEventListener('click', (e) => {
  if (e.target === $('key-modal')) {
    $('key-modal').classList.add('hidden');
    $('modal-key-value').textContent = '';
  }
});

// ---------------------------------------------------------------------------
// Utility
// ---------------------------------------------------------------------------
function escapeHtml(str) {
  return String(str)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

// ---------------------------------------------------------------------------
// Initialise
// ---------------------------------------------------------------------------
loadModels();
loadKeys();
