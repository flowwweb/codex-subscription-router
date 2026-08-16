(() => {
  'use strict';
  const state = { csrf: '', accounts: [], pending: new Set(), events: null };
  const $ = (selector) => document.querySelector(selector);
  const title = $('#status-title');
  const detail = $('#status-detail');
  const badge = $('#health-badge');
  const notice = $('#notice');
  const accountsNode = $('#accounts');

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
    accountsNode.replaceChildren();
    accountsNode.setAttribute('aria-busy', 'false');
    const connected = state.accounts.filter((account) => account.connected);
    if (!connected.length) {
      title.textContent = 'Connect your first subscription';
      detail.textContent = 'Sign in to the ChatGPT account you want the router to use first.';
    } else {
      title.textContent = `${connected.length} subscription${connected.length === 1 ? '' : 's'} connected`;
      detail.textContent = 'New chats use available quota. Existing chats stay with their assigned subscription.';
    }
    if (!state.accounts.length) {
      const empty = document.createElement('p');
      empty.className = 'empty';
      empty.textContent = 'No subscriptions are configured yet.';
      accountsNode.append(empty);
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
      enabled.addEventListener('change', () => updateAccount(account.id, { enabled: enabled.checked }));
      fragment.querySelector('.rename').addEventListener('click', () => renameAccount(account));
      const login = fragment.querySelector('.login');
      login.hidden = account.connected;
      login.disabled = state.pending.has(account.id);
      login.textContent = state.pending.has(account.id) ? 'Waiting for ChatGPT…' : 'Connect';
      login.addEventListener('click', () => connectAccount(account.id));
      accountsNode.append(fragment);
    }
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

  function showLogin(code) {
    notice.hidden = false;
    notice.setAttribute('role', 'status');
    notice.replaceChildren(document.createTextNode('Finish sign-in with code '));
    const value = document.createElement('span');
    value.className = 'code';
    value.textContent = code;
    notice.append(value, document.createTextNode('. Waiting for ChatGPT…'));
  }

  async function connectAccount(id) {
    state.pending.add(id); render(); announce('Starting secure ChatGPT sign-in…');
    try {
      const result = await api(`/v1/accounts/${encodeURIComponent(id)}/login`, { method: 'POST', body: JSON.stringify({ mode: 'chatgptDeviceCode' }) });
      const info = loginDetails(result.login);
      if (info.code) showLogin(info.code);
      else announce('Finish sign-in in the ChatGPT window. Waiting for confirmation…');
      if (info.uri && /^https:\/\//.test(info.uri)) window.open(info.uri, '_blank', 'noopener,noreferrer');
    } catch (error) {
      state.pending.delete(id); render(); announce(`We couldn’t connect this subscription. ${error.message}`, true);
    }
  }

  async function addOrConnect() {
    const first = state.accounts.find((account) => !account.connected);
    if (first) return connectAccount(first.id);
    try {
      const result = await api('/v1/accounts', { method: 'POST', body: JSON.stringify({ label: `Subscription ${state.accounts.length + 1}` }) });
      await loadAccounts(); await connectAccount(result.account.id);
    } catch (error) { announce(error.message, true); }
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
      await api('/v1/health');
      badge.textContent = 'Running'; badge.classList.remove('offline');
      await loadAccounts(); subscribe();
    } catch (error) { setOffline(error); }
  }

  $('#connect').addEventListener('click', addOrConnect);
  start();
})();
