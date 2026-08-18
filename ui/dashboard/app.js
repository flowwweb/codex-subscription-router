(() => {
  'use strict';
  function readPreference(key, fallback) {
    try {
      const value = localStorage.getItem(`flow.${key}`);
      if (value !== null) return value;
    } catch (_) {}
    try {
      const cookie = document.cookie.split('; ').find((item) => item.startsWith(`flow-${key}=`));
      return cookie ? decodeURIComponent(cookie.slice(`flow-${key}=`.length)) : fallback;
    } catch (_) { return fallback; }
  }

  function writePreference(key, value) {
    try { localStorage.setItem(`flow.${key}`, value); } catch (_) {}
    try { document.cookie = `flow-${key}=${encodeURIComponent(value)}; Max-Age=31536000; Path=/; SameSite=Strict`; } catch (_) {}
  }

  const state = {
    csrf: '', accounts: [], pending: new Map(), events: null, addKey: '', migrationReview: null,
    migrationInFlight: false, activeLoginAccountId: '', loginWindow: null, loginWindowName: '',
    removeRequest: null, addInFlight: false, renderError: null, activity: new Map(), combinedProfile: null, activityRequests: new Map(),
    statsRequest: 0, sort: readPreference('account-sort', 'best'),
    showSparkUsage: readPreference('show-spark-usage', 'false') === 'true',
    hideAccountEmails: readPreference('hide-account-emails', 'false') === 'true',
  };
  if (!['best', 'usage', 'available', 'name', 'recent'].includes(state.sort)) state.sort = 'best';
  writePreference('account-sort', state.sort);
  writePreference('show-spark-usage', String(state.showSparkUsage));
  writePreference('hide-account-emails', String(state.hideAccountEmails));
  const $ = (selector) => document.querySelector(selector);
  const title = $('#status-title');
  const detail = $('#status-detail');
  const health = $('#health-badge');
  const toastRegion = $('#toast-region');
  const settingsToastRegion = $('#settings-toast-region');
  const accountsNode = $('#accounts');
  const loginDialog = $('#login-dialog');
  const statsDialog = $('#stats-dialog');
  const statsTitle = $('#stats-title');
  const statsContent = $('#stats-content');
  const removeDialog = $('#remove-account-dialog');
  const settingsDialog = $('#settings-dialog');
  const accountSort = $('#account-sort');
  const showSparkUsage = $('#show-spark-usage');
  const hideAccountEmails = $('#hide-account-emails');

  function createIcon(name, className) {
    const icon = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    icon.setAttribute('class', className);
    icon.setAttribute('aria-hidden', 'true');
    icon.setAttribute('viewBox', '0 0 24 24');
    const use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
    use.setAttribute('href', `#icon-${name}`);
    icon.append(use);
    return icon;
  }

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
    const copy = document.createElement('span');
    copy.textContent = message;
    toast.append(createIcon(isError ? 'triangle-alert' : 'check', 'toast-icon icon'), copy);
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

  function rateLimitWindows(account) {
    const limits = account?.rateLimits;
    if (!limits || typeof limits !== 'object') return [];
    return [limits.primary, limits.secondary]
      .filter((window) => window && typeof window === 'object')
      .sort((left, right) => Number(left.windowDurationMins || 0) - Number(right.windowDurationMins || 0));
  }

  function weeklyWindow(account) { return rateLimitWindows(account).at(-1) || null; }

  function hasCapacity(account) {
    if (!account.connected || !account.enabled || account.error) return false;
    if (account.authType && account.authType !== 'chatgpt') return false;
    const weekly = weeklyWindow(account);
    return !weekly || Number(weekly.usedPercent || 0) < 100;
  }

  function accountStatus(account) {
    if (!account.connected) return { label: account.error ? 'Unavailable' : 'Needs sign-in', className: 'warning', icon: 'warning' };
    if (account.authType && account.authType !== 'chatgpt') return { label: 'ChatGPT subscription required', className: 'warning', icon: 'warning' };
    if (!account.enabled) return { label: 'Paused', className: '', icon: 'pause' };
    if (account.error) return { label: 'Unavailable', className: 'warning', icon: 'warning' };
    if (!hasCapacity(account)) return { label: 'Depleted', className: 'warning', icon: 'warning' };
    return { label: 'Ready', className: 'ready', icon: 'check' };
  }

  function isPlaceholderAccount(account) {
    const label = String(account.label || '').trim();
    return !account.connected && !account.email && !account.error && (!label || label === 'OpenAI account' || /^(?:Imported )?(?:OpenAI )?(?:account|subscription) \d+$/i.test(label));
  }

  function accountDisplayName(account) {
    const label = String(account.label || '').trim();
    const generatedLabel = /^(?:Imported )?(?:OpenAI )?(?:account|subscription) \d+$/i.test(label);
    const name = account.email || (label && !generatedLabel ? label : '') || 'OpenAI account';
    if (!state.hideAccountEmails || !account.email) return name;
    const [local, domain] = String(name).split('@');
    if (!domain) return '••••••';
    const suffix = domain.includes('.') ? `.${domain.split('.').at(-1)}` : '';
    const localMask = local.length > 1 ? `${local[0]}•••` : '••••';
    return `${localMask}@••••${suffix}`;
  }

  function activityEligible(account) {
    return Boolean(account?.connected && account?.authType === 'chatgpt');
  }

  function dateKey(date) { return date.toISOString().slice(0, 10); }

  function recentDateKeys(days = 14, anchorValue = '') {
    const today = new Date(anchorValue || Date.now());
    if (Number.isNaN(today.getTime())) return recentDateKeys(days);
    today.setUTCHours(0, 0, 0, 0);
    return Array.from({ length: days }, (_, index) => {
      const date = new Date(today);
      date.setUTCDate(today.getUTCDate() - (days - index - 1));
      return dateKey(date);
    });
  }

  function formatCount(value) { return new Intl.NumberFormat().format(Math.max(0, Number(value) || 0)); }

  function formatTokens(value) {
    const amount = Math.max(0, Number(value) || 0);
    if (amount >= 1_000_000) return `${(amount / 1_000_000).toFixed(amount >= 10_000_000 ? 0 : 1)}M`;
    if (amount >= 1_000) return `${(amount / 1_000).toFixed(amount >= 10_000 ? 0 : 1)}k`;
    return formatCount(amount);
  }

  function formatStatsDate(value) {
    if (!value) return '';
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? '' : date.toLocaleDateString([], { dateStyle: 'medium' });
  }

  function activityBuckets(activity) {
    const buckets = activity?.profile?.stats?.daily_usage_buckets;
    if (!Array.isArray(buckets)) return new Map();
    return new Map(buckets.filter((bucket) => bucket && bucket.start_date).map((bucket) => [bucket.start_date, Math.max(0, Number(bucket.tokens) || 0)]));
  }

  function renderActivityStrip(strip, account) {
    if (!strip) return;
    strip.replaceChildren();
    if (!activityEligible(account)) { strip.hidden = true; return; }
    const activity = state.activity.get(account.id);
    strip.hidden = false;
    const label = document.createElement('span'); label.className = 'activity-label'; label.textContent = activity?.loading || !activity ? 'Loading' : '14d';
    const keys = recentDateKeys(14, activity?.profile?.metadata?.stats_as_of);
    if (activity?.error) {
      const unavailable = document.createElement('span'); unavailable.className = 'activity-unavailable'; unavailable.textContent = 'Unavailable';
      const retry = document.createElement('button'); retry.className = 'activity-retry'; retry.type = 'button'; retry.setAttribute('aria-label', `Retry stats for ${accountDisplayName(account)}`); retry.title = 'Retry stats'; retry.append(createIcon('refresh-cw', 'icon'));
      retry.addEventListener('click', (event) => { event.stopPropagation(); void loadActivity([account], true); });
      strip.append(label, unavailable, retry);
      return;
    }
    const dots = document.createElement('span'); dots.className = `activity-dots${activity?.loading || !activity ? ' loading' : ''}`; dots.tabIndex = 0; dots.setAttribute('role', 'img');
    const buckets = activityBuckets(activity);
    const peak = Math.max(0, ...keys.map((key) => buckets.get(key) || 0));
    const description = [];
    for (const key of keys) {
      const tokens = buckets.get(key) || 0;
      const level = tokens ? Math.max(1, Math.ceil((tokens / Math.max(peak, 1)) * 4)) : 0;
      const dot = document.createElement('span'); dot.className = `activity-dot${level ? ` level-${level}` : ''}`;
      dot.title = activity?.loading || !activity ? 'Loading activity' : `${key}: ${tokens ? `${formatTokens(tokens)} tokens` : 'No activity'}`;
      if (!activity?.loading && activity) description.push(dot.title);
      dots.append(dot);
    }
    dots.setAttribute('aria-label', activity?.loading || !activity ? 'Loading read-only activity for the last 14 days' : `Read-only activity for the last 14 days: ${description.join('; ')}`);
    strip.append(label, dots);
  }

  function renderActivityStrips() {
    for (const row of document.querySelectorAll('.account-row[data-account-id]')) {
      const account = state.accounts.find((item) => item.id === row.dataset.accountId);
      if (account) renderActivityStrip(row.querySelector('.activity-strip'), account);
    }
  }

  async function loadActivity(accounts, force = false) {
    const now = Date.now();
    const targets = accounts.filter(activityEligible).filter((account) => {
      const cached = state.activity.get(account.id);
      return force || !cached || (!cached.loading && now - (cached.fetchedAt || 0) > 5 * 60 * 1000);
    });
    if (!targets.length) { renderActivityStrips(); return; }
    const requestIds = new Map();
    for (const account of targets) {
      const requestId = (state.activityRequests.get(account.id) || 0) + 1;
      state.activityRequests.set(account.id, requestId);
      requestIds.set(account.id, requestId);
      state.activity.set(account.id, { loading: true });
    }
    renderActivityStrips();
    let result;
    try { result = await api('/v1/profile/combined'); }
    catch (_) {
      for (const account of targets) {
        if (state.activityRequests.get(account.id) !== requestIds.get(account.id)) continue;
        state.activity.set(account.id, { error: true, fetchedAt: Date.now() });
      }
      renderActivityStrips();
      return;
    }
    state.combinedProfile = result.profile ? { profile: result.profile, partial: Boolean(result.partial), fetchedAt: Date.now() } : null;
    const descriptors = new Map((Array.isArray(result.accounts) ? result.accounts : []).map((account) => [account.id, account]));
    const entries = targets.map((account) => {
      const descriptor = descriptors.get(account.id);
      return [account.id, descriptor?.stats ? {
        profile: { stats: descriptor.stats, metadata: { stats_as_of: descriptor.statsAsOf || '' } },
        partial: false, fetchedAt: Date.now(),
      } : { error: true, fetchedAt: Date.now() }];
    });
    for (const [accountId, activity] of entries) {
      if (state.activityRequests.get(accountId) !== requestIds.get(accountId)) continue;
      state.activity.set(accountId, activity);
    }
    renderActivityStrips();
  }

  function appendStatMetric(container, labelText, valueText) {
    const metric = document.createElement('div'); metric.className = 'stats-metric';
    const label = document.createElement('span'); label.textContent = labelText;
    const value = document.createElement('strong'); value.textContent = valueText;
    metric.append(label, value); container.append(metric);
  }

  function renderStats(account, profile, partial = false) {
    const stats = profile?.stats;
    if (!stats) {
      renderStatsUnavailable(account);
      return;
    }
    statsContent.replaceChildren();
    const summary = document.createElement('div'); summary.className = 'stats-summary';
    appendStatMetric(summary, 'Lifetime tokens', formatTokens(stats.lifetime_tokens));
    appendStatMetric(summary, 'Threads', formatCount(stats.total_threads));
    appendStatMetric(summary, 'Current streak', `${formatCount(stats.current_streak_days)}d`);
    appendStatMetric(summary, 'Peak-day tokens', formatTokens(stats.peak_daily_tokens));
    const chartSection = document.createElement('section'); chartSection.className = 'stats-chart';
    const heading = document.createElement('div'); heading.className = 'stats-section-heading';
    const headingTitle = document.createElement('h3'); headingTitle.textContent = 'Daily activity';
    const headingCopy = document.createElement('span'); headingCopy.textContent = 'Last 14 days';
    heading.append(headingTitle, headingCopy);
    const chart = document.createElement('div'); chart.className = 'stats-bars'; chart.tabIndex = 0; chart.setAttribute('role', 'img');
    const buckets = activityBuckets({ profile });
    const keys = recentDateKeys(14, profile.metadata?.stats_as_of);
    const peak = Math.max(0, ...keys.map((key) => buckets.get(key) || 0));
    const description = [];
    for (const key of keys) {
      const tokens = buckets.get(key) || 0;
      const column = document.createElement('div'); column.className = 'stats-bar-column';
      const level = tokens ? Math.max(1, Math.ceil((tokens / Math.max(peak, 1)) * 5)) : 0;
      const bar = document.createElement('span'); bar.className = `stats-bar${level ? ` active level-${level}` : ''}`;
      bar.title = `${key}: ${tokens ? `${formatTokens(tokens)} tokens` : 'No activity'}`;
      description.push(bar.title);
      const date = document.createElement('span'); date.className = 'stats-bar-date'; date.textContent = key.slice(8, 10);
      column.append(bar, date); chart.append(column);
    }
    chart.setAttribute('aria-label', `Daily token activity for the last 14 days: ${description.join('; ')}`);
    chartSection.append(heading, chart);
    statsContent.append(summary, chartSection);
    const metadata = profile.metadata;
    const footer = document.createElement('p'); footer.className = 'stats-footer';
    footer.textContent = metadata?.stats_as_of ? `Updated ${formatStatsDate(metadata.stats_as_of)}.` : 'Stats are provided by OpenAI.';
    if (partial) footer.textContent += ' Some connected accounts are older or unavailable.';
    statsContent.append(footer);
    statsTitle.textContent = account ? accountDisplayName(account) : 'Usage stats';
  }

  function renderStatsUnavailable(account) {
    statsContent.replaceChildren();
    const empty = document.createElement('div'); empty.className = 'empty data-error';
    const heading = document.createElement('strong'); heading.textContent = 'Stats are unavailable';
    const copy = document.createElement('span'); copy.textContent = 'Try again in a moment.';
    const retry = document.createElement('button'); retry.className = 'quiet stats-retry'; retry.type = 'button'; retry.textContent = 'Try again';
    retry.addEventListener('click', () => openStats(account));
    empty.append(heading, copy, retry); statsContent.append(empty);
  }

  async function openStats(account) {
    state.statsRequest += 1;
    const requestId = state.statsRequest;
    statsTitle.textContent = account ? accountDisplayName(account) : 'Usage stats';
    statsContent.replaceChildren();
    const loading = document.createElement('div'); loading.className = 'stats-loading';
    const spinner = document.createElement('span'); spinner.className = 'spinner'; spinner.setAttribute('aria-hidden', 'true'); loading.append(spinner);
    const copy = document.createElement('span'); copy.textContent = 'Loading stats…'; loading.append(copy); statsContent.append(loading);
    if (!statsDialog.open) statsDialog.showModal();
    $('#close-stats').focus();
    const cached = account ? state.activity.get(account.id) : state.combinedProfile;
    if (cached?.profile && Date.now() - (cached.fetchedAt || 0) <= 5 * 60 * 1000) {
      renderStats(account, cached.profile, cached.partial);
      return;
    }
    try {
      const result = await api(account ? `/v1/profile/combined?accountId=${encodeURIComponent(account.id)}` : '/v1/profile/combined');
      if (requestId !== state.statsRequest) return;
      if (account) state.activity.set(account.id, { profile: result.profile, partial: Boolean(result.partial), fetchedAt: Date.now() });
      else state.combinedProfile = { profile: result.profile, partial: Boolean(result.partial), fetchedAt: Date.now() };
      renderActivityStrips();
      renderStats(account, result.profile, Boolean(result.partial));
    } catch (_) {
      if (requestId !== state.statsRequest) return;
      renderStatsUnavailable(account);
    }
  }

  function closeAccountMenus() {
    document.querySelectorAll('.account-menu[open]').forEach((menu) => menu.removeAttribute('open'));
  }

  function renderSummary() {
    const usable = state.accounts.filter(hasCapacity);
    const enabled = state.accounts.filter((account) => account.connected && account.enabled && !account.error && (!account.authType || account.authType === 'chatgpt'));
    const unsupported = state.accounts.filter((account) => account.connected && account.authType && account.authType !== 'chatgpt');
    const attention = state.accounts.filter((account) => !account.connected || account.error || unsupported.includes(account));
    if (!state.accounts.length) {
      title.textContent = 'Connect a ChatGPT account';
      detail.textContent = 'Add an account to start routing Codex work.';
      setHealth('Setup needed', 'warning');
    } else if (usable.length) {
      title.textContent = 'Ready to route';
      detail.textContent = attention.length ? 'Some accounts need attention.' : 'Routing is ready.';
      setHealth('Ready', 'ready');
    } else if (unsupported.length && !enabled.length) {
      title.textContent = 'ChatGPT subscription required';
      detail.textContent = 'Connect a ChatGPT subscription to route through FLOW.';
      setHealth('Action needed', 'warning');
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
    const allWindows = rateLimitWindows(account);
    const windows = state.showSparkUsage || allWindows.length <= 1 ? allWindows : allWindows.slice(-1);
    for (const [index, item] of windows.entries()) {
      const sourceIndex = allWindows.indexOf(item);
      const isSpark = allWindows.length > 1 ? sourceIndex < allWindows.length - 1 : Number(item?.windowDurationMins || 0) < 6 * 24 * 60;
      const remaining = Math.max(0, Math.min(100, Math.round(100 - Number(item?.usedPercent || 0))));
      const row = document.createElement('div'); row.className = `usage-row ${isSpark ? 'spark' : 'weekly'}`;
      const label = document.createElement('span'); label.className = 'usage-label'; label.id = `usage-${account.id}-${index}-label`; label.textContent = isSpark && state.showSparkUsage ? 'Spark' : usageName(item, isSpark ? 'Spark' : 'Weekly');
      const value = document.createElement('span'); value.className = 'usage-value'; value.textContent = `${remaining}% left`;
      const progress = document.createElement('progress'); progress.max = 100; progress.value = remaining;
      progress.setAttribute('aria-labelledby', label.id);
      const reset = document.createElement('span'); reset.className = 'usage-reset'; reset.id = `usage-${account.id}-${index}-reset`; reset.textContent = resetText(item?.resetsAt);
      if (reset.textContent) progress.setAttribute('aria-describedby', reset.id);
      row.append(label, value, progress);
      if (reset.textContent) row.append(reset);
      usage.append(row);
    }
    const available = Number(account.resetCredits?.availableCount || 0);
    if (available > 0) {
      const credits = document.createElement('span'); credits.className = 'reset-credits';
      const expiry = account.resetCredits?.earliestExpiry ? ` · earliest expiry ${resetCreditExpiryText(account.resetCredits.earliestExpiry)}` : '';
      credits.textContent = `${available} reset${available === 1 ? '' : 's'} available${expiry}`;
      usage.append(credits);
    }
  }

  function resetCreditExpiryText(timestamp) {
    const date = new Date(Number(timestamp) * 1000);
    return Number.isNaN(date.getTime()) ? '' : date.toLocaleDateString([], { dateStyle: 'medium' });
  }

  function remaining(window) {
    return window ? Math.max(0, Math.min(100, 100 - Number(window.usedPercent || 0))) : -1;
  }

  function compareDescending(left, right) { return right - left; }

  function compareAccounts(left, right) {
    const leftCapacity = hasCapacity(left) ? 1 : 0;
    const rightCapacity = hasCapacity(right) ? 1 : 0;
    if (state.sort === 'best') {
      if (leftCapacity !== rightCapacity) return rightCapacity - leftCapacity;
      const short = compareDescending(remaining(rateLimitWindows(left)[0]), remaining(rateLimitWindows(right)[0]));
      if (short) return short;
      const weekly = compareDescending(remaining(weeklyWindow(left)), remaining(weeklyWindow(right)));
      if (weekly) return weekly;
      if (left.threadCount !== right.threadCount) return left.threadCount - right.threadCount;
    } else if (state.sort === 'usage') {
      if (left.threadCount !== right.threadCount) return right.threadCount - left.threadCount;
    } else if (state.sort === 'available') {
      const weekly = compareDescending(remaining(weeklyWindow(left)), remaining(weeklyWindow(right)));
      if (weekly) return weekly;
      const short = compareDescending(remaining(rateLimitWindows(left)[0]), remaining(rateLimitWindows(right)[0]));
      if (short) return short;
    } else if (state.sort === 'name') {
      const name = accountDisplayName(left).localeCompare(accountDisplayName(right), undefined, { sensitivity: 'base' });
      if (name) return name;
    } else if (state.sort === 'recent' && left.createdAt !== right.createdAt) {
      return right.createdAt - left.createdAt;
    }
    return (left.createdAt || 0) - (right.createdAt || 0) || String(left.id).localeCompare(String(right.id));
  }

  function orderedAccounts() {
    return [...state.accounts].sort(compareAccounts);
  }

  function accountIdentity(account, status) {
    if (!account.connected) return account.error || 'Not connected';
    if (!account.enabled) return 'Routing paused';
    if (account.authType && account.authType !== 'chatgpt') return 'ChatGPT subscription required';
    if (account.error) return account.error;
    if (status.label === 'Depleted') return 'Waiting for reset';
    return 'Ready to route';
  }

  function renderAccounts() {
    const activeCard = document.activeElement?.closest?.('[data-account-id]');
    const activeControl = ['refresh-account', 'toggle-account', 'rename', 'login', 'remove', 'account-details-trigger', 'account-menu-trigger', 'activity-dots'].find((name) => document.activeElement?.classList?.contains(name));
    accountsNode.replaceChildren();
    accountsNode.setAttribute('aria-busy', 'false');
    renderSummary();
    accountSort.value = state.sort;
    accountSort.parentElement.hidden = state.accounts.length < 2;
    $('#open-stats').disabled = !state.accounts.some(activityEligible);
    document.querySelectorAll('[data-connect]').forEach((button) => { button.disabled = state.pending.size > 0 || state.addInFlight; });
    if (!state.accounts.length) {
      const empty = document.createElement('p'); empty.className = 'empty';
      const heading = document.createElement('strong'); heading.textContent = 'No accounts connected';
      empty.append(heading); accountsNode.append(empty); return;
    }
    for (const account of orderedAccounts()) {
      const fragment = $('#account-template').content.cloneNode(true);
      const card = fragment.querySelector('.account-row'); card.dataset.accountId = account.id;
      const displayName = accountDisplayName(account);
      const status = accountStatus(account);
      fragment.querySelector('.account-name').textContent = displayName;
      const detailsTrigger = fragment.querySelector('.account-details-trigger');
      detailsTrigger.setAttribute('aria-label', `View usage stats for ${displayName}`);
      detailsTrigger.disabled = !activityEligible(account);
      detailsTrigger.addEventListener('click', () => openStats(account));
      if (activityEligible(account)) {
        card.classList.add('is-clickable');
        card.addEventListener('click', (event) => {
          if (event.target.closest?.('button, a, details, summary, input, select')) return;
          void openStats(account);
        });
      }
      const plan = fragment.querySelector('.account-plan');
      plan.textContent = account.planLabel || '';
      plan.hidden = !account.planLabel;
      fragment.querySelector('.account-identity').textContent = accountIdentity(account, status);
      renderActivityStrip(fragment.querySelector('.activity-strip'), account);
      const statusNode = fragment.querySelector('.account-state');
      statusNode.className = `account-state${status.className ? ` ${status.className}` : ''}`;
      statusNode.querySelector('.state-label').textContent = status.label;
      statusNode.querySelectorAll('[data-state-icon]').forEach((icon) => { icon.hidden = icon.dataset.stateIcon !== status.icon; });
      appendUsage(fragment.querySelector('.usage'), account);
      const refresh = fragment.querySelector('.refresh-account');
      refresh.setAttribute('aria-label', `Refresh usage for ${displayName}`);
      refresh.addEventListener('click', refreshAccount);
      const menu = fragment.querySelector('.account-menu');
      const menuTrigger = menu.querySelector('.account-menu-trigger');
      menuTrigger.addEventListener('click', (event) => {
        document.querySelectorAll('.account-menu[open]').forEach((openMenu) => { if (openMenu !== event.currentTarget.parentElement) openMenu.removeAttribute('open'); });
      });
      menuTrigger.addEventListener('keydown', (event) => {
        if (event.key !== 'Escape') return;
        menu.removeAttribute('open');
        menuTrigger.focus();
      });
      menu.querySelector('.account-menu-panel').addEventListener('keydown', (event) => {
        if (event.key !== 'Escape') return;
        menu.removeAttribute('open');
        menuTrigger.focus();
      });
      const toggle = fragment.querySelector('.toggle-account');
      const toggleMode = account.enabled ? 'pause' : 'play';
      toggle.setAttribute('aria-label', `${account.enabled ? 'Pause' : 'Resume'} routing for ${displayName}`);
      toggle.querySelector('.toggle-copy').textContent = account.enabled ? 'Pause routing' : 'Resume routing';
      toggle.querySelectorAll('[data-toggle-icon]').forEach((icon) => { icon.hidden = icon.dataset.toggleIcon !== toggleMode; });
      toggle.addEventListener('click', () => { closeAccountMenus(); updateAccount(account.id, { enabled: !account.enabled }); });
      fragment.querySelector('.rename').addEventListener('click', () => { closeAccountMenus(); renameAccount(account); });
      const login = fragment.querySelector('.login');
      const needsRepair = Boolean(account.error);
      login.hidden = account.connected && !needsRepair;
      login.querySelector('.login-label').textContent = state.pending.has(account.id) ? 'Signing in…' : (needsRepair ? 'Repair' : 'Connect');
      login.disabled = state.pending.size > 0;
      login.addEventListener('click', () => connectAccount(account.id, openLoginWindow()));
      const remove = fragment.querySelector('.remove');
      remove.hidden = account.controller;
      remove.addEventListener('click', (event) => openRemoveDialog(account, event.currentTarget));
      accountsNode.append(fragment);
    }
    if (activeCard && activeControl) {
      const replacement = accountsNode.querySelector(`[data-account-id="${CSS.escape(activeCard.dataset.accountId)}"] .${activeControl}`);
      replacement?.focus();
    }
  }

  function render() {
    state.renderError = null;
    try {
      renderAccounts();
      return true;
    } catch (error) {
      state.renderError = error;
      accountsNode.replaceChildren();
      const empty = document.createElement('p'); empty.className = 'empty data-error';
      const heading = document.createElement('strong'); heading.textContent = 'Usage data could not be displayed';
      const copy = document.createElement('span'); copy.textContent = 'Refresh FLOW to try again.';
      empty.append(heading, copy); accountsNode.append(empty);
      setHealth('Data error', 'warning');
      title.textContent = 'Usage needs a refresh';
      detail.textContent = 'The router responded, but one account returned incomplete usage data.';
      announce('Usage data could not be displayed. Refresh FLOW to try again.', true);
      return false;
    }
  }

  function showDataError() {
    state.renderError = new Error('Router returned incomplete usage data.');
    accountsNode.replaceChildren();
    const empty = document.createElement('p'); empty.className = 'empty data-error';
    const heading = document.createElement('strong'); heading.textContent = 'Usage data could not be displayed';
    const copy = document.createElement('span'); copy.textContent = 'Refresh FLOW to try again.';
    empty.append(heading, copy); accountsNode.append(empty);
    accountsNode.setAttribute('aria-busy', 'false');
    setHealth('Data error', 'warning');
    title.textContent = 'Usage needs a refresh';
    detail.textContent = 'The router responded, but one account returned incomplete usage data.';
    announce('Usage data could not be displayed. Refresh FLOW to try again.', true);
  }

  async function loadAccounts(forceActivity = false) {
    const result = await api('/v1/accounts');
    if (!Array.isArray(result?.accounts)) return showDataError();
    state.accounts = result.accounts.filter((account) => account && typeof account === 'object' && !isPlaceholderAccount(account));
    render();
    void loadActivity(state.accounts, forceActivity);
  }

  async function updateAccount(id, change) {
    try { await api(`/v1/accounts/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify(change) }); }
    catch (error) { announce(requestFailureMessage(error, 'This account could not be updated. Try again.'), true); return; }
    try { await loadAccounts(); }
    catch (_) { render(); announce('Account updated. The account list could not refresh; reopen FLOW.', true); }
  }

  async function refreshAccount(event) {
    const button = event?.currentTarget;
    closeAccountMenus();
    button?.classList.add('is-busy');
    button?.setAttribute('aria-busy', 'true');
    if (button) button.disabled = true;
    try {
      await loadAccounts(true);
      if (!state.renderError) announce('Usage refreshed.');
    } catch (_) {
      render();
      announce('Usage could not be refreshed. Try again.', true);
    }
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
    $('#login-instruction').textContent = 'Finish sign-in in the OpenAI window.';
    $('#login-status').textContent = 'Waiting for approval…';
    $('#cancel-login').hidden = false;
    if (!loginDialog.open) loginDialog.showModal();
    loginDialog.focus();
    if (uri && popup && !popup.closed) popup.location.replace(uri);
  }

  function showLoginFinishing() {
    $('#login-instruction').textContent = 'OpenAI is done. FLOW is syncing your account.';
    $('#login-status').textContent = 'Finishing connection…';
    $('#login-link').hidden = true;
    $('#cancel-login').hidden = true;
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
    if (state.pending.has(id)) {
      if (popup && !popup.closed) popup.close();
      return true;
    }
    if (state.pending.size > 0) {
      if (popup && !popup.closed) popup.close();
      announce('Finish the current sign-in first.', true);
      return false;
    }
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
    if (current.finished) return;
    current.finished = true;
    const terminalMessage = attempt.state === 'succeeded'
      ? 'Account connected'
      : loginFailureMessage(attempt.error, attempt.state);
    const terminalIsError = attempt.state !== 'succeeded' && attempt.state !== 'cancelled';
    if (attempt.state === 'succeeded') {
      showLoginFinishing();
      const settling = new Promise((resolve) => window.setTimeout(resolve, 450));
      const refresh = loadAccounts().then(() => !state.renderError).catch(() => false);
      await settling;
      closeLoginDialog(true);
      state.pending.delete(accountId);
      render();
      const refreshed = await Promise.race([refresh, new Promise((resolve) => window.setTimeout(() => resolve(null), 2500))]);
      announce(refreshed === true ? terminalMessage : `${terminalMessage}. Usage will refresh shortly.`, refreshed === false);
      return;
    }
    closeLoginDialog(true);
    try {
      await loadAccounts();
      announce(terminalMessage, terminalIsError);
    } catch (_) {
      render();
      announce(`${terminalMessage}. Reopen FLOW to refresh the account list.`, true);
    } finally {
      state.pending.delete(accountId);
      render();
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
      if (current.finished) return;
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

  function openRemoveDialog(account, trigger) {
    closeAccountMenus();
    state.removeRequest = { account, trigger };
    $('#remove-account-copy').textContent = `Remove ${accountDisplayName(account)}? Its local account home will be archived for recovery.`;
    removeDialog.showModal();
    removeDialog.focus();
  }

  function closeRemoveDialog(restoreFocus = true) {
    const trigger = state.removeRequest?.trigger;
    state.removeRequest = null;
    if (removeDialog.open) removeDialog.close();
    if (restoreFocus && trigger?.isConnected) trigger.focus();
  }

  async function confirmRemoveAccount() {
    const request = state.removeRequest;
    if (!request) return;
    const { account, trigger } = request;
    closeRemoveDialog(false);
    try {
      await api(`/v1/accounts/${encodeURIComponent(account.id)}`, { method: 'DELETE' });
      announce('Account removed. Its local data was archived for recovery.');
      try { await loadAccounts(); }
      catch (_) { render(); announce('Account removed. The account list could not refresh; reopen FLOW.', true); }
      if (!trigger?.isConnected) document.querySelector('[data-connect]')?.focus();
    } catch (error) {
      render();
      if (trigger?.isConnected) trigger.focus();
      announce(requestFailureMessage(error, 'This account could not be removed. Try again.'), true);
    }
  }

  async function addOrConnect(popup = null) {
    if (state.addInFlight) {
      if (popup && !popup.closed) popup.close();
      announce('Finish adding the current account first.', true);
      return;
    }
    state.addInFlight = true;
    render();
    try {
      state.addKey ||= requestKey();
      const result = await api('/v1/accounts', { method: 'POST', body: JSON.stringify({ label: 'OpenAI account', idempotencyKey: state.addKey }) });
      let reconciled = false;
      try { await loadAccounts(); reconciled = true; }
      catch (_) { render(); }
      const loginStarted = await connectAccount(result.account.id, popup);
      if (reconciled || loginStarted) state.addKey = '';
    } catch (error) {
      if (popup && !popup.closed) popup.close();
      announce(requestFailureMessage(error, 'A new account could not be added. Try again.'), true);
    } finally {
      state.addInFlight = false;
      render();
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
    const batchKey = requestKey(); const items = []; const seen = new Set();
    for (let index = 0; index < files.length; index += 1) {
      const file = files[index]; const parsed = canonicalExport(JSON.parse(await file.text())); const target = disconnected[index];
      if (seen.has(parsed.sourceAccountId)) throw new Error('This import contains the same ChatGPT account more than once. Choose one export per account.');
      seen.add(parsed.sourceAccountId);
      items.push({ ...parsed, file, targetId: target?.id || '', targetLabel: target?.label || 'Imported OpenAI account', createKey: `codex-lb-${batchKey}-${index}` });
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
    accountsNode.setAttribute('aria-busy', 'false');
    accountsNode.replaceChildren();
    const empty = document.createElement('p'); empty.className = 'empty offline-state';
    const heading = document.createElement('strong'); heading.textContent = 'Router unavailable';
    const copy = document.createElement('span'); copy.textContent = 'Start FLOW, then refresh this page.';
    empty.append(heading, copy); accountsNode.append(empty);
    announce('Couldn’t reach the router. Start FLOW, then refresh this page.', true);
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
  document.addEventListener('click', (event) => { if (!event.target.closest('.account-menu')) closeAccountMenus(); });
  $('#open-settings').addEventListener('click', () => { settingsDialog.showModal(); settingsDialog.focus(); });
  $('#open-stats').addEventListener('click', () => openStats(null));
  $('#close-settings').addEventListener('click', () => settingsDialog.close());
  $('#close-stats').addEventListener('click', () => { state.statsRequest += 1; statsDialog.close(); });
  statsDialog.addEventListener('cancel', (event) => { event.preventDefault(); state.statsRequest += 1; statsDialog.close(); });
  accountSort.value = state.sort;
  accountSort.addEventListener('change', () => {
    state.sort = accountSort.value;
    writePreference('account-sort', state.sort);
    render();
  });
  showSparkUsage.checked = state.showSparkUsage;
  showSparkUsage.addEventListener('change', () => {
    state.showSparkUsage = showSparkUsage.checked;
    writePreference('show-spark-usage', String(state.showSparkUsage));
    render();
    announce(state.showSparkUsage ? 'Spark usage shown.' : 'Spark usage hidden.', false, true);
  });
  hideAccountEmails.checked = state.hideAccountEmails;
  hideAccountEmails.addEventListener('change', () => {
    state.hideAccountEmails = hideAccountEmails.checked;
    writePreference('hide-account-emails', String(state.hideAccountEmails));
    render();
    announce(state.hideAccountEmails ? 'Email addresses hidden.' : 'Email addresses shown.', false, true);
  });
  $('#cancel-login').addEventListener('click', cancelActiveLogin);
  loginDialog.addEventListener('cancel', (event) => { event.preventDefault(); cancelActiveLogin(); });
  $('#close-remove-account').addEventListener('click', () => closeRemoveDialog());
  $('#cancel-remove-account').addEventListener('click', () => closeRemoveDialog());
  $('#confirm-remove-account').addEventListener('click', confirmRemoveAccount);
  removeDialog.addEventListener('cancel', (event) => { event.preventDefault(); closeRemoveDialog(); });
  $('#migrate-codex-lb').addEventListener('click', migrateCodexLB);
  $('#codex-lb-files').addEventListener('change', () => {
    state.migrationReview = null; $('#migration-preview').hidden = true; $('#migrate-codex-lb').textContent = 'Review import'; announce('', false, true);
  });
  start();
})();
