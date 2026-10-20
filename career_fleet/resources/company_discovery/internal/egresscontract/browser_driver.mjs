// Primitive Playwright transport for one printed Process. Node-authored page
// programs and selectors arrive as explicit calls; this driver chooses no
// search source, query, extraction mapping, pagination or retry policy.
// The entire driver and Chromium descendant run under OS network denial.
// Every routed real request is fulfilled with bytes from the Go scoped broker.
import readline from 'node:readline';
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);
const contexts = new Map();
const openingContexts = new Set();
const networkWaiters = new Map();
let browser;
let initialized = false;
let netSequence = 0;
const send = value => process.stdout.write(JSON.stringify(value) + '\n');
const errorResult = (id, code) => send({ type: 'result', id, error: code });
const ref = state => ({ context_id: state.id, generation: state.generation, url: state.page.url() });
const normalizedURL = raw => {
  const url = new URL(raw); url.hash = ''; url.searchParams.sort(); return url.href;
};

async function finish() {
  for (const waiter of networkWaiters.values()) waiter.reject(new Error('session_lost'));
  networkWaiters.clear();
  if (browser) await browser.close().catch(() => {});
  contexts.clear();
  openingContexts.clear();
}

async function initialize(message) {
  if (initialized) throw new Error('already_initialized');
  const { chromium } = require(message.playwright_directory);
  const version = require(message.playwright_directory + '/package.json').version;
  if (version !== message.playwright_version) throw new Error('runtime_mismatch');
  browser = await chromium.launch({ executablePath: message.browser_executable, headless: true,
    args: ['--disable-background-networking', '--disable-component-update', '--disable-quic', '--disable-sync', '--force-webrtc-ip-handling-policy=disable_non_proxied_udp'] });
  if (browser.version() !== message.browser_version) { await browser.close(); throw new Error('runtime_mismatch'); }
  initialized = true;
  send({ type: 'initialized', browser_version: browser.version(), playwright_version: version });
}

async function routed(state, route, scope) {
  if (!scope) { await route.abort('blockedbyclient').catch(() => {}); return; }
  const request = route.request();
  const id = ++netSequence;
  let redirects = 0;
  for (let previous = request.redirectedFrom(); previous; previous = previous.redirectedFrom()) redirects++;
  const headers = await request.allHeaders();
  const selected = {};
  for (const name of ['accept', 'accept-language', 'cookie']) if (typeof headers[name] === 'string') selected[name] = headers[name];
  const response = new Promise((resolve, reject) => {
    const timer = scope.timeout_ms > 0 ? setTimeout(() => { networkWaiters.delete(id); reject(new Error('request_timeout')); }, scope.timeout_ms) : null;
    networkWaiters.set(id, { resolve: value => { clearTimeout(timer); resolve(value); }, reject: error => { clearTimeout(timer); reject(error); } });
  });
  send({ type: 'request', call_id: scope.call_id, request_id: id, url: request.url(), method: request.method(),
    resource_type: request.resourceType(), redirects, headers: selected });
  try {
    const value = await response;
    if (value.denied) {
      if (value.error === 'request_allowance_exhausted' && scope.abort) scope.abort(value.error);
      await route.abort('blockedbyclient'); return;
    }
    await route.fulfill({ status: value.status, headers: value.headers, body: Buffer.from(value.body_base64, 'base64') });
  } catch { await route.abort('blockedbyclient').catch(() => {}); }
}

async function captureProgram(page, body) {
  // Go verifies the exact program digest before transport. The argument value
  // stays separately serialized; it cannot rewrite the selected program.
  return page.evaluate(({program,args}) => {
    const capture = (0, eval)('(' + program + '\n)');
    if (typeof capture !== 'function') throw new Error('capture_function_required');
    return capture(args);
  }, {program:body.program,args:body.arguments});
}

async function call(message) {
  if (!initialized) throw new Error('runtime_unavailable');
  const body = message.body;
  if (message.method === 'OPEN') {
    const old = contexts.get(body.context_id);
    if (old) {
      if (old.owner !== message.owner || old.intent !== body.intent_digest || old.origin_hash !== message.origin_hash) throw new Error('context_intent_conflict');
      if (message.exclusive) throw new Error('context_busy');
      return { ...ref(old), expires_at: old.expires_at, status: 'opened' };
    }
    if (openingContexts.has(body.context_id)) throw new Error('context_busy');
    if (message.max_contexts > 0 && contexts.size + openingContexts.size >= message.max_contexts) throw new Error('context_bound');
    // Reserve before the first await: input frames execute concurrently.
    openingContexts.add(body.context_id);
    let context;
    try {
      context = await browser.newContext({ serviceWorkers: 'block', acceptDownloads: false });
      const state = { id: body.context_id, owner: message.owner, intent: body.intent_digest, origin_hash: message.origin_hash,
        context, page: null, generation: 0, active: null, uncertain: false, last_status: 0,
        expires_at: message.lease_ms > 0 ? new Date(Date.now() + message.lease_ms).toISOString() : '', lease: null };
      await context.route('**/*', route => {
        const scope = state.active;
        const pending = routed(state, route, scope);
        if (scope) {
          scope.pending.add(pending);
          pending.then(() => scope.pending.delete(pending), () => scope.pending.delete(pending));
        }
        return pending;
      });
      await context.routeWebSocket('**/*', ws => ws.close());
      context.on('page', page => { if (state.page && page !== state.page) page.close().catch(() => {}); });
      const page = await context.newPage(); state.page = page;
      page.on('dialog', dialog => dialog.dismiss().catch(() => {}));
      page.on('response', response => { try { const request = response.request(); if (request.isNavigationRequest() && request.frame() === page.mainFrame()) state.last_status = response.status(); } catch {} });
      if (message.lease_ms > 0) {
        state.lease = setTimeout(async () => { contexts.delete(state.id); await context.close().catch(() => {}); }, message.lease_ms);
        state.lease.unref();
      } contexts.set(state.id, state);
      return { ...ref(state), expires_at: state.expires_at, status: 'opened' };
    } catch (error) {
      if (context) await context.close().catch(() => {});
      throw error;
    } finally {
      openingContexts.delete(body.context_id);
    }
  }
  if (message.method === 'READ') {
    // One anonymous document read owns its entire ephemeral context. The exact
    // Node-pinned program decides readiness/extraction; there is no idle gap
    // between navigation and capture that would silently block later XHRs.
    await call({ ...message, method: 'OPEN', exclusive: true, body: { context_id: body.context_id, intent_digest: body.intent_digest } });
    const state = contexts.get(body.context_id);
    if (state.active) throw new Error('context_busy');
    const page = state.page;
    const scope = { call_id: message.id, timeout_ms: message.timeout_ms, pending: new Set(), failure: '' };
    const refused = new Promise((_, reject) => {
      scope.abort = code => { scope.failure = code; reject(new Error(code)); };
    });
    state.active = scope;
    page.setDefaultTimeout(message.timeout_ms); page.setDefaultNavigationTimeout(message.timeout_ms);
    try {
      state.generation++;
      const reading = (async () => {
        await page.goto(body.url, { waitUntil: 'domcontentloaded', timeout: message.timeout_ms });
        if (state.last_status < 200 || state.last_status >= 300) throw new Error('document_http_failure');
        return captureProgram(page, body);
      })();
      const payload = await Promise.race([reading, refused]);
      return { ...ref(state), status: 'read', status_code: state.last_status,
        captured_at: new Date().toISOString(), payload };
    } finally {
      const scope = state.active;
      state.active = null;
      await Promise.allSettled([...scope.pending]);
      clearTimeout(state.lease);
      await state.context.close().catch(() => {});
      contexts.delete(state.id);
      if (scope.failure) throw new Error(scope.failure);
    }
  }
  const state = contexts.get(body.context_id);
  if (!state || state.owner !== message.owner || state.origin_hash !== message.origin_hash) throw new Error('session_lost');
  if (message.method === 'CLOSE') {
    if (state.active) throw new Error('context_busy');
    clearTimeout(state.lease); await state.context.close(); contexts.delete(state.id);
    return { context_id: state.id, generation: state.generation, status: 'closed' };
  }
  if (state.uncertain || state.generation !== body.generation) throw new Error('stale_page');
  if (state.active) throw new Error('context_busy');
  if (state.generation >= Number.MAX_SAFE_INTEGER && ['NAVIGATE','SUBMIT','ADVANCE'].includes(message.method)) throw new Error('driver_numeric_representation');
  state.active = { call_id: message.id, timeout_ms: message.timeout_ms, pending: new Set() };
  const page = state.page; page.setDefaultTimeout(message.timeout_ms); page.setDefaultNavigationTimeout(message.timeout_ms);
  try {
    if (message.method === 'NAVIGATE') {
      state.generation++; state.uncertain = true;
      await page.goto(body.url, { waitUntil: 'domcontentloaded', timeout: message.timeout_ms });
      state.uncertain = false;
      return { ...ref(state), status: 'navigated', status_code: state.last_status };
    }
    if (message.method === 'SUBMIT') {
      let stage = 'submission_validate';
      try {
      const submit = page.locator(body.selector);
      if (await submit.count() !== 1) throw new Error('control_cardinality');
      const formScope = await submit.evaluate((element, expected) => {
        const form = element.form;
        if (!form || form.method.toLowerCase() !== 'get' || element.disabled || element.formMethod && element.formMethod.toLowerCase() !== 'get') return false;
        const action = new URL(element.getAttribute('formaction') || form.action, document.baseURI);
        const target = new URL(expected);
        return action.origin === target.origin && action.pathname === target.pathname && !action.hash && (!element.formTarget || element.formTarget === '_self') && (!form.target || form.target === '_self');
      }, body.expected_url);
      if (!formScope) throw new Error('submission_scope');
      for (const value of body.values) {
        const locator = page.locator(value.selector);
        if (await locator.count() !== 1) throw new Error('control_cardinality');
        if (!(await locator.evaluate((element, selector) => element.form === document.querySelector(selector).form && !element.disabled, body.selector))) throw new Error('submission_scope');
        if (value.kind === 'text') {
          const tag = await locator.evaluate(element => element.tagName.toLowerCase());
          const type = (await locator.getAttribute('type') || 'text').toLowerCase();
          if (!(tag === 'textarea' || tag === 'input' && ['text', 'search'].includes(type))) throw new Error('control_type');
        } else if (value.kind === 'select') {
          if (await locator.evaluate(element => element.tagName.toLowerCase()) !== 'select') throw new Error('control_type');
        } else throw new Error('control_type');
      }
      for (const value of body.values) {
        stage = 'submission_fill';
        if (value.kind === 'text') await page.locator(value.selector).fill(value.value);
        else await page.locator(value.selector).selectOption(value.value);
      }
      stage = 'submission_form_data';
      const prospective = await submit.evaluate(element => {
        const url = new URL(element.getAttribute('formaction') || element.form.action, document.baseURI);
        const entries = new FormData(element.form, element);
        if ([...entries.values()].some(value => typeof value !== 'string')) return '';
        url.search = new URLSearchParams(entries).toString(); return url.href;
      });
      if (!prospective || normalizedURL(prospective) !== normalizedURL(body.expected_url)) throw new Error('submission_scope');
      state.generation++; state.uncertain = true;
      stage = 'submission_navigation';
      await Promise.all([page.waitForURL(url => normalizedURL(url.href) === normalizedURL(body.expected_url), { waitUntil: 'domcontentloaded', timeout: message.timeout_ms }), submit.click({ timeout: message.timeout_ms })]);
      state.uncertain = false;
      return { ...ref(state), status: 'submitted', status_code: state.last_status };
      } catch (error) {
        if (['control_cardinality','control_type','submission_scope'].includes(error.message)) throw error;
        throw new Error(stage);
      }
    }
    if (message.method === 'ADVANCE') {
      let locator = page.locator(body.selector);
      if (body.text_exact) locator = locator.filter({ hasText: new RegExp('^\\s*' + body.text_exact.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '\\s*$') });
      if (await locator.count() !== 1) throw new Error('control_cardinality');
      const target = await locator.evaluate(element => element.tagName.toLowerCase() === 'a' && element.href && (!element.target || element.target === '_self') ? element.href : '');
      if (!target || normalizedURL(target) !== normalizedURL(body.expected_url) || normalizedURL(target) === normalizedURL(page.url())) throw new Error('continuation_scope');
      state.generation++; state.uncertain = true;
      await Promise.all([page.waitForURL(url => normalizedURL(url.href) === normalizedURL(body.expected_url), { waitUntil: 'domcontentloaded', timeout: message.timeout_ms }), locator.click({ timeout: message.timeout_ms })]);
      state.uncertain = false;
      return { ...ref(state), status: 'advanced', status_code: state.last_status };
    }
    if (message.method === 'CAPTURE') {
      // The Go side verifies this program's exact policy-pinned SHA-256.
      // Playwright treats a string as an expression. Explicitly invoke the
      // Node-owned function with its separately serialized arguments.
      const payload = await captureProgram(page, body);
      return { ...ref(state), status: 'captured', status_code: state.last_status,
        captured_at: new Date().toISOString(), payload };
    }
    throw new Error('unsupported_primitive');
  } finally {
    const scope = state.active;
    state.active = null;
    await Promise.allSettled([...scope.pending]);
  }
}

const input = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
input.on('line', line => {
  let message;
  try { message = JSON.parse(line); } catch { errorResult(0, 'invalid_frame'); return; }
  if (message.type === 'network_response') {
    const waiter = networkWaiters.get(message.request_id);
    if (waiter) { networkWaiters.delete(message.request_id); waiter.resolve(message); }
  } else if (message.type === 'initialize') {
    initialize(message).catch(error => {
      console.error('browser initialization failed:', error.stack || error.message);
      errorResult(0, 'runtime_unavailable');
    });
  } else if (message.type === 'call') {
    call(message).then(result => send({ type: 'result', id: message.id, result }),
      error => errorResult(message.id, ['session_lost','stale_page','context_busy','context_intent_conflict','context_bound','driver_numeric_representation','control_cardinality','control_type','submission_scope','submission_validate','submission_fill','submission_form_data','submission_navigation','continuation_scope','unsupported_primitive','request_allowance_exhausted','document_http_failure'].includes(error.message) ? error.message : 'browser_operation_failed'));
  } else errorResult(0, 'invalid_frame');
});
input.on('close', () => { finish().finally(() => process.exit(0)); });
process.on('SIGTERM', () => { finish().finally(() => process.exit(0)); });
