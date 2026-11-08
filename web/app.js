(() => {
  "use strict";

  const params = new URLSearchParams(window.location.search);
  const demoEnabled = params.get("demo") === "1";
  const TOKEN_KEY = "t2o.token";
  const POLL_MS = 2000;
  const RETRY_MS = 10000;

  // Demo fixture. Delete this object and every branch guarded by demo.enabled
  // to remove demo mode. It never touches the network.
  const demo = {
    enabled: demoEnabled,
    token: "t2o_demo_6f4a2c91",
    state: null,
    reset() {
      this.token = "t2o_demo_6f4a2c91";
      this.state = this.create();
    },
    create() {
      const now = Date.now();
      const ago = (seconds) => new Date(now - seconds * 1000).toISOString();
      return {
        version: "0.1.0",
        config: {
          bind: "127.0.0.1",
          port: 8787,
          server: "https://server.traycer.ai",
          accountsFile: "./accounts.json",
          defaultModel: "gpt-6-astra",
          uptimeSeconds: 4982
        },
        activeId: "a1b2",
        accounts: [
          {
            id: "a1b2", name: "work", email: "me@x.dev", plan: "PRO",
            status: "ok", lastError: "", expiresInSeconds: 812,
            addedAt: "2026-01-01T00:00:00Z", active: true
          },
          {
            id: "c3d4", name: "personal", email: "me@home.dev", plan: "FREE",
            status: "expired", lastError: "", expiresInSeconds: 0,
            addedAt: "2026-01-02T00:00:00Z", active: false
          },
          {
            id: "e5f6", name: "lab", email: "lab@x.dev", plan: "TEAM",
            status: "error", lastError: "needs reauth", expiresInSeconds: 0,
            addedAt: "2026-01-03T00:00:00Z", active: false
          }
        ],
        models: [
          {
            id: "gpt-6-astra", name: "GPT-6 Astra", provider: "openai",
            contextWindow: 1050000, maxOutputTokens: 128000, supportsImages: true,
            efforts: ["low", "medium", "high", "xhigh", "max"]
          },
          {
            id: "gpt-6.1-sol", name: "GPT-6.1 Sol", provider: "openai",
            contextWindow: 400000, maxOutputTokens: 128000, supportsImages: true,
            efforts: ["low", "medium", "high"]
          },
          {
            id: "claude-fable-5", name: "Claude Fable 5", provider: "anthropic",
            contextWindow: 200000, maxOutputTokens: 64000, supportsImages: true,
            efforts: ["low", "medium", "high"]
          },
          {
            id: "claude-opus-5-5", name: "Claude Opus 5.5", provider: "anthropic",
            contextWindow: 200000, maxOutputTokens: 64000, supportsImages: true,
            efforts: ["low", "medium", "high", "max"]
          },
          {
            id: "grok-4.7", name: "Grok 4.7", provider: "xai",
            contextWindow: 256000, maxOutputTokens: 32000, supportsImages: false,
            efforts: ["low", "medium", "high"]
          },
          {
            id: "gemini-4-argon", name: "Gemini 4 Argon", provider: "gemini",
            contextWindow: 1000000, maxOutputTokens: 64000, supportsImages: true,
            efforts: ["low", "medium", "high"]
          },
          {
            id: "kimi-k3", name: "Kimi K3", provider: "openrouter",
            contextWindow: 262144, maxOutputTokens: 32000, supportsImages: false,
            efforts: ["low", "high"]
          },
          {
            id: "deepseek-v4-pro", name: "DeepSeek V4 Pro", provider: "openrouter",
            contextWindow: 131072, maxOutputTokens: 16000, supportsImages: false,
            efforts: ["low", "medium", "high"]
          }
        ],
        requests: [
          { ts: ago(3), account: "work", model: "gpt-6-astra", status: 200, ms: 1234, stream: true, inTokens: 120, outTokens: 480, error: "" },
          { ts: ago(21), account: "personal", model: "claude-fable-5", status: 200, ms: 890, stream: true, inTokens: 40, outTokens: 160, error: "" },
          { ts: ago(52), account: "work", model: "gpt-6.1-sol", status: 402, ms: 312, stream: false, inTokens: 0, outTokens: 0, error: "OUT_OF_CREDITS" },
          { ts: ago(88), account: "lab", model: "grok-4.7", status: 200, ms: 2210, stream: false, inTokens: 300, outTokens: 900, error: "" },
          { ts: ago(140), account: "work", model: "gpt-6-astra", status: 200, ms: 640, stream: true, inTokens: 80, outTokens: 210, error: "" },
          { ts: ago(225), account: "personal", model: "gemini-4-argon", status: 200, ms: 1102, stream: false, inTokens: 12, outTokens: 340, error: "" },
          { ts: ago(310), account: "work", model: "gpt-6-astra", status: 500, ms: 87, stream: false, inTokens: 0, outTokens: 0, error: "upstream unavailable" },
          { ts: ago(402), account: "lab", model: "kimi-k3", status: 200, ms: 4600, stream: true, inTokens: 900, outTokens: 2100, error: "" },
          { ts: ago(515), account: "work", model: "claude-fable-5", status: 200, ms: 980, stream: false, inTokens: 55, outTokens: 180, error: "" },
          { ts: ago(690), account: "personal", model: "deepseek-v4-pro", status: 200, ms: 770, stream: false, inTokens: 22, outTokens: 110, error: "" },
          { ts: ago(840), account: "work", model: "gpt-6-astra", status: 200, ms: 1500, stream: true, inTokens: 150, outTokens: 600, error: "" },
          { ts: ago(1024), account: "personal", model: "claude-opus-5-5", status: 200, ms: 2040, stream: false, inTokens: 66, outTokens: 320, error: "" }
        ]
      };
    },
    setActive(id) {
      const st = this.state;
      st.activeId = id;
      st.accounts.forEach((a) => { a.active = a.id === id; });
    },
    remove(id) {
      const st = this.state;
      st.accounts = st.accounts.filter((a) => a.id !== id);
      if (st.activeId === id) {
        const next = st.accounts.find((a) => a.status === "ok") || st.accounts[0];
        st.activeId = next ? next.id : "";
      }
      st.accounts.forEach((a) => { a.active = a.id === st.activeId; });
    },
    add(name) {
      const st = this.state;
      const id = `demo-${st.accounts.length + 1}-${Math.random().toString(16).slice(2, 6)}`;
      const account = {
        id, name, email: "", plan: "", status: "ok", lastError: "",
        expiresInSeconds: 900, addedAt: new Date().toISOString(), active: false
      };
      st.accounts.push(account);
      if (!st.activeId) {
        st.activeId = id;
        account.active = true;
      }
      return id;
    },
    refresh(id) {
      const account = this.state.accounts.find((a) => a.id === id);
      if (!account) return;
      account.status = "ok";
      account.lastError = "";
      account.expiresInSeconds = 900;
    }
  };
  if (demo.enabled) demo.reset();

  const $ = (id) => document.getElementById(id);
  const els = {
    auth: $("auth"),
    app: $("app"),
    loginForm: $("login-form"),
    loginToken: $("login-token"),
    loginError: $("login-error"),
    loginSubmit: $("login-submit"),
    metaVersion: $("meta-version"),
    metaUpstream: $("meta-upstream"),
    signout: $("signout"),
    conn: $("conn"),
    baseUrl: $("base-url"),
    apiKey: $("api-key"),
    copyKey: $("copy-api-key"),
    accountsCount: $("accounts-count"),
    accountsError: $("accounts-error"),
    accountsList: $("accounts-list"),
    addDetails: $("add-details"),
    addForm: $("add-form"),
    addName: $("add-name"),
    addAccess: $("add-access"),
    addRefresh: $("add-refresh"),
    addCredPath: $("add-credpath"),
    addError: $("add-error"),
    addSubmit: $("add-submit"),
    modelFilter: $("model-filter"),
    modelsList: $("models-list"),
    modelsEmpty: $("models-empty"),
    requestsCount: $("requests-count"),
    requestsList: $("requests-list"),
    requestsEmpty: $("requests-empty"),
    endpointAddr: $("endpoint-addr"),
    uptime: $("uptime"),
    copyLive: $("copy-live"),
    deviceForm: $("device-form"),
    deviceName: $("device-name"),
    deviceStart: $("device-start"),
    devicePanel: $("device-panel"),
    devicePending: $("device-pending"),
    deviceCode: $("device-code"),
    deviceCopy: $("device-copy"),
    deviceLink: $("device-link"),
    deviceWaitText: $("device-wait-text"),
    deviceCount: $("device-count"),
    deviceFailed: $("device-failed"),
    deviceFailedText: $("device-failed-text"),
    deviceRetry: $("device-retry"),
    deviceCancel: $("device-cancel"),
    deviceError: $("device-error")
  };

  const origin = window.location.origin && window.location.origin !== "null"
    ? window.location.origin
    : "http://127.0.0.1:8787";
  const apiBase = origin.replace(/\/+$/, "") + "/v1";

  let state = null;
  let signedIn = false;
  let token = "";
  let pollTimer = 0;
  let polling = false;
  let failures = 0;
  let activeSeq = 0;
  let addPending = false;
  let copyTimer = 0;
  let loginBusy = false;
  const DEVICE_POLL_MS = 2000;
  const DEVICE_HIDDEN_LIMIT_MS = 30000;
  const device = {
    active: false,
    starting: false,
    attempt: "",
    label: "",
    expiresAt: 0,
    pollTimer: 0,
    countTimer: 0,
    hiddenAt: 0,
    copyTimer: 0,
    demoSeq: 0,
    demoThis: 0,
    startedAt: 0
  };
  const keys = { meta: "", accounts: "", models: "", requests: "" };
  const copyButtons = Array.from(document.querySelectorAll("[data-copy]"));

  function el(tag, className, textContent) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (textContent !== undefined) node.textContent = textContent;
    return node;
  }

  function srOnly(text) {
    return el("span", "sr-only", text);
  }

  function button(text, className) {
    const node = el("button", className, text);
    node.type = "button";
    return node;
  }

  function setLine(node, message) {
    node.textContent = message || "";
    node.hidden = !message;
  }

  function showAccountsError(message) {
    setLine(els.accountsError, message);
  }

  function clearAccountsError() {
    setLine(els.accountsError, "");
  }

  function showAddError(message) {
    setLine(els.addError, message);
  }

  function formatContext(value) {
    const n = Number(value);
    if (!Number.isFinite(n) || n <= 0) return "";
    if (n >= 1000000) {
      const m = n / 1000000;
      return `${m >= 10 ? Math.round(m) : Number(m.toFixed(2))}M`;
    }
    const k = n / 1000;
    return `${k >= 100 ? Math.round(k) : Number(k.toFixed(1))}K`;
  }

  function formatUptime(value) {
    const total = Math.max(0, Math.floor(Number(value) || 0));
    if (total < 60) return `${total}s`;
    const minutes = Math.floor(total / 60);
    if (minutes < 60) return `${minutes}m`;
    const hours = Math.floor(minutes / 60);
    if (hours < 24) return `${hours}h ${minutes % 60}m`;
    return `${Math.floor(hours / 24)}d ${hours % 24}h`;
  }

  function formatTime(value) {
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return "--:--:--";
    return [date.getHours(), date.getMinutes(), date.getSeconds()]
      .map((n) => String(n).padStart(2, "0"))
      .join(":");
  }

  function hostname(value) {
    if (!value) return "";
    try {
      return new URL(String(value)).host;
    } catch (_) {
      return String(value).replace(/^[a-z]+:\/\//i, "").replace(/\/.*$/, "");
    }
  }

  function saveToken(value) {
    try { window.sessionStorage.setItem(TOKEN_KEY, value); } catch (_) { /* no storage */ }
  }

  function readToken() {
    try { return window.sessionStorage.getItem(TOKEN_KEY) || ""; } catch (_) { return ""; }
  }

  function forgetToken() {
    try { window.sessionStorage.removeItem(TOKEN_KEY); } catch (_) { /* no storage */ }
  }

  async function requestJSON(path, options = {}) {
    const headers = Object.assign({ accept: "application/json" }, options.headers || {});
    const res = await fetch(path, Object.assign({ cache: "no-store" }, options, { headers }));
    let data = null;
    try { data = await res.json(); } catch (_) { /* no body */ }
    if (res.status === 401) {
      const error = new Error("Session expired.");
      error.signedOut = true;
      throw error;
    }
    if (!res.ok) {
      const message = data && typeof data.error === "string" && data.error
        ? data.error
        : `Request failed (${res.status}).`;
      throw new Error(message);
    }
    return data;
  }

  async function writeClipboard(value) {
    if (navigator.clipboard && window.isSecureContext) {
      try {
        await navigator.clipboard.writeText(value);
        return true;
      } catch (_) { /* fall through */ }
    }
    const previous = document.activeElement;
    let area = null;
    try {
      area = document.createElement("textarea");
      area.value = value;
      area.setAttribute("readonly", "");
      area.style.position = "fixed";
      area.style.top = "0";
      area.style.left = "-9999px";
      document.body.appendChild(area);
      area.select();
      return document.execCommand("copy");
    } catch (_) {
      return false;
    } finally {
      if (area) area.remove();
      if (previous && previous.isConnected && typeof previous.focus === "function") {
        previous.focus({ preventScroll: true });
      }
    }
  }

  function resetCopyButtons() {
    copyButtons.forEach((node) => {
      node.textContent = "Copy";
      node.setAttribute("aria-label", node.dataset.copy === "api-key" ? "Copy API key" : "Copy Base URL");
    });
    els.copyLive.textContent = "";
  }

  async function handleCopy(btn) {
    const isKey = btn.dataset.copy === "api-key";
    const value = isKey ? token : apiBase;
    if (!value) return;
    const name = isKey ? "API key" : "Base URL";
    window.clearTimeout(copyTimer);
    resetCopyButtons();
    const ok = await writeClipboard(value);
    if (!ok) {
      btn.textContent = "Copy failed";
      btn.setAttribute("aria-label", `${name} copy failed`);
      els.copyLive.textContent = `Could not copy the ${name}.`;
    } else {
      btn.textContent = "Copied";
      btn.setAttribute("aria-label", `${name} copied`);
      els.copyLive.textContent = `${name} copied.`;
    }
    copyTimer = window.setTimeout(resetCopyButtons, 1200);
  }

  function renderEndpoint() {
    els.baseUrl.textContent = apiBase;
    els.baseUrl.title = apiBase;
    // API_AUTH=false: the server ignores Authorization, so showing a key would
    // imply a check that no longer happens.
    const authOn = !state || !state.config || state.config.apiAuth !== false;
    if (!authOn) {
      els.apiKey.textContent = "Not required";
      els.apiKey.title = "API_AUTH=false: any key works, or none";
      els.apiKey.classList.add("is-muted");
      els.copyKey.disabled = true;
      return;
    }
    const available = Boolean(token);
    els.apiKey.textContent = available ? token : "not saved in this tab";
    els.apiKey.title = available ? token : "";
    els.apiKey.classList.toggle("is-muted", !available);
    els.copyKey.disabled = !available;
  }

  function renderMeta() {
    if (!state) return;
    const cfg = state.config || {};
    const metaKey = JSON.stringify([state.version || "", cfg.bind || "", cfg.port || "", cfg.server || ""]);
    if (metaKey !== keys.meta) {
      keys.meta = metaKey;
      els.metaVersion.textContent = state.version ? String(state.version) : "";
      els.metaUpstream.textContent = hostname(cfg.server);
      document.title = state.version ? `traycer2openai ${state.version}` : "traycer2openai";
    }
    els.endpointAddr.textContent = cfg.bind && cfg.port ? `${cfg.bind}:${cfg.port}` : "";
    els.uptime.textContent = cfg.uptimeSeconds != null && Number.isFinite(Number(cfg.uptimeSeconds))
      ? `up ${formatUptime(cfg.uptimeSeconds)}`
      : "";
  }

  function accountView(account) {
    const a = account && typeof account === "object" ? account : {};
    const id = String(a.id == null ? "" : a.id);
    const name = a.name ? String(a.name) : "account";
    const email = a.email ? String(a.email) : "";
    const plan = a.plan ? String(a.plan) : "";
    const activeId = state && state.activeId != null ? String(state.activeId) : "";
    const active = activeId ? id === activeId : Boolean(a.active);
    const rawStatus = a.status ? String(a.status) : "ok";
    const expires = a.expiresInSeconds == null ? NaN : Number(a.expiresInSeconds);
    let status = "ok";
    let bad = false;
    if (rawStatus === "error") {
      status = a.lastError ? `error: ${a.lastError}` : "error";
      bad = true;
    } else if (rawStatus === "expired" || (Number.isFinite(expires) && expires <= 0)) {
      status = "expired";
      bad = true;
    } else if (Number.isFinite(expires) && expires > 0 && expires < 1200) {
      status = `expires in ${Math.max(1, Math.floor(expires / 60))}m`;
    } else if (rawStatus !== "ok") {
      status = rawStatus;
    }
    return {
      id,
      name,
      active,
      status,
      bad,
      meta: [email, plan].filter(Boolean).join(" · ")
    };
  }

  function captureFocus() {
    const node = document.activeElement;
    if (!node || !els.accountsList.contains(node)) return "";
    return node.dataset && node.dataset.focus ? node.dataset.focus : "";
  }

  function restoreFocus(focusId) {
    if (!focusId) return;
    const target = Array.from(els.accountsList.querySelectorAll("[data-focus]"))
      .find((node) => node.dataset.focus === focusId);
    if (target) target.focus({ preventScroll: true });
  }

  function accountRow(view, index) {
    const row = el("li", "acct");
    row.dataset.id = view.id;

    const radioId = `acct-radio-${index}`;
    const pick = el("label", "acct-pick");
    pick.htmlFor = radioId;
    const radio = el("input", "acct-radio");
    radio.type = "radio";
    radio.name = "active-account";
    radio.id = radioId;
    radio.value = view.id;
    radio.checked = view.active;
    radio.dataset.focus = `${view.id}:radio`;
    radio.addEventListener("change", () => {
      if (radio.checked) selectAccount(view.id);
    });
    pick.appendChild(radio);
    pick.appendChild(srOnly(`Make ${view.name} the active account`));

    const idColumn = el("div", "acct-id");
    idColumn.appendChild(el("span", "acct-name", view.name));
    if (view.meta) idColumn.appendChild(el("span", "acct-meta", view.meta));

    const side = el("div", "acct-side");
    const status = el("span", view.bad ? "acct-status is-bad" : "acct-status", view.status);
    status.title = view.status;
    side.appendChild(status);

    const refresh = button("Refresh", "word-btn");
    refresh.dataset.focus = `${view.id}:refresh`;
    refresh.addEventListener("click", () => {
      if (refresh.dataset.busy === "1") return;
      refreshAccount(view.id, refresh);
    });
    side.appendChild(refresh);

    const remove = button("Remove", "word-btn");
    remove.dataset.focus = `${view.id}:remove`;
    remove.addEventListener("click", () => {
      if (remove.dataset.busy === "1") return;
      removeAccount(view.id, view.name, remove);
    });
    side.appendChild(remove);

    row.appendChild(pick);
    row.appendChild(idColumn);
    row.appendChild(side);
    return row;
  }

  function renderAccounts(force = false) {
    if (!state) return;
    const accounts = Array.isArray(state.accounts) ? state.accounts : [];
    const rows = accounts.map((account) => accountView(account));
    const key = JSON.stringify({
      activeId: state.activeId != null ? String(state.activeId) : "",
      rows
    });
    els.accountsCount.textContent = String(accounts.length);
    if (!force && key === keys.accounts) return;
    keys.accounts = key;

    const focused = captureFocus();
    els.accountsList.textContent = "";
    if (!rows.length) {
      els.accountsList.appendChild(el("li", "empty-line", "No accounts yet."));
    } else {
      rows.forEach((view, index) => {
        els.accountsList.appendChild(accountRow(view, index));
      });
    }
    restoreFocus(focused);
  }

  function applyModelFilter() {
    const query = els.modelFilter.value.trim().toLowerCase();
    let visible = 0;
    Array.from(els.modelsList.children).forEach((row) => {
      const match = !query || (row.dataset.search || "").includes(query);
      row.hidden = !match;
      if (match) visible += 1;
    });
    els.modelsEmpty.textContent = query ? "no matches" : "no models";
    els.modelsEmpty.hidden = visible > 0;
  }

  function renderModels(force = false) {
    if (!state) return;
    const models = Array.isArray(state.models) ? state.models : [];
    const key = JSON.stringify(models);
    if (!force && key === keys.models) return;
    keys.models = key;

    els.modelsList.textContent = "";
    models.forEach((model) => {
      const m = model && typeof model === "object" ? model : {};
      const row = el("li", "model");
      const efforts = Array.isArray(m.efforts) ? m.efforts.map(String) : [];
      row.dataset.search = [m.id, m.name, m.provider, ...efforts]
        .filter(Boolean)
        .join(" ")
        .toLowerCase();

      row.appendChild(el("span", "model-id", m.id ? String(m.id) : ""));

      const metaParts = [];
      if (m.provider) metaParts.push(String(m.provider));
      const context = formatContext(m.contextWindow);
      if (context) metaParts.push(`${context} ctx`);
      row.appendChild(el("span", "model-meta", metaParts.join(" · ")));

      const effortLine = el("span", "model-efforts");
      if (efforts.length) {
        effortLine.appendChild(srOnly("Reasoning efforts: "));
        effortLine.appendChild(document.createTextNode(efforts.join(" · ")));
      }
      row.appendChild(effortLine);
      els.modelsList.appendChild(row);
    });
    applyModelFilter();
  }

  function requestParts(entry) {
    const r = entry && typeof entry === "object" ? entry : {};
    const parts = [];
    parts.push({ cls: "req-time", text: formatTime(r.ts) });
    if (r.account) parts.push({ cls: "req-account", text: String(r.account) });
    if (r.model) parts.push({ cls: "req-model", text: String(r.model) });

    const statusText = r.status == null ? "" : String(r.status);
    const statusNumber = Number(r.status);
    const bad = Number.isFinite(statusNumber) && statusNumber >= 400 && statusNumber < 600;
    const status = {
      cls: bad ? "req-status is-bad" : "req-status",
      text: statusText
    };
    if (r.error) status.title = String(r.error);
    parts.push(status);

    if (r.ms != null && Number.isFinite(Number(r.ms))) {
      parts.push({ cls: "req-ms", text: `${Math.round(Number(r.ms))}ms` });
    }
    if (r.inTokens != null || r.outTokens != null) {
      const inTokens = Number.isFinite(Number(r.inTokens)) ? Math.max(0, Number(r.inTokens)) : 0;
      const outTokens = Number.isFinite(Number(r.outTokens)) ? Math.max(0, Number(r.outTokens)) : 0;
      parts.push({
        cls: "req-tokens",
        text: `${inTokens}/${outTokens}`,
        label: `${inTokens} input tokens, ${outTokens} output tokens`
      });
    }
    if (r.stream) parts.push({ cls: "req-stream", text: "stream" });
    return parts;
  }

  function renderRequests(force = false) {
    if (!state) return;
    const requests = Array.isArray(state.requests) ? state.requests : [];
    const key = JSON.stringify(requests);
    els.requestsCount.textContent = String(requests.length);
    if (!force && key === keys.requests) return;
    keys.requests = key;

    els.requestsList.textContent = "";
    els.requestsList.hidden = requests.length === 0;
    els.requestsEmpty.hidden = requests.length !== 0;
    if (!requests.length) return;

    const sorted = requests.slice().sort((a, b) => {
      const at = new Date(a && a.ts).getTime();
      const bt = new Date(b && b.ts).getTime();
      return (Number.isNaN(bt) ? 0 : bt) - (Number.isNaN(at) ? 0 : at);
    });

    sorted.forEach((entry) => {
      const row = el("li", "req");
      const parts = requestParts(entry);
      parts.forEach((part) => {
        const node = el("span", part.cls, part.text);
        if (part.title) node.title = part.title;
        if (part.label) node.setAttribute("aria-label", part.label);
        row.appendChild(node);
      });
      els.requestsList.appendChild(row);
    });
  }

  function applyState(data) {
    state = data && typeof data === "object" ? data : {};
    if (!state.config || typeof state.config !== "object") state.config = {};
    if (!Array.isArray(state.accounts)) state.accounts = [];
    if (!Array.isArray(state.models)) state.models = [];
    if (!Array.isArray(state.requests)) state.requests = [];
    if (state.activeId == null || state.activeId === "") {
      const active = state.accounts.find((account) => account && account.active);
      state.activeId = active ? String(active.id) : "";
    }
    renderMeta();
    renderAccounts();
    renderModels();
    renderRequests();
  }

  function showSignedIn() {
    if (!state) return;
    signedIn = true;
    token = token || (demo.enabled ? demo.token : "");
    els.auth.hidden = true;
    setLine(els.loginError, "");
    setLine(els.accountsError, "");
    setLine(els.addError, "");
    renderEndpoint();
    renderMeta();
    renderAccounts(true);
    renderModels(true);
    renderRequests(true);
    els.app.hidden = false;
    if (!demo.enabled) startPolling();
  }

  function showSignedOut(message) {
    signedIn = false;
    stopPolling();
    deviceClose();
    state = null;
    keys.meta = "";
    keys.accounts = "";
    keys.models = "";
    keys.requests = "";
    failures = 0;
    els.app.hidden = true;
    els.auth.hidden = false;
    document.title = "traycer2openai";
    setLine(els.conn, "");
    setLine(els.loginError, message);
    setLine(els.accountsError, "");
    setLine(els.addError, "");
    els.addDetails.open = false;
    els.addForm.reset();
    setAddBusy(false);
    els.modelFilter.value = "";
    resetCopyButtons();
    els.apiKey.textContent = "";
    els.apiKey.classList.add("is-muted");
    els.copyKey.disabled = true;
    els.loginSubmit.disabled = false;
    els.loginForm.removeAttribute("aria-busy");
    loginBusy = false;
  }

  function handleAuthFailure(error) {
    if (!error || !error.signedOut) return false;
    if (!signedIn) return true;
    forgetToken();
    token = "";
    showSignedOut("Session expired. Sign in again.");
    return true;
  }

  function schedule(delay) {
    if (!polling) return;
    if (pollTimer) window.clearTimeout(pollTimer);
    pollTimer = window.setTimeout(poll, delay);
  }

  async function poll() {
    pollTimer = 0;
    if (!polling || !signedIn || demo.enabled) return;
    if (document.visibilityState === "hidden") return;
    try {
      const res = await fetch("/api/state", {
        cache: "no-store",
        headers: { accept: "application/json" }
      });
      if (res.status === 401) {
        handleAuthFailure({ signedOut: true });
        return;
      }
      if (!res.ok) throw new Error("State request failed.");
      const data = await res.json();
      if (!polling || !signedIn) return;
      failures = 0;
      setLine(els.conn, "");
      applyState(data);
      schedule(POLL_MS);
    } catch (_) {
      failures += 1;
      if (!polling) return;
      if (failures >= 3) {
        setLine(els.conn, "disconnected");
        schedule(RETRY_MS);
      } else {
        schedule(POLL_MS);
      }
    }
  }

  function startPolling() {
    stopPolling();
    if (demo.enabled) return;
    polling = true;
    failures = 0;
    setLine(els.conn, "");
    schedule(POLL_MS);
  }

  function stopPolling() {
    polling = false;
    if (pollTimer) {
      window.clearTimeout(pollTimer);
      pollTimer = 0;
    }
  }

  async function selectAccount(id) {
    if (!state) return;
    const activeId = state.activeId != null ? String(state.activeId) : "";
    if (activeId === id) return;
    const previous = activeId;
    const seq = ++activeSeq;
    clearAccountsError();
    state.activeId = id;
    state.accounts.forEach((account) => {
      if (account) account.active = String(account.id) === id;
    });
    renderAccounts(true);

    if (demo.enabled) {
      demo.setActive(id);
      renderAccounts(true);
      return;
    }

    try {
      await requestJSON(`/api/accounts/${encodeURIComponent(id)}/active`, { method: "POST" });
    } catch (error) {
      if (seq !== activeSeq) return;
      if (handleAuthFailure(error)) return;
      if (!signedIn || !state) return;
      state.activeId = previous;
      state.accounts.forEach((account) => {
        if (account) account.active = String(account.id) === previous;
      });
      renderAccounts(true);
      showAccountsError(error.message || "Could not change the active account.");
    }
  }

  async function refreshAccount(id, btn) {
    btn.dataset.busy = "1";
    btn.setAttribute("aria-busy", "true");
    clearAccountsError();
    try {
      if (demo.enabled) {
        demo.refresh(id);
        renderAccounts(true);
        return;
      }
      const data = await requestJSON(`/api/accounts/${encodeURIComponent(id)}/refresh`, { method: "POST" });
      if (!signedIn || !state) return;
      const account = state.accounts.find((item) => String(item.id) === id);
      if (account) {
        account.status = "ok";
        account.lastError = "";
        if (data && data.expiresInSeconds != null && Number.isFinite(Number(data.expiresInSeconds))) {
          account.expiresInSeconds = Number(data.expiresInSeconds);
        }
      }
      renderAccounts(true);
    } catch (error) {
      if (handleAuthFailure(error)) return;
      showAccountsError(error.message || "Could not refresh the account.");
    } finally {
      if (btn.isConnected) {
        btn.dataset.busy = "";
        btn.removeAttribute("aria-busy");
      }
    }
  }

  async function removeAccount(id, name, btn) {
    if (!window.confirm(`Remove ${name}?`)) return;
    btn.dataset.busy = "1";
    btn.setAttribute("aria-busy", "true");
    clearAccountsError();
    let removed = false;
    try {
      if (demo.enabled) {
        demo.remove(id);
        removed = true;
        renderAccounts(true);
        return;
      }
      await requestJSON(`/api/accounts/${encodeURIComponent(id)}`, { method: "DELETE" });
      if (!signedIn || !state) return;
      removed = true;
      state.accounts = state.accounts.filter((account) => String(account.id) !== id);
      if (String(state.activeId) === id) {
        const next = state.accounts.find((account) => account && account.status === "ok")
          || state.accounts[0]
          || null;
        state.activeId = next ? String(next.id) : "";
        state.accounts.forEach((account) => {
          if (account) account.active = String(account.id) === state.activeId;
        });
      }
      renderAccounts(true);
    } catch (error) {
      if (handleAuthFailure(error)) return;
      showAccountsError(error.message || "Could not remove the account.");
    } finally {
      if (btn.isConnected) {
        btn.dataset.busy = "";
        btn.removeAttribute("aria-busy");
      }
      if (removed) {
        const target = els.accountsList.querySelector(".acct-radio:checked")
          || els.accountsList.querySelector(".acct-radio")
          || els.addDetails.querySelector("summary");
        if (target) target.focus({ preventScroll: true });
      }
    }
  }

  function setLoginBusy(busy) {
    loginBusy = busy;
    els.loginSubmit.disabled = busy;
    if (busy) {
      els.loginForm.setAttribute("aria-busy", "true");
    } else {
      els.loginForm.removeAttribute("aria-busy");
    }
  }

  async function onLogin(event) {
    event.preventDefault();
    if (loginBusy) return;
    const value = els.loginToken.value.trim();
    if (!value) {
      setLine(els.loginError, "Enter a token.");
      els.loginToken.focus();
      return;
    }
    setLine(els.loginError, "");
    setLoginBusy(true);
    try {
      if (demo.enabled) {
        demo.token = value;
        token = value;
        state = demo.state;
        applyState(state);
        els.loginForm.reset();
        showSignedIn();
        return;
      }

      const res = await fetch("/api/login", {
        method: "POST",
        cache: "no-store",
        headers: { "content-type": "application/json", accept: "application/json" },
        body: JSON.stringify({ token: value })
      });
      let data = null;
      try { data = await res.json(); } catch (_) { /* no body */ }
      if (!res.ok) {
        const message = data && typeof data.error === "string" && data.error
          ? data.error
          : `Sign in failed (${res.status}).`;
        throw new Error(message);
      }

      token = value;
      saveToken(token);
      const next = await requestJSON("/api/state");
      applyState(next);
      els.loginForm.reset();
      showSignedIn();
    } catch (error) {
      setLine(els.loginError, error.message || "Sign in failed.");
    } finally {
      setLoginBusy(false);
    }
  }

  function setAddBusy(busy) {
    addPending = busy;
    els.addSubmit.disabled = busy;
    if (busy) {
      els.addForm.setAttribute("aria-busy", "true");
    } else {
      els.addForm.removeAttribute("aria-busy");
    }
  }

  /* device sign-in (RFC 8628 style flow against /api/auth/device/*) ------ */

  function stopDeviceTimers() {
    if (device.pollTimer) { window.clearTimeout(device.pollTimer); device.pollTimer = 0; }
    if (device.countTimer) { window.clearInterval(device.countTimer); device.countTimer = 0; }
  }

  function setDeviceError(message) {
    setLine(els.deviceError, message);
  }

  function formatCountdown(remainingMs) {
    const total = Math.max(0, Math.ceil(remainingMs / 1000));
    const minutes = Math.floor(total / 60);
    const seconds = total % 60;
    return `Code expires in ${minutes}:${String(seconds).padStart(2, "0")}`;
  }

  function updateDeviceCountdown() {
    const remaining = device.expiresAt - Date.now();
    if (remaining <= 0) {
      stopDeviceTimers();
      device.active = false;
      deviceFail("The code expired before it was approved.");
      return;
    }
    els.deviceCount.textContent = formatCountdown(remaining);
  }

  function scheduleDevicePoll(delay) {
    if (!device.active) return;
    if (device.pollTimer) window.clearTimeout(device.pollTimer);
    device.pollTimer = window.setTimeout(pollDeviceStatus, delay);
  }

  function beginDeviceWait(data) {
    device.active = true;
    device.attempt = String(data.attemptId || "");
    const expires = Number(data.expiresInSeconds);
    device.expiresAt = Date.now() + (Number.isFinite(expires) && expires > 0 ? expires : 600) * 1000;
    device.hiddenAt = 0;
    els.deviceCode.textContent = data.userCode ? String(data.userCode) : "";
    const href = data.verificationUriComplete || data.verificationUri || "";
    if (href) els.deviceLink.setAttribute("href", href);
    els.deviceWaitText.textContent = "Waiting for approval";
    els.devicePending.hidden = false;
    els.deviceFailed.hidden = true;
    setDeviceError("");
    els.devicePanel.hidden = false;
    els.deviceForm.hidden = true;
    updateDeviceCountdown();
    device.countTimer = window.setInterval(updateDeviceCountdown, 500);
    scheduleDevicePoll(DEVICE_POLL_MS);
    els.deviceLink.focus({ preventScroll: true });
  }

  function deviceClose() {
    stopDeviceTimers();
    device.active = false;
    device.attempt = "";
    device.hiddenAt = 0;
    els.devicePanel.hidden = true;
    els.deviceForm.hidden = false;
    setDeviceError("");
  }

  function deviceFail(message) {
    stopDeviceTimers();
    device.active = false;
    els.devicePending.hidden = true;
    els.deviceFailedText.textContent = message || "The sign-in could not be completed.";
    els.deviceFailed.hidden = false;
  }

  async function startDeviceFlow() {
    if (device.starting || device.active) return;
    device.starting = true;
    els.deviceStart.disabled = true;
    els.deviceStart.setAttribute("aria-busy", "true");
    setDeviceError("");
    const label = els.deviceName.value.trim();
    device.label = label;
    try {
      let data;
      if (demo.enabled) {
        device.demoSeq += 1;
        device.demoThis = device.demoSeq;
        device.startedAt = Date.now();
        const userCode = device.demoThis === 1 ? "E4556-XT3CA" : "Q7721-KZ4DW";
        data = {
          attemptId: `demo-attempt-${device.demoThis}`,
          userCode,
          verificationUri: "https://traycer.ai/device",
          verificationUriComplete: `https://traycer.ai/device?user_code=${userCode}`,
          intervalSeconds: 2,
          expiresInSeconds: 600
        };
      } else {
        const res = await fetch("/api/auth/device/start", {
          method: "POST",
          cache: "no-store",
          headers: { "content-type": "application/json", accept: "application/json" },
          body: JSON.stringify({ name: label })
        });
        let body = null;
        try { body = await res.json(); } catch (_) { /* no body */ }
        if (!res.ok) {
          throw new Error(body && typeof body.error === "string" && body.error
            ? body.error
            : `Could not start sign-in (${res.status}).`);
        }
        data = body;
      }
      if (!data || !data.attemptId) throw new Error("The sign-in response was missing the attempt id.");
      beginDeviceWait(data);
    } catch (error) {
      setDeviceError(error.message || "Could not start sign-in.");
    } finally {
      device.starting = false;
      els.deviceStart.disabled = false;
      els.deviceStart.removeAttribute("aria-busy");
    }
  }

  function demoDeviceStatus() {
    const elapsed = Date.now() - device.startedAt;
    if (device.demoThis === 1) {
      if (elapsed < 6000) return { state: "pending" };
      return { state: "authorized", account: { id: "traycer-demo-1", name: device.label || "Traycer" } };
    }
    if (elapsed < 4000) return { state: "pending" };
    return { state: "denied", error: "denied" };
  }

  async function pollDeviceStatus() {
    device.pollTimer = 0;
    if (!device.active) return;
    let payload = null;
    try {
      if (demo.enabled) {
        payload = demoDeviceStatus();
      } else {
        const res = await fetch(`/api/auth/device/status?attempt=${encodeURIComponent(device.attempt)}`, {
          cache: "no-store",
          headers: { accept: "application/json" }
        });
        let body = null;
        try { body = await res.json(); } catch (_) { /* no body */ }
        if (!res.ok) {
          throw new Error(body && typeof body.error === "string" && body.error
            ? body.error
            : `Status request failed (${res.status}).`);
        }
        payload = body;
      }
    } catch (error) {
      if (!device.active) return;
      deviceFail(error.message || "Could not check the sign-in status.");
      return;
    }
    if (!device.active || !payload) return;
    const state = payload.state ? String(payload.state) : "error";
    if (state === "pending") {
      scheduleDevicePoll(DEVICE_POLL_MS);
      return;
    }
    if (state === "authorized") {
      await onDeviceAuthorized(payload.account);
      return;
    }
    if (state === "denied") {
      deviceFail("The sign-in request was denied.");
      return;
    }
    if (state === "expired") {
      deviceFail("The code expired before it was approved.");
      return;
    }
    deviceFail(payload.error ? String(payload.error) : "The sign-in status could not be read.");
  }

  function demoAuthorizeAccount(account) {
    const st = demo.state;
    const a = account && typeof account === "object" ? account : {};
    const id = a.id ? String(a.id) : `traycer-${st.accounts.length + 1}`;
    const name = a.name ? String(a.name) : (device.label || "Traycer account");
    st.accounts = st.accounts.filter((item) => String(item.id) !== id);
    st.accounts.push({
      id, name, email: "you@traycer.ai", plan: "PRO",
      status: "ok", lastError: "", expiresInSeconds: 3600,
      addedAt: new Date().toISOString(), active: true
    });
    st.activeId = id;
    st.accounts.forEach((item) => { item.active = String(item.id) === id; });
  }

  async function onDeviceAuthorized(account) {
    stopDeviceTimers();
    device.active = false;
    deviceClose();
    clearAccountsError();
    try {
      if (demo.enabled) {
        demoAuthorizeAccount(account);
        applyState(demo.state);
        renderAccounts(true);
      } else {
        const next = await requestJSON("/api/state");
        if (!signedIn) return;
        applyState(next);
      }
    } catch (error) {
      if (handleAuthFailure(error)) return;
      showAccountsError(error.message || "Signed in, but the state could not be refreshed.");
    }
  }

  async function cancelDeviceFlow() {
    const attempt = device.attempt;
    deviceClose();
    if (!attempt || demo.enabled) return;
    try {
      await requestJSON("/api/auth/device/cancel", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ attempt })
      });
    } catch (_) { /* cancelled locally anyway */ }
  }

  async function onDeviceCopy() {
    const code = els.deviceCode.textContent;
    if (!code) return;
    const ok = await writeClipboard(code);
    window.clearTimeout(device.copyTimer);
    els.deviceCopy.textContent = ok ? "Copied" : "Copy failed";
    els.copyLive.textContent = ok ? "Code copied." : "Could not copy the code.";
    device.copyTimer = window.setTimeout(() => {
      els.deviceCopy.textContent = "Copy Code";
      els.copyLive.textContent = "";
    }, 1200);
  }

  async function onAdd(event) {
    event.preventDefault();
    if (addPending) return;
    const name = els.addName.value.trim();
    const accessToken = els.addAccess.value.trim();
    const refreshToken = els.addRefresh.value.trim();
    const credentialPath = els.addCredPath.value.trim();
    if (!name || (!accessToken && !credentialPath)) {
      showAddError("Name and either an access token or a credentials file are required.");
      (!name ? els.addName : els.addAccess).focus();
      return;
    }
    showAddError("");
    setAddBusy(true);
    try {
      if (demo.enabled) {
        demo.add(name);
        renderAccounts(true);
      } else {
        await requestJSON("/api/accounts", {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({ name, accessToken, refreshToken, teamId: "", credentialPath })
        });
        const next = await requestJSON("/api/state");
        if (!signedIn) return;
        applyState(next);
      }
      els.addForm.reset();
      clearAccountsError();
      els.addDetails.open = false;
      const summary = els.addDetails.querySelector("summary");
      if (summary) summary.focus({ preventScroll: true });
    } catch (error) {
      if (!handleAuthFailure(error)) {
        showAddError(error.message || "Could not add the account.");
      }
    } finally {
      setAddBusy(false);
    }
  }

  async function onSignOut() {
    if (demo.enabled) {
      token = "";
      showSignedOut();
      els.loginToken.focus();
      return;
    }
    try {
      await fetch("/api/logout", {
        method: "POST",
        cache: "no-store",
        headers: { accept: "application/json" }
      });
    } catch (_) { /* sign out locally anyway */ }
    forgetToken();
    token = "";
    showSignedOut();
    els.loginToken.focus();
  }

  async function init() {
    if (demo.enabled) {
      token = demo.token;
      state = demo.state;
      applyState(state);
      showSignedIn();
      return;
    }

    token = readToken();
    try {
      const res = await fetch("/api/state", {
        cache: "no-store",
        headers: { accept: "application/json" }
      });
      if (res.status === 401) {
        forgetToken();
        token = "";
        showSignedOut();
        return;
      }
      if (!res.ok) {
        showSignedOut("Could not load the dashboard.");
        return;
      }
      const data = await res.json();
      applyState(data);
      showSignedIn();
    } catch (_) {
      showSignedOut("Cannot reach the server.");
    }
  }

  els.loginForm.addEventListener("submit", onLogin);
  els.signout.addEventListener("click", onSignOut);
  els.addForm.addEventListener("submit", onAdd);
  els.modelFilter.addEventListener("input", applyModelFilter);
  copyButtons.forEach((btn) => {
    btn.addEventListener("click", () => handleCopy(btn));
  });
  els.deviceForm.addEventListener("submit", (event) => {
    event.preventDefault();
    startDeviceFlow();
  });
  els.deviceRetry.addEventListener("click", () => { startDeviceFlow(); });
  els.deviceCancel.addEventListener("click", () => { cancelDeviceFlow(); });
  els.deviceCopy.addEventListener("click", () => { onDeviceCopy(); });

  document.addEventListener("visibilitychange", () => {
    if (device.active) {
      if (document.visibilityState === "hidden") {
        device.hiddenAt = Date.now();
        if (device.pollTimer) { window.clearTimeout(device.pollTimer); device.pollTimer = 0; }
        if (device.countTimer) { window.clearInterval(device.countTimer); device.countTimer = 0; }
      } else {
        const away = device.hiddenAt ? Date.now() - device.hiddenAt : 0;
        device.hiddenAt = 0;
        if (away > DEVICE_HIDDEN_LIMIT_MS) {
          deviceFail("The tab was hidden for more than 30 seconds, so waiting stopped.");
        } else {
          updateDeviceCountdown();
          device.countTimer = window.setInterval(updateDeviceCountdown, 500);
          pollDeviceStatus();
        }
      }
    }
    if (demo.enabled || !signedIn) return;
    if (document.visibilityState === "hidden") {
      if (pollTimer) {
        window.clearTimeout(pollTimer);
        pollTimer = 0;
      }
      return;
    }
    if (polling) {
      if (pollTimer) {
        window.clearTimeout(pollTimer);
        pollTimer = 0;
      }
      poll();
    }
  });

  init();
})();
