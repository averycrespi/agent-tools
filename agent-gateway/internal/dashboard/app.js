// agent-gateway dashboard SPA
// No framework, no build step. fetch + EventSource only.
//
// All DOM construction uses document.createElement + textContent +
// addEventListener. No innerHTML with interpolation, no inline on* handlers.
// This keeps user-derived values out of HTML/attribute parsing and is a
// prerequisite for a strict script-src 'self' Content-Security-Policy.

// ---------- Utility ----------

function fmtTime(iso) {
  if (!iso) return "-";
  try {
    return new Date(iso).toLocaleString();
  } catch (e) {
    return iso;
  }
}

function fmtShortTime(iso) {
  if (!iso) return "-";
  try {
    return new Date(iso).toLocaleTimeString();
  } catch (e) {
    return iso;
  }
}

function verdictClass(v) {
  if (v === "allow") return "v-allow";
  if (v === "deny") return "v-deny";
  if (v === "require-approval") return "v-approve";
  return "";
}

function rowClass(entry) {
  var v = entry.verdict || "";
  if (v === "allow") return "row-allow";
  if (v === "deny") return "row-deny";
  if (v === "require-approval") return "row-approve";
  return "";
}

// Helpers for DOM construction.
function el(tag, className, text) {
  var e = document.createElement(tag);
  if (className) e.className = className;
  if (text != null) e.textContent = String(text);
  return e;
}

function td(text, className) {
  return el("td", className || "", text == null ? "-" : text);
}

function clear(node) {
  while (node && node.firstChild) node.removeChild(node.firstChild);
}

// ---------- Tab switching ----------

var currentTab = "live";

function switchTab(name) {
  document.querySelectorAll(".tab").forEach(function (t) {
    t.classList.remove("active");
  });
  document.querySelectorAll(".tab-pane").forEach(function (p) {
    p.classList.remove("active");
  });
  var tabEl = document.getElementById("tab-" + name);
  var paneEl = document.getElementById("pane-" + name);
  if (tabEl) tabEl.classList.add("active");
  if (paneEl) paneEl.classList.add("active");
  currentTab = name;

  if (name === "audit") {
    auditOffset = 0;
    loadAudit();
  }
  if (name === "rules") loadRules();
  if (name === "agents") loadAgents();
  if (name === "secrets") loadSecrets();
}

// ---------- SSE / Live feed ----------

var feedContainer = null;
var pendingCards = new Map(); // id → DOM element

function initLiveFeed() {
  feedContainer = document.getElementById("feed-rows");

  // Seed from last 200 audit entries.
  fetch("/dashboard/api/audit?limit=200")
    .then(function (r) {
      return r.json();
    })
    .then(function (data) {
      var records = (data.records || []).slice().reverse();
      records.forEach(function (rec) {
        appendFeedRow(rec, false);
      });
    })
    .catch(function () {
      /* ignore */
    });

  // Load pending approvals.
  fetch("/dashboard/api/pending")
    .then(function (r) {
      return r.json();
    })
    .then(function (items) {
      (items || []).forEach(function (pr) {
        upsertPendingCard(pr);
      });
      updatePendingBadge();
    })
    .catch(function () {
      /* ignore */
    });

  // SSE stream.
  var es = new EventSource("/dashboard/api/events");
  es.onmessage = function (e) {
    var msg;
    try {
      msg = JSON.parse(e.data);
    } catch (ex) {
      return;
    }
    handleSSEEvent(msg);
  };
  es.onopen = function () {
    var dot = document.getElementById("status");
    var lbl = document.getElementById("status-label");
    if (dot) dot.classList.add("connected");
    if (lbl) lbl.textContent = "Connected";
  };
  es.onerror = function () {
    var dot = document.getElementById("status");
    var lbl = document.getElementById("status-label");
    if (dot) dot.classList.remove("connected");
    if (lbl) lbl.textContent = "Disconnected";
  };
}

function handleSSEEvent(msg) {
  var kind = msg.kind || msg.type || "";
  var data = msg.data || msg;

  if (kind === "audit") {
    appendFeedRow(data, true);
  } else if (kind === "approval") {
    upsertPendingCard(data);
    updatePendingBadge();
  } else if (kind === "decided" || kind === "removed") {
    removePendingCard(data.id || data.request_id);
    updatePendingBadge();
    if (kind === "decided") {
      appendFeedRow(data, true);
    }
  }
}

function appendFeedRow(rec, prepend) {
  if (!feedContainer) return;
  var empty = document.getElementById("live-empty");
  if (empty) empty.style.display = "none";
  feedContainer.style.display = "";

  var isTunnel = rec.type === "tunnel" || rec.tunnel === true;
  var div = el("div", "feed-row" + (isTunnel ? " tunnel tunnel-header" : ""));

  var ts = fmtShortTime(rec.timestamp || rec.created_at);
  var agent = rec.agent_id || rec.agent || "";
  var tool = rec.tool || rec.method || "-";
  var vclass = verdictClass(rec.verdict);
  var vtext = rec.verdict || "-";

  div.appendChild(el("span", "feed-ts", ts));
  div.appendChild(el("span", "feed-agent", agent));
  div.appendChild(el("span", "feed-tool", tool));
  div.appendChild(el("span", "feed-verdict " + vclass, vtext));

  if (isTunnel) {
    div.addEventListener("click", function () {
      var body = div.nextElementSibling;
      if (body && body.classList.contains("tunnel-body")) {
        body.classList.toggle("open");
      }
    });
    var tunnelBody = el("div", "tunnel-body");
    var pre = el("pre", "", JSON.stringify(rec, null, 2));
    tunnelBody.appendChild(pre);
    if (prepend) {
      feedContainer.insertBefore(tunnelBody, feedContainer.firstChild);
      feedContainer.insertBefore(div, tunnelBody);
    } else {
      feedContainer.appendChild(div);
      feedContainer.appendChild(tunnelBody);
    }
  } else {
    if (prepend) {
      feedContainer.insertBefore(div, feedContainer.firstChild);
    } else {
      feedContainer.appendChild(div);
    }
  }
}

// ---------- Pending approvals ----------

function updatePendingBadge() {
  var badge = document.getElementById("pending-badge");
  var section = document.getElementById("pending-section");
  var count = pendingCards.size;
  if (badge) {
    badge.textContent = String(count);
    if (count === 0) badge.classList.add("hidden");
    else badge.classList.remove("hidden");
  }
  if (section) {
    section.style.display = count === 0 ? "none" : "block";
  }
}

function upsertPendingCard(pr) {
  var id = pr.id || pr.request_id;
  if (!id) return;
  var existing = pendingCards.get(id);
  if (existing) return; // already present

  var container = document.getElementById("pending-cards");
  if (!container) return;

  var div = el("div", "card pending");
  div.dataset.id = id;

  var ts = fmtShortTime(pr.timestamp || pr.created_at);
  var tool = pr.tool || pr.method || "-";
  var agent = pr.agent_id || pr.agent || "";

  // Pending rows: server enforces no body / unasserted headers.
  // We render only the fields the API returns (tool, agent, timestamp).
  div.appendChild(el("div", "method", tool));
  div.appendChild(el("div", "meta", ts + (agent ? " · " + agent : "")));

  var actions = el("div", "actions");
  var approveBtn = el("button", "btn-approve", "Approve");
  approveBtn.addEventListener("click", function () {
    decide(id, "approve");
  });
  var denyBtn = el("button", "btn-deny", "Deny");
  denyBtn.addEventListener("click", function () {
    decide(id, "deny");
  });
  actions.appendChild(approveBtn);
  actions.appendChild(denyBtn);
  div.appendChild(actions);

  container.appendChild(div);
  pendingCards.set(id, div);
}

function removePendingCard(id) {
  if (!id) return;
  var card = pendingCards.get(id);
  if (card) {
    card.remove();
    pendingCards.delete(id);
  }
}

function decide(id, decision) {
  fetch("/dashboard/api/decide", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ id: id, decision: decision }),
  }).then(function () {
    removePendingCard(id);
    updatePendingBadge();
  });
}

// ---------- Audit tab ----------

var auditLimit = 20;
var auditOffset = 0;
var auditTotal = 0;

function loadAudit() {
  var agent = (document.getElementById("audit-agent") || {}).value || "";
  var params = new URLSearchParams();
  if (agent.trim()) params.set("agent", agent.trim());
  params.set("limit", String(auditLimit));
  params.set("offset", String(auditOffset));

  fetch("/dashboard/api/audit?" + params.toString())
    .then(function (r) {
      return r.json();
    })
    .then(function (data) {
      renderAuditTable(data.records || [], data.total || 0);
    })
    .catch(function () {
      renderAuditTable([], 0);
    });
}

function renderAuditTable(records, total) {
  auditTotal = total;
  var body = document.getElementById("audit-body");
  var empty = document.getElementById("audit-empty");
  var wrap = document.getElementById("audit-table-wrap");
  var pag = document.getElementById("audit-pagination");

  if (!records.length) {
    if (body) clear(body);
    if (wrap) wrap.style.display = "none";
    if (empty) empty.style.display = "block";
    if (pag) pag.style.display = "none";
    return;
  }

  if (wrap) wrap.style.display = "";
  if (empty) empty.style.display = "none";

  if (body) {
    clear(body);
    records.forEach(function (rec) {
      var tr = el("tr", rowClass(rec));
      tr.appendChild(td(fmtTime(rec.timestamp || rec.created_at)));
      tr.appendChild(td(rec.agent_id || rec.agent || "-"));
      tr.appendChild(td(rec.tool || rec.method || "-"));
      tr.appendChild(td(rec.host || "-"));
      tr.appendChild(td(rec.rule || "-"));

      var verdictCell = el("td");
      var span = el("span", verdictClass(rec.verdict), rec.verdict || "-");
      verdictCell.appendChild(span);
      tr.appendChild(verdictCell);

      body.appendChild(tr);
    });
  }

  // Pagination
  if (pag) {
    pag.style.display = "flex";
    var start = auditOffset + 1;
    var end = Math.min(auditOffset + records.length, total);
    var info = document.getElementById("audit-page-info");
    if (info)
      info.textContent = "Showing " + start + "-" + end + " of " + total;
    var prev = document.getElementById("audit-prev");
    var next = document.getElementById("audit-next");
    if (prev) prev.disabled = auditOffset === 0;
    if (next) next.disabled = auditOffset + auditLimit >= total;
  }
}

function auditPrev() {
  auditOffset = Math.max(0, auditOffset - auditLimit);
  loadAudit();
}

function auditNext() {
  auditOffset += auditLimit;
  loadAudit();
}

var auditDebounce = null;
function debounceAudit() {
  clearTimeout(auditDebounce);
  auditDebounce = setTimeout(function () {
    auditOffset = 0;
    loadAudit();
  }, 300);
}

// ---------- Rules tab ----------

function loadRules() {
  fetch("/dashboard/api/rules")
    .then(function (r) {
      return r.json();
    })
    .then(function (data) {
      renderRules(data.rules || []);
    })
    .catch(function () {
      renderRules([]);
    });
}

function renderRules(rules) {
  var container = document.getElementById("rules-container");
  var empty = document.getElementById("rules-empty");
  if (!rules.length) {
    if (container) clear(container);
    if (empty) empty.style.display = "block";
    return;
  }
  if (empty) empty.style.display = "none";

  // Group by file (rules have a Name; treat the part before ":" as file if present).
  var groups = {};
  var groupOrder = [];
  rules.forEach(function (rule) {
    var parts = (rule.name || "").split(":");
    var file = parts.length > 1 ? parts[0] : "(unnamed)";
    if (!groups[file]) {
      groups[file] = [];
      groupOrder.push(file);
    }
    groups[file].push(rule);
  });

  if (!container) return;
  clear(container);

  groupOrder.forEach(function (file) {
    var groupEl = el("div", "rule-group");
    groupEl.appendChild(el("div", "rule-group-header", file));

    groups[file].forEach(function (rule) {
      var row = el("div", "rule-row");

      var nameSpan = el("span", "rule-name", rule.name || "-");
      if (rule.missing_secret) {
        nameSpan.appendChild(document.createTextNode(" "));
        nameSpan.appendChild(
          el("span", "badge badge-missing", "missing secret"),
        );
      }
      row.appendChild(nameSpan);

      if (rule.last_matched_at) {
        row.appendChild(
          el("span", "badge badge-amber", fmtShortTime(rule.last_matched_at)),
        );
      }

      if (rule.match_count_24h != null) {
        // Inline style kept from original; color/size are not user data.
        var countSpan = el(
          "span",
          "",
          String(rule.match_count_24h) + " in 24h",
        );
        countSpan.style.color = "var(--text-secondary)";
        countSpan.style.fontSize = "0.75rem";
        // Preceding space for readability.
        row.appendChild(document.createTextNode(" "));
        row.appendChild(countSpan);
      }

      row.appendChild(
        el(
          "span",
          "rule-verdict " + verdictClass(rule.verdict),
          rule.verdict || "-",
        ),
      );

      groupEl.appendChild(row);
    });

    container.appendChild(groupEl);
  });
}

// ---------- Agents tab ----------

function loadAgents() {
  fetch("/dashboard/api/agents")
    .then(function (r) {
      return r.json();
    })
    .then(function (data) {
      renderAgents(data.agents || []);
    })
    .catch(function () {
      renderAgents([]);
    });
}

function renderAgents(agents) {
  var body = document.getElementById("agents-body");
  var empty = document.getElementById("agents-empty");
  var wrap = document.getElementById("agents-table-wrap");

  if (!agents.length) {
    if (body) clear(body);
    if (wrap) wrap.style.display = "none";
    if (empty) empty.style.display = "block";
    return;
  }

  if (wrap) wrap.style.display = "";
  if (empty) empty.style.display = "none";

  if (!body) return;
  clear(body);

  agents.forEach(function (a) {
    var lastSeen = fmtTime(a.last_seen_at || a.last_seen);
    var allow24 = a.allow_count_24h != null ? String(a.allow_count_24h) : "-";
    var deny24 = a.deny_count_24h != null ? String(a.deny_count_24h) : "-";
    var approve24 =
      a.approve_count_24h != null ? String(a.approve_count_24h) : "-";

    var tr = el("tr");
    tr.appendChild(td(a.id || a.agent_id || "-"));
    tr.appendChild(td(a.name || "-"));
    tr.appendChild(td(lastSeen));
    tr.appendChild(td(allow24, "v-allow"));
    tr.appendChild(td(deny24, "v-deny"));
    tr.appendChild(td(approve24, "v-approve"));
    body.appendChild(tr);
  });
}

// ---------- Secrets tab ----------

function loadSecrets() {
  fetch("/dashboard/api/secrets")
    .then(function (r) {
      return r.json();
    })
    .then(function (data) {
      renderSecrets(data.secrets || []);
    })
    .catch(function () {
      renderSecrets([]);
    });
}

function renderSecrets(secrets) {
  var body = document.getElementById("secrets-body");
  var empty = document.getElementById("secrets-empty");
  var wrap = document.getElementById("secrets-table-wrap");

  if (!secrets.length) {
    if (body) clear(body);
    if (wrap) wrap.style.display = "none";
    if (empty) empty.style.display = "block";
    return;
  }

  if (wrap) wrap.style.display = "";
  if (empty) empty.style.display = "none";

  if (!body) return;
  clear(body);

  secrets.forEach(function (s) {
    // Never render plaintext values — server only returns metadata.
    var tr = el("tr");
    tr.appendChild(td(s.name || "-"));
    tr.appendChild(td(s.scope || "-"));
    tr.appendChild(td(fmtTime(s.created_at || s.created)));
    tr.appendChild(td(fmtTime(s.rotated_at || s.rotated)));
    tr.appendChild(td(fmtTime(s.last_used_at || s.last_used)));
    tr.appendChild(td(s.ref_count != null ? String(s.ref_count) : "-"));
    body.appendChild(tr);
  });
}

// ---------- Tunneled-hosts banner ----------

var BANNER_DISMISSED_KEY = "tunneled-banner-dismissed";

function initTunneledHostsBanner() {
  if (localStorage.getItem(BANNER_DISMISSED_KEY) === "true") {
    return; // dismissed by user
  }
  fetch("/dashboard/api/stats/tunneled-hosts?since=24h")
    .then(function (r) {
      return r.json();
    })
    .then(function (hosts) {
      if (!Array.isArray(hosts) || hosts.length === 0) return;
      renderTunneledBanner(hosts);
    })
    .catch(function () {
      /* ignore */
    });
}

function renderTunneledBanner(hosts) {
  var existing = document.getElementById("tunneled-banner");
  if (existing) existing.remove();

  var banner = el("div", "tunneled-banner");
  banner.id = "tunneled-banner";

  var strong = el("strong", "", "Tunneled hosts without rules (last 24h):");
  banner.appendChild(strong);
  banner.appendChild(document.createTextNode(" "));

  hosts.forEach(function (h, i) {
    if (i > 0) banner.appendChild(document.createTextNode(", "));
    banner.appendChild(
      el(
        "span",
        "tunneled-host",
        (h.host || "") + " (" + String(h.count) + ")",
      ),
    );
  });

  banner.appendChild(
    document.createTextNode(" — consider adding rules for these hosts."),
  );

  var dismissBtn = el("button", "tunneled-dismiss", "Dismiss");
  dismissBtn.addEventListener("click", dismissTunneledBanner);
  banner.appendChild(dismissBtn);

  var body = document.body;
  if (body) body.insertBefore(banner, body.firstChild);
}

function dismissTunneledBanner() {
  localStorage.setItem(BANNER_DISMISSED_KEY, "true");
  var banner = document.getElementById("tunneled-banner");
  if (banner) banner.remove();
}

// ---------- Init ----------

document.addEventListener("DOMContentLoaded", function () {
  // Wire up tab bar buttons (previously inline onclick).
  document.querySelectorAll(".tab[data-tab]").forEach(function (btn) {
    btn.addEventListener("click", function () {
      switchTab(btn.dataset.tab);
    });
  });

  // Wire up audit pagination + filter (previously inline onclick/oninput).
  var prev = document.getElementById("audit-prev");
  if (prev) prev.addEventListener("click", auditPrev);
  var next = document.getElementById("audit-next");
  if (next) next.addEventListener("click", auditNext);
  var agentInput = document.getElementById("audit-agent");
  if (agentInput) agentInput.addEventListener("input", debounceAudit);

  initLiveFeed();
  initTunneledHostsBanner();
  switchTab("live");
});
