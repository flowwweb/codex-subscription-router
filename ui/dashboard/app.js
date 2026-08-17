(() => {
  'use strict';
  const state = { csrf: '', accounts: [], pending: new Map(), events: null, addKey: '', migrationReview: null, migrationInFlight: false };
  const $ = (selector) => document.querySelector(selector);
  const title = $('#status-title');
  const detail = $('#status-detail');
  const badge = $('#health-badge');
  const notice = $('#notice');
  const accountsNode = $('#accounts');

  function requestKey() {
    if (crypto.randomUUID) return crypto.randomUUID();
    const bytes = crypto.getRandomValues(new Uint8Array(16));
    return Array.from(bytes, (value) => value.toString(16).padStart(2, '0')).join('');
  }

  function announce(message, isError = false) {
    notice.hidden = !message;
    notice.textContent = message;
    notice.setAttribute('role', isError ? 'alert' : 'status');
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

  function resetText(window, name) {
    if (!window) return `${name}: usage unavailable`;
    const used = Math.round(Number(window.usedPercent || 0));
    const reset = window.resetsAt ? new Date(window.resetsAt * 1000).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' }) : 'reset time unavailable';
    return `${name}: ${used}% used · Resets ${reset}`;
  }

  function render() {
    const activeCard = document.activeElement?.closest?.('[data-account-id]');
    const activeControl = ['enabled', 'rename', 'login', 'remove'].find((name) => document.activeElement?.classList?.contains(name));
    const restoreFocus = () => {
      if (!activeCard || !activeControl) return;
      const card = Array.from(accountsNode.querySelectorAll('[data-account-id]')).find((item) => item.dataset.accountId === activeCard.dataset.accountId);
      const replacement = card?.querySelector(`.${activeControl}`);
      if (replacement && !replacement.hidden) replacement.focus();
      else notice.focus();
    };
    accountsNode.replaceChildren();
    accountsNode.setAttribute('aria-busy', 'false');
    const connected = state.accounts.filter((account) => account.connected);
    if (!connected.length) {
      title.textContent = 'Connect your first subscription';
      detail.textContent = 'Sign in to the ChatGPT account you want the router to use first.';
    } else {
      title.textContent = `${connected.length} subscription${connected.length === 1 ? '' : 's'} connected`;
      detail.textContent = 'Accounts are ready for compatible app-server clients. The official Codex Windows app does not use this router.';
    }
    if (!state.accounts.length) {
      const empty = document.createElement('p');
      empty.className = 'empty';
      empty.textContent = 'No subscriptions are configured yet.';
      accountsNode.append(empty);
      restoreFocus();
      return;
    }
    for (const account of state.accounts) {
      const fragment = $('#account-template').content.cloneNode(true);
      const card = fragment.querySelector('.account-card');
      card.dataset.accountId = account.id;
      fragment.querySelector('.account-name').textContent = `${account.label || 'Subscription'}${account.planLabel ? ` · ${account.planLabel}` : ''}`;
      fragment.querySelector('.account-identity').textContent = account.connected ? (account.email || 'Connected') : (account.error || 'Not connected');
      const usage = fragment.querySelector('.usage');
      const limits = account.rateLimits || {};
      for (const line of [resetText(limits.primary, 'Short window'), resetText(limits.secondary, 'Weekly')]) {
        const p = document.createElement('p'); p.textContent = line; usage.append(p);
      }
      const enabled = fragment.querySelector('.enabled');
      enabled.checked = Boolean(account.enabled);
      enabled.setAttribute('aria-label', `Include ${account.label || 'subscription'} in routing`);
      fragment.querySelector('.enabled-text').textContent = enabled.checked ? 'Included in routing' : 'Excluded from routing';
      enabled.addEventListener('change', () => updateAccount(account.id, { enabled: enabled.checked }));
      fragment.querySelector('.rename').addEventListener('click', () => renameAccount(account));
      const login = fragment.querySelector('.login');
      login.hidden = account.connected;
      const pending = state.pending.get(account.id);
      login.textContent = pending ? 'Cancel sign-in' : 'Connect';
      login.addEventListener('click', () => pending ? cancelLogin(account.id, pending) : connectAccount(account.id));
      const remove = fragment.querySelector('.remove');
      remove.hidden = account.controller;
      remove.addEventListener('click', () => removeAccount(account));
      accountsNode.append(fragment);
    }
    restoreFocus();
  }

  async function loadAccounts() {
    const result = await api('/v1/accounts');
    state.accounts = result.accounts || [];
    for (const account of state.accounts) {
      if (account.connected && state.pending.delete(account.id)) {
        announce(`Subscription connected. ${account.email || account.label || 'This subscription'} is ready to use.`);
      }
    }
    render();
  }

  async function updateAccount(id, change) {
    try { await api(`/v1/accounts/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify(change) }); await loadAccounts(); }
    catch (error) { announce(error.message, true); await loadAccounts().catch(() => {}); }
  }

  function renameAccount(account) {
    const label = window.prompt('Name this subscription', account.label || '');
    if (label !== null && label.trim()) updateAccount(account.id, { label: label.trim() });
  }

  function loginDetails(login) {
    const code = login?.userCode || login?.user_code || login?.deviceCode || login?.device_code;
    const uri = login?.verificationUriComplete || login?.verification_uri_complete || login?.verificationUri || login?.verification_uri;
    return { code, uri };
  }

  function showLogin(code, uri) {
    notice.hidden = false;
    notice.setAttribute('role', 'status');
    notice.replaceChildren(document.createTextNode('Finish sign-in with code '));
    const value = document.createElement('span');
    value.className = 'code';
    value.textContent = code;
    notice.append(value, document.createTextNode('. '));
    if (uri && /^https:\/\//.test(uri)) {
      const link = document.createElement('a');
      link.href = uri; link.target = '_blank'; link.rel = 'noopener noreferrer'; link.textContent = 'Open ChatGPT verification';
      notice.append(link, document.createTextNode('. '));
    }
    notice.append(document.createTextNode('Waiting for ChatGPT…'));
  }

  async function connectAccount(id) {
    const idempotencyKey = requestKey();
    state.pending.set(id, { id: '', key: idempotencyKey }); render(); announce('Starting secure ChatGPT sign-in…');
    try {
      const result = await api(`/v1/accounts/${encodeURIComponent(id)}/login`, { method: 'POST', body: JSON.stringify({ mode: 'chatgptDeviceCode', idempotencyKey }) });
      state.pending.set(id, { id: result.attempt.id, key: idempotencyKey });
      const info = loginDetails(result.login || result.attempt?.result);
      if (info.code) showLogin(info.code, info.uri);
      else announce('Finish sign-in in the ChatGPT window. Waiting for confirmation…');
      watchLogin(id, result.attempt.id);
    } catch (error) {
      state.pending.delete(id); render(); announce(`We couldn’t connect this subscription. ${error.message}`, true);
    }
  }

  async function watchLogin(accountId, attemptId) {
    if (!attemptId) return;
    try {
      const result = await api(`/v1/login-attempts/${encodeURIComponent(attemptId)}`);
      const attempt = result.attempt;
      if (attempt.state === 'pending') {
        setTimeout(() => watchLogin(accountId, attemptId), 1500);
        return;
      }
      state.pending.delete(accountId);
      await loadAccounts();
      if (attempt.state === 'succeeded') announce('Subscription connected and ready to use.');
      else announce(`We couldn’t connect this subscription. ${attempt.error || `Sign-in ${attempt.state}.`} You can try again or remove it.`, true);
    } catch (error) {
      state.pending.delete(accountId); render(); announce(`Sign-in status is unavailable. ${error.message}`, true);
    }
  }

  async function cancelLogin(accountId, pending) {
    if (!pending.id) return;
    try {
      await api(`/v1/login-attempts/${encodeURIComponent(pending.id)}/cancel`, { method: 'POST', body: '{}' });
      state.pending.delete(accountId); await loadAccounts(); announce('Sign-in cancelled. No other subscriptions were changed.');
    } catch (error) { announce(error.message, true); }
  }

  async function removeAccount(account) {
    if (!window.confirm(`Remove ${account.label || 'this subscription'} from the router? Its account home will be archived.`)) return;
    try {
      await api(`/v1/accounts/${encodeURIComponent(account.id)}`, { method: 'DELETE' });
      await loadAccounts(); announce('Subscription removed. Its local account home was archived for recovery.');
    } catch (error) { announce(error.message, true); }
  }

  async function addOrConnect() {
    const first = state.accounts.find((account) => !account.connected);
    if (first) return connectAccount(first.id);
    try {
      state.addKey ||= requestKey();
      const result = await api('/v1/accounts', { method: 'POST', body: JSON.stringify({ label: `Subscription ${state.accounts.length + 1}`, idempotencyKey: state.addKey }) });
      state.addKey = '';
      await loadAccounts(); await connectAccount(result.account.id);
    } catch (error) { announce(error.message, true); }
  }

  function migrationSignature(files) {
    return files.map((file) => `${file.name}:${file.size}:${file.lastModified}`).join('|');
  }

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
    const preview = $('#migration-preview');
    preview.replaceChildren(); preview.hidden = false;
    const heading = document.createElement('strong'); heading.textContent = 'Review this one-time migration'; preview.append(heading);
    const list = document.createElement('ul');
    for (const item of review.items) {
      const row = document.createElement('li');
      row.textContent = `${item.file.name} (${item.sourceAccountId}) → ${item.targetLabel}`;
      list.append(row);
    }
    const note = document.createElement('p');
    note.textContent = 'Import keeps any replaced auth file as a protected backup. If verification fails, keep codex-lb paused and retry this batch. To roll back later, exclude or remove every migrated subscription here before resuming those accounts in codex-lb.';
    preview.append(list, note);
    $('#migrate-codex-lb').textContent = 'Import reviewed subscriptions';
  }

  async function prepareMigration(files) {
    const disconnected = state.accounts.filter((account) => !account.connected);
    const batchKey = requestKey();
    const items = [];
    for (let index = 0; index < files.length; index += 1) {
      const file = files[index];
      const parsed = canonicalExport(JSON.parse(await file.text()));
      const target = disconnected[index];
      items.push({ ...parsed, file, targetId: target?.id || '', targetLabel: target?.label || `Migrated subscription ${index + 1}`, createKey: `codex-lb-${batchKey}-${index}` });
    }
    return { signature: migrationSignature(files), items };
  }

  async function migrateCodexLB() {
    const files = Array.from($('#codex-lb-files').files || []);
    if (!files.length) return announce('Choose at least one codex-lb auth export.', true);
    if (!$('#codex-lb-paused').checked) return announce('Pause codex-lb routing for these accounts before importing.', true);
    if (state.migrationInFlight) return;
    const signature = migrationSignature(files);
    if (!state.migrationReview || state.migrationReview.signature !== signature) {
      try {
        state.migrationReview = await prepareMigration(files);
        showMigrationReview(state.migrationReview);
        return announce('Review the account mapping, then choose Import reviewed subscriptions.');
      } catch (error) {
        state.migrationReview = null;
        return announce(`Migration review failed. ${error.message}`, true);
      }
    }
    state.migrationInFlight = true;
    const button = $('#migrate-codex-lb'); button.disabled = true;
    let migrated = 0;
    let backups = 0;
    for (const item of state.migrationReview.items) {
      try {
        if (!item.targetId) {
          const created = await api('/v1/accounts', { method: 'POST', body: JSON.stringify({ label: item.targetLabel, idempotencyKey: item.createKey }) });
          item.targetId = created.account.id;
        }
        const result = await api(`/v1/accounts/${encodeURIComponent(item.targetId)}/import/codex-lb`, {
          method: 'POST', body: JSON.stringify({ export: item.exported, sourcePaused: true })
        });
        if (result.backupAvailable) backups += 1;
        migrated += 1;
      } catch (error) {
        state.migrationInFlight = false; button.disabled = false;
        await loadAccounts().catch(() => {});
        return announce(`Imported ${migrated} of ${files.length}. ${item.file.name}: ${error.message} Retry this reviewed batch while codex-lb stays paused.`, true);
      }
    }
    await loadAccounts();
    state.migrationInFlight = false; state.migrationReview = null; button.disabled = false; button.textContent = 'Review migration';
    $('#migration-preview').hidden = true;
    announce(`${migrated} subscription${migrated === 1 ? '' : 's'} migrated from codex-lb. ${backups ? `${backups} protected backup${backups === 1 ? '' : 's'} kept for rollback.` : 'No existing auth files needed backup.'}`);
  }

  function setOffline(error) {
    badge.textContent = 'Offline'; badge.classList.add('offline');
    title.textContent = 'Router is offline';
    detail.textContent = 'Your subscriptions are safe, but routing is paused. Start the installed router and reload this page.';
    accountsNode.setAttribute('aria-busy', 'false');
    announce(error ? `Could not reach the router. ${error.message}` : 'Could not reach the router.', true);
  }

  function subscribe() {
    state.events?.close();
    state.events = new EventSource('/v1/events');
    state.events.onmessage = () => loadAccounts().catch(setOffline);
    state.events.onerror = () => { state.events.close(); setTimeout(() => loadAccounts().then(subscribe).catch(setOffline), 2000); };
  }

  async function start() {
    try {
      await establishSession();
      const health = await api('/v1/health');
      $('#technical-build').textContent = health.technical?.build || 'Unavailable';
      $('#technical-state').textContent = health.technical?.stateRoot || 'Unavailable';
      $('#technical-primary').textContent = health.technical?.primaryCodexHome || 'Unavailable';
      badge.textContent = 'Running'; badge.classList.remove('offline');
      await loadAccounts(); subscribe();
    } catch (error) { setOffline(error); }
  }

  $('#connect').addEventListener('click', addOrConnect);
  $('#migrate-codex-lb').addEventListener('click', migrateCodexLB);
  $('#codex-lb-files').addEventListener('change', () => { state.migrationReview = null; $('#migration-preview').hidden = true; $('#migrate-codex-lb').textContent = 'Review migration'; });
  start();
})();
