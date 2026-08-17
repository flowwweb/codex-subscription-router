(() => {
  'use strict';
  const state = { csrf: '', accounts: [], pending: new Map(), events: null, addKey: '', migrationReview: null, migrationInFlight: false, activeLoginAccountId: '' };
  const $ = (selector) => document.querySelector(selector);
  const title = $('#status-title');
  const detail = $('#status-detail');
  const health = $('#health-badge');
  const notice = $('#notice');
  const settingsNotice = $('#settings-notice');
  const accountsNode = $('#accounts');
  const loginDialog = $('#login-dialog');
  const settingsDialog = $('#settings-dialog');

  function requestKey() {
    if (crypto.randomUUID) return crypto.randomUUID();
    const bytes = crypto.getRandomValues(new Uint8Array(16));
    return Array.from(bytes, (value) => value.toString(16).padStart(2, '0')).join('');
  }

  function announce(message, isError = false, inSettings = false) {
    const target = inSettings ? settingsNotice : notice;
    target.hidden = !message;
    target.textContent = message;
    target.setAttribute('role', isError ? 'alert' : 'status');
  }

  async function api(url, options = {}) {
    const headers = new Headers(options.headers || {});
    if (options.body) headers.set('Content-Type', 'application/json');
    if (state.csrf && options.method && options.method !== 'GET') headers.set('X-Codex-Mux-CSRF', state.csrf);
    const response = await fetch(url, { ...options, headers, credentials: 'same-origin' });
    const payload = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(payload.error || `Request failed (${response.status})`);
    return payload;
  }

  function bootstrapNonce() {
    const params = new URLSearchParams(location.hash.slice(1));
    const nonce = params.get('bootstrap') || '';
    history.replaceState(null, '', location.pathname);
    return nonce;
  }

  async function establishSession() {
    const nonce = bootstrapNonce();
    const result = nonce
      ? await api('/v1/session/bootstrap', { method: 'POST', body: JSON.stringify({ nonce }) })
      : await api('/v1/session');
    state.csrf = result.csrfToken;
  }

  function setHealth(label, className) {
    health.textContent = label;
    health.className = `health${className ? ` ${className}` : ''}`;
  }

  function weeklyWindow(account) {
    const windows = [account.rateLimits?.primary, account.rateLimits?.secondary].filter(Boolean);
    windows.sort((left, right) => Number(left.windowDurationMins || 0) - Number(right.windowDurationMins || 0));
    return windows.at(-1) || null;
  }

  function hasCapacity(account) {
    if (!account.connected || !account.enabled || account.error) return false;
    if (account.authType && account.authType !== 'chatgpt') return false;
    const weekly = weeklyWindow(account);
    return !weekly || Number(weekly.usedPercent || 0) < 100;
  }

  function accountStatus(account) {
    if (!account.connected) return { label: account.error ? 'Unavailable' : 'Needs sign-in', className: 'warning' };
    if (!account.enabled) return { label: 'Paused', className: '' };
    if (account.error) return { label: 'Unavailable', className: 'warning' };
    if (!hasCapacity(account)) return { label: 'Depleted', className: 'warning' };
    return { label: 'Ready', className: 'ready' };
  }

  function renderSummary() {
    const usable = state.accounts.filter(hasCapacity);
    const enabled = state.accounts.filter((account) => account.connected && account.enabled && !account.error);
    const attention = state.accounts.filter((account) => !account.connected || account.error);
    if (!state.accounts.length) {
      title.textContent = 'Connect a ChatGPT account';
      detail.textContent = 'Add an account to start routing Codex work.';
      setHealth('Setup needed', 'warning');
    } else if (usable.length) {
      title.textContent = 'Ready to route';
      detail.textContent = attention.length
        ? `${usable.length} available · ${attention.length} ${attention.length === 1 ? 'needs' : 'need'} attention`
        : `${usable.length} ${usable.length === 1 ? 'account' : 'accounts'} available`;
      setHealth('Ready', 'ready');
    } else if (enabled.length) {
      const nextReset = enabled.map((account) => weeklyWindow(account)?.resetsAt).filter(Boolean).sort((left, right) => left - right)[0];
      title.textContent = 'No usage available';
      detail.textContent = nextReset ? resetText(nextReset) : 'Add an account or wait for usage to reset.';
      setHealth('Waiting', 'warning');
    } else {
      title.textContent = 'Routing is paused';
      detail.textContent = attention.length ? 'Connect an account to resume routing.' : 'Turn on an account to resume routing.';
      setHealth('Action needed', 'warning');
    }
  }

  function usageName(window, fallback) {
    const minutes = Number(window?.windowDurationMins || 0);
    if (minutes && minutes <= 360) return `${Math.max(1, Math.round(minutes / 60))}-hour`;
    if (minutes && minutes >= 6 * 24 * 60 && minutes <= 8 * 24 * 60) return 'Weekly';
    return fallback;
  }

  function resetText(timestamp) {
    if (!timestamp) return '';
    const target = new Date(timestamp * 1000);
    const delta = target.getTime() - Date.now();
    if (delta > 0 && delta < 60 * 60 * 1000) return `Resets in ${Math.max(1, Math.ceil(delta / 60000))} min`;
    if (delta > 0 && delta < 24 * 60 * 60 * 1000) return `Resets in ${Math.ceil(delta / 3600000)} hr`;
    return `Resets ${target.toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' })}`;
  }

  function appendUsage(usage, account) {
    const limits = account.rateLimits || {};
    const windows = [
      { window: limits.primary, fallback: 'Short-term' },
      { window: limits.secondary, fallback: 'Weekly' },
    ].filter((item) => item.window);
    for (const [index, item] of windows.entries()) {
      const remaining = Math.max(0, Math.min(100, Math.round(100 - Number(item.window.usedPercent || 0))));
      const row = document.createElement('div'); row.className = 'usage-row';
      const label = document.createElement('span'); label.className = 'usage-label'; label.id = `usage-${account.id}-${index}-label`; label.textContent = usageName(item.window, item.fallback);
      const value = document.createElement('span'); value.className = 'usage-value'; value.textContent = `${remaining}% left`;
      const progress = document.createElement('progress'); progress.max = 100; progress.value = remaining;
      progress.setAttribute('aria-labelledby', label.id);
      const reset = document.createElement('span'); reset.className = 'usage-reset'; reset.id = `usage-${account.id}-${index}-reset`; reset.textContent = resetText(item.window.resetsAt);
      if (reset.textContent) progress.setAttribute('aria-describedby', reset.id);
      row.append(label, value, progress);
      if (reset.textContent) row.append(reset);
      usage.append(row);
    }
  }

  function render() {
    const activeCard = document.activeElement?.closest?.('[data-account-id]');
    const activeControl = ['enabled', 'rename', 'login', 'remove'].find((name) => document.activeElement?.classList?.contains(name));
    accountsNode.replaceChildren();
    accountsNode.setAttribute('aria-busy', 'false');
    renderSummary();
    if (!state.accounts.length) {
      const empty = document.createElement('p'); empty.className = 'empty';
      const heading = document.createElement('strong'); heading.textContent = 'No accounts yet';
      const copy = document.createElement('span'); copy.textContent = 'Add an account above, or import existing accounts from Settings.';
      empty.append(heading, copy); accountsNode.append(empty); return;
    }
    for (const account of state.accounts) {
      const fragment = $('#account-template').content.cloneNode(true);
      const card = fragment.querySelector('.account-row'); card.dataset.accountId = account.id;
      fragment.querySelector('.account-name').textContent = `${account.label || 'Account'}${account.planLabel ? ` · ${account.planLabel}` : ''}`;
      fragment.querySelector('.account-identity').textContent = account.connected ? (account.email || 'Connected') : (account.error || 'Not connected');
      const status = accountStatus(account);
      const statusNode = fragment.querySelector('.account-state'); statusNode.textContent = status.label; statusNode.classList.add(status.className);
      appendUsage(fragment.querySelector('.usage'), account);
      const enabled = fragment.querySelector('.enabled');
      enabled.checked = Boolean(account.enabled);
      enabled.setAttribute('aria-label', `Use ${account.label || 'this account'} for routing`);
      fragment.querySelector('.enabled-text').textContent = enabled.checked ? 'Use this account' : 'Account paused';
      enabled.addEventListener('change', () => updateAccount(account.id, { enabled: enabled.checked }));
      fragment.querySelector('.rename').addEventListener('click', () => renameAccount(account));
      const login = fragment.querySelector('.login');
      const needsRepair = Boolean(account.error);
      login.hidden = account.connected && !needsRepair;
      login.textContent = state.pending.has(account.id) ? 'Signing in…' : (needsRepair ? 'Repair' : 'Connect');
      login.disabled = state.pending.has(account.id);
      login.addEventListener('click', () => connectAccount(account.id));
      const remove = fragment.querySelector('.remove');
      remove.hidden = account.controller;
      remove.addEventListener('click', () => removeAccount(account));
      accountsNode.append(fragment);
    }
    if (activeCard && activeControl) {
      const replacement = accountsNode.querySelector(`[data-account-id="${CSS.escape(activeCard.dataset.accountId)}"] .${activeControl}`);
      replacement?.focus();
    }
  }

  async function loadAccounts() {
    const result = await api('/v1/accounts');
    state.accounts = result.accounts || [];
    for (const account of state.accounts) {
      if (account.connected && state.pending.delete(account.id)) {
        if (state.activeLoginAccountId === account.id) closeLoginDialog();
        announce(`${account.label || account.email || 'Account'} is connected and ready.`);
      }
    }
    render();
  }

  async function updateAccount(id, change) {
    try { await api(`/v1/accounts/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify(change) }); await loadAccounts(); }
    catch (error) { announce(error.message, true); await loadAccounts().catch(() => {}); }
  }

  function renameAccount(account) {
    const label = window.prompt('Name this account', account.label || '');
    if (label !== null && label.trim()) updateAccount(account.id, { label: label.trim() });
  }

  function loginDetails(login) {
    return { code: login?.userCode || '', uri: trustedVerificationURL(login?.verificationUrl) };
  }

  function trustedVerificationURL(value) {
    if (!value) return '';
    try {
      const destination = new URL(value);
      const hostname = destination.hostname.toLowerCase();
      const trustedHost = hostname === 'chatgpt.com' || hostname.endsWith('.chatgpt.com') || hostname === 'auth.openai.com' || hostname.endsWith('.auth.openai.com');
      return destination.protocol === 'https:' && trustedHost ? destination.href : '';
    } catch (_) {
      return '';
    }
  }

  function showLogin(accountId, code, uri) {
    state.activeLoginAccountId = accountId;
    $('#login-code').textContent = code || '';
    $('#login-code').hidden = !code;
    $('#login-instruction').textContent = code ? 'Open ChatGPT and enter this code.' : 'Finish sign-in in the ChatGPT window.';
    const link = $('#login-link');
    link.hidden = !uri;
    if (!link.hidden) link.href = uri;
    $('#login-status').textContent = 'Waiting for confirmation…';
    if (!loginDialog.open) loginDialog.showModal();
    loginDialog.focus();
  }

  function closeLoginDialog() {
    state.activeLoginAccountId = '';
    if (loginDialog.open) loginDialog.close();
  }

  async function connectAccount(id) {
    if (state.pending.has(id)) return;
    const idempotencyKey = requestKey();
    state.pending.set(id, { id: '', key: idempotencyKey }); render();
    try {
      const result = await api(`/v1/accounts/${encodeURIComponent(id)}/login`, { method: 'POST', body: JSON.stringify({ mode: 'chatgptDeviceCode', idempotencyKey }) });
      state.pending.set(id, { id: result.attempt.id, key: idempotencyKey });
      const info = loginDetails(result.login || result.attempt?.result);
      showLogin(id, info.code, info.uri);
      watchLogin(id, result.attempt.id);
    } catch (error) {
      state.pending.delete(id); closeLoginDialog(); render(); announce(`Couldn’t connect this account. ${error.message}`, true);
    }
  }

  async function watchLogin(accountId, attemptId) {
    if (!attemptId) return;
    try {
      const result = await api(`/v1/login-attempts/${encodeURIComponent(attemptId)}`);
      const attempt = result.attempt;
      if (attempt.state === 'pending') { setTimeout(() => watchLogin(accountId, attemptId), 1500); return; }
      state.pending.delete(accountId); closeLoginDialog(); await loadAccounts();
      if (attempt.state === 'succeeded') announce('Account connected and ready.');
      else announce(`Couldn’t connect this account. ${attempt.error || `Sign-in ${attempt.state}.`} Try again.`, true);
    } catch (error) {
      state.pending.delete(accountId); closeLoginDialog(); render(); announce(`Sign-in status is unavailable. ${error.message}`, true);
    }
  }

  async function cancelActiveLogin() {
    const accountId = state.activeLoginAccountId;
    const pending = state.pending.get(accountId);
    if (!pending) return closeLoginDialog();
    if (!pending.id) return;
    try {
      await api(`/v1/login-attempts/${encodeURIComponent(pending.id)}/cancel`, { method: 'POST', body: '{}' });
      state.pending.delete(accountId); closeLoginDialog(); await loadAccounts(); announce('Sign-in cancelled.');
    } catch (error) { announce(error.message, true); }
  }

  async function removeAccount(account) {
    if (!window.confirm(`Remove ${account.label || 'this account'}? Its local account home will be archived.`)) return;
    try {
      await api(`/v1/accounts/${encodeURIComponent(account.id)}`, { method: 'DELETE' });
      announce('Account removed. Its local data was archived for recovery.');
      await loadAccounts(); notice.focus();
    } catch (error) { announce(error.message, true); }
  }

  async function addOrConnect() {
    const first = state.accounts.find((account) => !account.connected);
    if (first) return connectAccount(first.id);
    try {
      state.addKey ||= requestKey();
      const result = await api('/v1/accounts', { method: 'POST', body: JSON.stringify({ label: `Account ${state.accounts.length + 1}`, idempotencyKey: state.addKey }) });
      state.addKey = '';
      await loadAccounts(); await connectAccount(result.account.id);
    } catch (error) { announce(error.message, true); }
  }

  function migrationSignature(files) { return files.map((file) => `${file.name}:${file.size}:${file.lastModified}`).join('|'); }

  function canonicalExport(exported) {
    let auth = exported?.codex_auth_json ?? exported;
    if (!exported?.codex_auth_json && typeof exported?.auth_json === 'string') auth = JSON.parse(exported.auth_json);
    if (typeof auth === 'string') auth = JSON.parse(auth);
    const tokens = auth?.tokens || {};
    if (auth?.auth_mode !== 'chatgpt' || !tokens.id_token || !tokens.access_token || !tokens.refresh_token || !tokens.account_id || !auth.last_refresh) {
      throw new Error('This file is not a complete Codex ChatGPT auth export.');
    }
    return { exported, sourceAccountId: tokens.account_id };
  }

  function showMigrationReview(review) {
    const preview = $('#migration-preview'); preview.replaceChildren(); preview.hidden = false;
    const heading = document.createElement('strong'); heading.textContent = 'Review this import'; preview.append(heading);
    const list = document.createElement('ul');
    for (const item of review.items) {
      const row = document.createElement('li'); row.textContent = `${item.file.name} → ${item.targetLabel}`; list.append(row);
    }
    const note = document.createElement('p'); note.textContent = 'Existing auth files are backed up for recovery.';
    preview.append(list, note); $('#migrate-codex-lb').textContent = 'Import accounts';
  }

  async function prepareMigration(files) {
    const disconnected = state.accounts.filter((account) => !account.connected);
    const batchKey = requestKey(); const items = [];
    for (let index = 0; index < files.length; index += 1) {
      const file = files[index]; const parsed = canonicalExport(JSON.parse(await file.text())); const target = disconnected[index];
      items.push({ ...parsed, file, targetId: target?.id || '', targetLabel: target?.label || `Imported account ${index + 1}`, createKey: `codex-lb-${batchKey}-${index}` });
    }
    return { signature: migrationSignature(files), items };
  }

  async function migrateCodexLB() {
    const files = Array.from($('#codex-lb-files').files || []);
    if (!files.length) return announce('Choose at least one codex-lb auth export.', true, true);
    if (!$('#codex-lb-paused').checked) return announce('Pause codex-lb for these accounts before importing.', true, true);
    if (state.migrationInFlight) return;
    const signature = migrationSignature(files);
    if (!state.migrationReview || state.migrationReview.signature !== signature) {
      try { state.migrationReview = await prepareMigration(files); showMigrationReview(state.migrationReview); return announce('Check the account mapping, then import.', false, true); }
      catch (error) { state.migrationReview = null; return announce(`Couldn’t review this import. ${error.message}`, true, true); }
    }
    state.migrationInFlight = true;
    const button = $('#migrate-codex-lb'); button.disabled = true; let imported = 0;
    for (const item of state.migrationReview.items) {
      try {
        if (!item.targetId) {
          const created = await api('/v1/accounts', { method: 'POST', body: JSON.stringify({ label: item.targetLabel, idempotencyKey: item.createKey }) });
          item.targetId = created.account.id;
        }
        await api(`/v1/accounts/${encodeURIComponent(item.targetId)}/import/codex-lb`, { method: 'POST', body: JSON.stringify({ export: item.exported, sourcePaused: true }) });
        imported += 1;
      } catch (error) {
        state.migrationInFlight = false; button.disabled = false; await loadAccounts().catch(() => {});
        return announce(`Imported ${imported} of ${files.length}. ${item.file.name}: ${error.message}`, true, true);
      }
    }
    await loadAccounts(); state.migrationInFlight = false; state.migrationReview = null; button.disabled = false; button.textContent = 'Review import';
    $('#migration-preview').hidden = true; announce(`${imported} ${imported === 1 ? 'account' : 'accounts'} imported.`, false, true);
  }

  function setOffline(error) {
    setHealth('Offline', 'offline'); title.textContent = 'Router is offline';
    detail.textContent = 'Your accounts are safe. Start Codex Router, then reload this page.';
    accountsNode.setAttribute('aria-busy', 'false'); announce(error ? `Couldn’t reach the router. ${error.message}` : 'Couldn’t reach the router.', true);
  }

  function subscribe() {
    state.events?.close(); state.events = new EventSource('/v1/events');
    state.events.onmessage = () => loadAccounts().catch(setOffline);
    state.events.onerror = () => { state.events.close(); setTimeout(() => loadAccounts().then(subscribe).catch(setOffline), 2000); };
  }

  async function start() {
    try {
      await establishSession();
      const result = await api('/v1/health');
      $('#technical-build').textContent = result.technical?.build || 'Unavailable';
      $('#technical-state').textContent = result.technical?.stateRoot || 'Unavailable';
      $('#technical-primary').textContent = result.technical?.primaryCodexHome || 'Unavailable';
      await loadAccounts(); subscribe();
    } catch (error) { setOffline(error); }
  }

  document.querySelectorAll('[data-connect]').forEach((button) => button.addEventListener('click', addOrConnect));
  $('#open-settings').addEventListener('click', () => { settingsDialog.showModal(); settingsDialog.focus(); });
  $('#close-settings').addEventListener('click', () => settingsDialog.close());
  $('#close-login').addEventListener('click', cancelActiveLogin);
  $('#cancel-login').addEventListener('click', cancelActiveLogin);
  loginDialog.addEventListener('cancel', (event) => { event.preventDefault(); cancelActiveLogin(); });
  $('#migrate-codex-lb').addEventListener('click', migrateCodexLB);
  $('#codex-lb-files').addEventListener('change', () => {
    state.migrationReview = null; $('#migration-preview').hidden = true; $('#migrate-codex-lb').textContent = 'Review import'; announce('', false, true);
  });
  start();
})();
