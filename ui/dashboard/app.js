(() => {
  'use strict';
  const state = { csrf: '', accounts: [], pending: new Map(), events: null, addKey: '', migrationReview: null, migrationInFlight: false, activeLoginAccountId: '', loginWindow: null, loginWindowName: '' };
  const $ = (selector) => document.querySelector(selector);
  const title = $('#status-title');
  const detail = $('#status-detail');
  const health = $('#health-badge');
  const toastRegion = $('#toast-region');
  const settingsToastRegion = $('#settings-toast-region');
  const accountsNode = $('#accounts');
  const loginDialog = $('#login-dialog');
  const settingsDialog = $('#settings-dialog');

  function requestKey() {
    if (crypto.randomUUID) return crypto.randomUUID();
    const bytes = crypto.getRandomValues(new Uint8Array(16));
    return Array.from(bytes, (value) => value.toString(16).padStart(2, '0')).join('');
  }

  let toastTimer = 0;
  function announce(message, isError = false, inSettings = false) {
    const target = inSettings && settingsDialog.open ? settingsToastRegion : toastRegion;
    window.clearTimeout(toastTimer);
    if (!message) { target.replaceChildren(); return; }
    toastRegion.replaceChildren();
    settingsToastRegion.replaceChildren();
    const toast = document.createElement('div');
    toast.className = `toast ${isError ? 'error' : 'success'}`;
    toast.setAttribute('role', isError ? 'alert' : 'status');
    const icon = document.createElement('span');
    icon.className = 'toast-icon';
    icon.setAttribute('aria-hidden', 'true');
    icon.textContent = isError ? '!' : '✓';
    const copy = document.createElement('span');
    copy.textContent = message;
    toast.append(icon, copy);
    target.append(toast);
    toastTimer = isError ? 0 : window.setTimeout(() => {
      if (toast.parentNode === target) target.replaceChildren();
    }, 4200);
  }

  async function api(url, options = {}) {
    const headers = new Headers(options.headers || {});
    if (options.body) headers.set('Content-Type', 'application/json');
    if (state.csrf && options.method && options.method !== 'GET') headers.set('X-Codex-Mux-CSRF', state.csrf);
    const response = await fetch(url, { ...options, headers, credentials: 'same-origin' });
    const payload = await response.json().catch(() => ({}));
    if (!response.ok) {
      const error = new Error(payload.error || `Request failed (${response.status})`);
      error.status = response.status;
      throw error;
    }
    return payload;
  }

  function bootstrapData() {
    const params = new URLSearchParams(location.hash.slice(1));
    const data = {
      nonce: params.get('bootstrap') || '',
      connectAccount: params.get('connectAccount') || '',
      connectAttempt: params.get('connectAttempt') || '',
      connectUrl: params.get('connectUrl') || '',
    };
    history.replaceState(null, '', location.pathname);
    return data;
  }

  async function establishSession() {
    const bootstrap = bootstrapData();
    const result = bootstrap.nonce
      ? await api('/v1/session/bootstrap', { method: 'POST', body: JSON.stringify({ nonce: bootstrap.nonce }) })
      : await api('/v1/session');
    state.csrf = result.csrfToken;
    return bootstrap;
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
      const heading = document.createElement('strong'); heading.textContent = 'No accounts connected';
      empty.append(heading); accountsNode.append(empty); return;
    }
    for (const account of state.accounts) {
      const fragment = $('#account-template').content.cloneNode(true);
      const card = fragment.querySelector('.account-row'); card.dataset.accountId = account.id;
      const customLabel = account.label && !/^Account \d+$/.test(account.label) ? account.label : '';
      fragment.querySelector('.account-name').textContent = account.email || customLabel || (account.connected ? 'Connected account' : 'OpenAI account');
      const plan = fragment.querySelector('.account-plan');
      plan.textContent = account.planLabel || '';
      plan.hidden = !account.planLabel;
      fragment.querySelector('.account-identity').textContent = account.connected
        ? (account.controller ? 'Primary account' : 'Ready to route')
        : (account.error || 'Not connected');
      const status = accountStatus(account);
      const statusNode = fragment.querySelector('.account-state'); statusNode.textContent = status.label; statusNode.classList.add(status.className);
      appendUsage(fragment.querySelector('.usage'), account);
      const enabled = fragment.querySelector('.enabled');
      enabled.checked = Boolean(account.enabled);
      enabled.setAttribute('aria-label', `Use ${account.email || customLabel || 'this account'} for routing`);
      fragment.querySelector('.enabled-text').textContent = enabled.checked ? 'Use this account' : 'Account paused';
      enabled.addEventListener('change', () => updateAccount(account.id, { enabled: enabled.checked }));
      fragment.querySelector('.rename').addEventListener('click', () => renameAccount(account));
      const login = fragment.querySelector('.login');
      const needsRepair = Boolean(account.error);
      login.hidden = account.connected && !needsRepair;
      login.textContent = state.pending.has(account.id) ? 'Signing in…' : (needsRepair ? 'Repair' : 'Connect');
      login.disabled = state.pending.has(account.id);
      login.addEventListener('click', () => connectAccount(account.id, openLoginWindow()));
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
    render();
  }

  async function updateAccount(id, change) {
    try { await api(`/v1/accounts/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify(change) }); }
    catch (error) { announce(requestFailureMessage(error, 'This account could not be updated. Try again.'), true); return; }
    try { await loadAccounts(); }
    catch (_) { render(); announce('Account updated. The account list could not refresh; reopen FLOW.', true); }
  }

  function renameAccount(account) {
    const label = window.prompt('Name this account', account.label || '');
    if (label !== null && label.trim()) updateAccount(account.id, { label: label.trim() });
  }

  function loginDetails(login) {
    return { uri: codexMuxTrustedBrowserLoginURL(login?.verificationUrl) };
  }

  function codexMuxTrustedBrowserLoginURL(value) {
    if (!value) return '';
    try {
      const destination = new URL(value);
      const validOuterEncoding = !/%(?![0-9a-f]{2})/i.test(destination.search);
      const redirects = destination.searchParams.getAll('redirect_uri');
      const trustedDestination = destination.protocol === 'https:' && destination.hostname === 'auth.openai.com' && !destination.username && !destination.password && (!destination.port || destination.port === '443') && destination.pathname === '/oauth/authorize' && !destination.hash && validOuterEncoding && destination.searchParams.getAll('response_type').length === 1 && destination.searchParams.get('response_type') === 'code' && destination.searchParams.getAll('code_challenge_method').length === 1 && destination.searchParams.get('code_challenge_method') === 'S256' && destination.searchParams.getAll('state').length === 1 && Boolean(destination.searchParams.get('state')) && destination.searchParams.getAll('code_challenge').length === 1 && Boolean(destination.searchParams.get('code_challenge'));
      if (!trustedDestination || redirects.length !== 1) return '';
      const callback = new URL(redirects[0]);
      const loopback = callback.hostname === 'localhost' || callback.hostname === '127.0.0.1' || callback.hostname === '[::1]';
      const port = Number(callback.port);
      const trustedCallback = callback.protocol === 'http:' && loopback && Number.isInteger(port) && port > 0 && port <= 65535 && callback.pathname === '/auth/callback' && !callback.search && !callback.hash && !callback.username && !callback.password;
      return trustedCallback ? destination.href : '';
    } catch (_) {
      return '';
    }
  }

  function openLoginWindow() {
    state.loginWindowName = `flow-openai-connect-${requestKey()}`;
    const popup = window.open('', state.loginWindowName, 'popup,width=560,height=760');
    if (popup) {
      const document = popup.document;
      document.title = 'FLOW';
      document.documentElement.lang = 'en';
      document.head.replaceChildren();
      const viewport = document.createElement('meta');
      viewport.name = 'viewport';
      viewport.content = 'width=device-width, initial-scale=1';
      const stylesheet = document.createElement('link');
      stylesheet.rel = 'stylesheet';
      stylesheet.href = new URL('/assets/app.css', location.origin).href;
      document.head.append(viewport, stylesheet);
      document.body.replaceChildren();
      const opening = document.createElement('main');
      opening.className = 'login-opening';
      opening.setAttribute('aria-live', 'polite');
      const spinner = document.createElement('span');
      spinner.className = 'spinner';
      spinner.setAttribute('aria-hidden', 'true');
      const label = document.createElement('span');
      label.textContent = 'Opening OpenAI…';
      opening.append(spinner, label);
      document.body.append(opening);
    }
    return popup;
  }

  function showLogin(accountId, uri, popup) {
    state.activeLoginAccountId = accountId;
    state.loginWindow = popup || null;
    const link = $('#login-link');
    link.hidden = !uri;
    if (!link.hidden) link.href = uri;
    link.target = state.loginWindowName || '_blank';
    $('#login-status').textContent = 'Waiting for approval…';
    if (!loginDialog.open) loginDialog.showModal();
    loginDialog.focus();
    if (uri && popup && !popup.closed) popup.location.replace(uri);
  }

  function closeLoginDialog(closePopup = false) {
    if (closePopup && state.loginWindow && !state.loginWindow.closed) state.loginWindow.close();
    state.loginWindow = null;
    state.loginWindowName = '';
    state.activeLoginAccountId = '';
    if (loginDialog.open) loginDialog.close();
    window.focus();
  }

  function requestFailureMessage(error, fallback) {
    if (error?.status === 401) return 'Dashboard access expired. Open FLOW again.';
    return fallback;
  }

  function loginFailureMessage(error, stateName = 'failed') {
    if (error?.status === 401) return requestFailureMessage(error, '');
    const detail = String(error?.message || error || '').toLowerCase();
    if (stateName === 'cancelled') return 'Sign-in cancelled.';
    if (stateName === 'expired') return 'Sign-in expired. Try connecting again.';
    if (detail.includes('unauthorized') || detail.includes('authoriz')) {
      return 'ChatGPT did not authorize this account. Try connecting again.';
    }
    return 'This account did not connect. Try connecting again.';
  }

  async function connectAccount(id, popup = null) {
    if (state.pending.has(id)) return true;
    const idempotencyKey = requestKey();
    state.pending.set(id, { id: '', key: idempotencyKey }); render();
    try {
      const result = await api(`/v1/accounts/${encodeURIComponent(id)}/login`, { method: 'POST', body: JSON.stringify({ mode: 'chatgpt', idempotencyKey }) });
      state.pending.set(id, { id: result.attempt.id, key: idempotencyKey });
      const info = loginDetails(result.login || result.attempt?.result);
      showLogin(id, info.uri, popup);
      watchLogin(id, result.attempt.id);
      return true;
    } catch (error) {
      if (popup && !popup.closed) popup.close();
      state.pending.delete(id); closeLoginDialog(); render(); announce(loginFailureMessage(error), true);
      return false;
    }
  }

  async function finishLogin(accountId, attempt) {
    const current = state.pending.get(accountId);
    if (!current || current.id !== attempt.id || attempt.state === 'pending') return;
    state.pending.delete(accountId);
    closeLoginDialog(true);
    const terminalMessage = attempt.state === 'succeeded'
      ? 'Account connected'
      : loginFailureMessage(attempt.error, attempt.state);
    const terminalIsError = attempt.state !== 'succeeded' && attempt.state !== 'cancelled';
    try {
      await loadAccounts();
      announce(terminalMessage, terminalIsError);
    } catch (_) {
      render();
      announce(`${terminalMessage}. Reopen FLOW to refresh the account list.`, true);
    }
  }

  async function watchLogin(accountId, attemptId) {
    if (!attemptId) return;
    const active = state.pending.get(accountId);
    if (!active || active.id !== attemptId) return;
    try {
      const result = await api(`/v1/login-attempts/${encodeURIComponent(attemptId)}`);
      const current = state.pending.get(accountId);
      if (!current || current.id !== attemptId) return;
      const attempt = result.attempt;
      if (attempt.state === 'pending') { setTimeout(() => watchLogin(accountId, attemptId), 1000); return; }
      await finishLogin(accountId, attempt);
    } catch (error) {
      const current = state.pending.get(accountId);
      if (!current || current.id !== attemptId) return;
      state.pending.delete(accountId); closeLoginDialog(); render();
      announce(requestFailureMessage(error, 'Sign-in status is unavailable. Try connecting again.'), true);
    }
  }

  async function cancelActiveLogin() {
    const accountId = state.activeLoginAccountId;
    const pending = state.pending.get(accountId);
    if (!pending) return closeLoginDialog(true);
    if (!pending.id) return;
    try {
      await api(`/v1/login-attempts/${encodeURIComponent(pending.id)}/cancel`, { method: 'POST', body: '{}' });
      state.pending.delete(accountId); closeLoginDialog(true); announce('Sign-in cancelled.');
      try { await loadAccounts(); announce('Sign-in cancelled.'); }
      catch (_) { render(); announce('Sign-in cancelled. Reopen FLOW to refresh the account list.', true); }
    } catch (error) { announce(requestFailureMessage(error, 'Sign-in could not be cancelled. Try again.'), true); }
  }

  async function removeAccount(account) {
    if (!window.confirm(`Remove ${account.label || 'this account'}? Its local account home will be archived.`)) return;
    try {
      await api(`/v1/accounts/${encodeURIComponent(account.id)}`, { method: 'DELETE' });
      announce('Account removed. Its local data was archived for recovery.');
      try { await loadAccounts(); }
      catch (_) { render(); announce('Account removed. The account list could not refresh; reopen FLOW.', true); }
    } catch (error) { announce(requestFailureMessage(error, 'This account could not be removed. Try again.'), true); }
  }

  async function addOrConnect(popup = null) {
    try {
      state.addKey ||= requestKey();
      const result = await api('/v1/accounts', { method: 'POST', body: JSON.stringify({ label: `Account ${state.accounts.length + 1}`, idempotencyKey: state.addKey }) });
      let reconciled = false;
      try { await loadAccounts(); reconciled = true; }
      catch (_) { render(); }
      const loginStarted = await connectAccount(result.account.id, popup);
      if (reconciled || loginStarted) state.addKey = '';
    } catch (error) {
      if (popup && !popup.closed) popup.close();
      announce(requestFailureMessage(error, 'A new account could not be added. Try again.'), true);
    }
  }

  function resumeLogin(bootstrap) {
    if (!bootstrap?.connectAccount || !bootstrap.connectAttempt || !bootstrap.connectUrl) return;
    if (!/^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/.test(bootstrap.connectAccount) || !/^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/.test(bootstrap.connectAttempt)) {
      announce('This sign-in could not be resumed. Choose Connect again.', true);
      return;
    }
    const uri = codexMuxTrustedBrowserLoginURL(bootstrap.connectUrl);
    if (!uri) {
      announce('OpenAI sign-in could not be resumed safely. Choose Connect again.', true);
      return;
    }
    state.pending.set(bootstrap.connectAccount, { id: bootstrap.connectAttempt, key: '' });
    render();
    showLogin(bootstrap.connectAccount, uri, null);
    watchLogin(bootstrap.connectAccount, bootstrap.connectAttempt);
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
        return announce(requestFailureMessage(error, `Imported ${imported} of ${files.length}. This account could not be imported. Try again.`), true, true);
      }
    }
    state.migrationInFlight = false; state.migrationReview = null; button.disabled = false; button.textContent = 'Review import';
    $('#migration-preview').hidden = true;
    try { await loadAccounts(); announce(`${imported} ${imported === 1 ? 'account' : 'accounts'} imported.`, false, true); }
    catch (_) { render(); announce(`${imported} ${imported === 1 ? 'account' : 'accounts'} imported. Reopen FLOW to refresh the account list.`, true, true); }
  }

  function setOffline(error) {
    setHealth('Offline', 'offline'); title.textContent = 'Router is offline';
    detail.textContent = 'Your accounts are safe. Start FLOW, then reload this page.';
    accountsNode.setAttribute('aria-busy', 'false'); announce(error ? `Couldn’t reach the router. ${error.message}` : 'Couldn’t reach the router.', true);
  }

  function subscribe() {
    state.events?.close(); state.events = new EventSource('/v1/events');
    state.events.onmessage = (event) => {
      let payload = null;
      try { payload = JSON.parse(event.data); } catch (_) {}
      const attempt = payload?.type === 'account-login' ? payload.data : null;
      if (attempt?.id && attempt.state && attempt.state !== 'pending') {
        const match = Array.from(state.pending.entries()).find(([, pending]) => pending.id === attempt.id);
        if (match) { void finishLogin(match[0], attempt); return; }
      }
      loadAccounts().catch(setOffline);
    };
    state.events.onerror = () => { state.events.close(); setTimeout(() => loadAccounts().then(subscribe).catch(setOffline), 2000); };
  }

  async function start() {
    try {
      const bootstrap = await establishSession();
      const result = await api('/v1/health');
      $('#technical-build').textContent = result.technical?.build || 'Unavailable';
      $('#technical-state').textContent = result.technical?.stateRoot || 'Unavailable';
      $('#technical-primary').textContent = result.technical?.primaryCodexHome || 'Unavailable';
      await loadAccounts(); subscribe(); resumeLogin(bootstrap);
    } catch (error) { setOffline(error); }
  }

  document.querySelectorAll('[data-connect]').forEach((button) => button.addEventListener('click', () => addOrConnect(openLoginWindow())));
  $('#open-settings').addEventListener('click', () => { settingsDialog.showModal(); settingsDialog.focus(); });
  $('#close-settings').addEventListener('click', () => settingsDialog.close());
  $('#cancel-login').addEventListener('click', cancelActiveLogin);
  loginDialog.addEventListener('cancel', (event) => { event.preventDefault(); cancelActiveLogin(); });
  $('#migrate-codex-lb').addEventListener('click', migrateCodexLB);
  $('#codex-lb-files').addEventListener('change', () => {
    state.migrationReview = null; $('#migration-preview').hidden = true; $('#migrate-codex-lb').textContent = 'Review import'; announce('', false, true);
  });
  start();
})();
