// agent-gateway dashboard SPA
// No framework, no build step. fetch + EventSource only.

// ---------- Utility ----------

function esc(s) {
  var d = document.createElement("div");
  d.textContent = s == null ? "" : String(s);
  return d.innerHTML;
}

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

function verdictBadge(v) {
  var cls = verdictClass(v);
  return '<span class="' + cls + '">' + esc(v || "-") + "</span>";
}

function rowClass(entry) {
  var v = entry.verdict || "";
  if (v === "allow") return "row-allow";
  if (v === "deny") return "row-deny";
  if (v === "require-approval") return "row-approve";
  return "";
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
  var div = document.createElement("div");
  var isTunnel = rec.type === "tunnel" || rec.tunnel === true;
  div.className = "feed-row" + (isTunnel ? " tunnel tunnel-header" : "");

  var ts = fmtShortTime(rec.timestamp || rec.created_at);
  var agent = esc(rec.agent_id || rec.agent || "");
  var tool = esc(rec.tool || rec.method || "-");
  var vclass = verdictClass(rec.verdict);
  var vtext = esc(rec.verdict || "-");

  div.innerHTML =
    '<span class="feed-ts">' +
    ts +
    "</span>" +
    '<span class="feed-agent">' +
    agent +
    "</span>" +
    '<span class="feed-tool">' +
    tool +
    "</span>" +
    '<span class="feed-verdict ' +
    vclass +
    '">' +
    vtext +
    "</span>";

  if (isTunnel) {
    div.addEventListener("click", function () {
      var body = div.nextElementSibling;
      if (body && body.classList.contains("tunnel-body")) {
        body.classList.toggle("open");
      }
    });
    var tunnelBody = document.createElement("div");
    tunnelBody.className = "tunnel-body";
    tunnelBody.innerHTML =
      "<pre>" + esc(JSON.stringify(rec, null, 2)) + "</pre>";
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

  var div = document.createElement("div");
  div.className = "card pending";
  div.dataset.id = id;

  var ts = fmtShortTime(pr.timestamp || pr.created_at);
  var tool = esc(pr.tool || pr.method || "-");
  var agent = esc(pr.agent_id || pr.agent || "");

  // Pending rows: server enforces no body / unasserted headers.
  // We render only the fields the API returns (tool, agent, timestamp).
  div.innerHTML =
    '<div class="method">' +
    tool +
    "</div>" +
    '<div class="meta">' +
    ts +
    (agent ? " \u00b7 " + agent : "") +
    "</div>" +
    '<div class="actions">' +
    '<button class="btn-approve" onclick="decide(\'' +
    esc(id) +
    "','approve')\">Approve</button>" +
    '<button class="btn-deny" onclick="decide(\'' +
    esc(id) +
    "','deny')\">Deny</button>" +
    "</div>";

  container.appendChild(div);
  pendingCards.set(id, div);
}

function removePendingCard(id) {
  if (!id) return;
  var el = pendingCards.get(id);
  if (el) {
    el.remove();
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
    if (body) body.innerHTML = "";
    if (wrap) wrap.style.display = "none";
    if (empty) empty.style.display = "block";
    if (pag) pag.style.display = "none";
    return;
  }

  if (wrap) wrap.style.display = "";
  if (empty) empty.style.display = "none";

  var html = "";
  records.forEach(function (rec) {
    var rc = rowClass(rec);
    var ts = fmtTime(rec.timestamp || rec.created_at);
    var tool = esc(rec.tool || rec.method || "-");
    var agent = esc(rec.agent_id || rec.agent || "-");
    var host = esc(rec.host || "-");
    var rule = esc(rec.rule || "-");
    var verdict = verdictBadge(rec.verdict);
    html +=
      '<tr class="' +
      rc +
      '">' +
      "<td>" +
      ts +
      "</td>" +
      "<td>" +
      agent +
      "</td>" +
      "<td>" +
      tool +
      "</td>" +
      "<td>" +
      host +
      "</td>" +
      "<td>" +
      rule +
      "</td>" +
      "<td>" +
      verdict +
      "</td>" +
      "</tr>";
  });
  if (body) body.innerHTML = html;

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
    if (container) container.innerHTML = "";
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

  var html = "";
  groupOrder.forEach(function (file) {
    html += '<div class="rule-group">';
    html += '<div class="rule-group-header">' + esc(file) + "</div>";
    groups[file].forEach(function (rule) {
      var vc = verdictClass(rule.verdict);
      var missingBadge = rule.missing_secret
        ? ' <span class="badge badge-missing">missing secret</span>'
        : "";
      var lastMatch = rule.last_matched_at
        ? '<span class="badge badge-amber">' +
          esc(fmtShortTime(rule.last_matched_at)) +
          "</span>"
        : "";
      var count24 =
        rule.match_count_24h != null
          ? ' <span style="color:var(--text-secondary);font-size:0.75rem">' +
            esc(String(rule.match_count_24h)) +
            " in 24h</span>"
          : "";
      html +=
        '<div class="rule-row">' +
        '<span class="rule-name">' +
        esc(rule.name || "-") +
        missingBadge +
        "</span>" +
        lastMatch +
        count24 +
        '<span class="rule-verdict ' +
        vc +
        '">' +
        esc(rule.verdict || "-") +
        "</span>" +
        "</div>";
    });
    html += "</div>";
  });

  if (container) container.innerHTML = html;
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
    if (body) body.innerHTML = "";
    if (wrap) wrap.style.display = "none";
    if (empty) empty.style.display = "block";
    return;
  }

  if (wrap) wrap.style.display = "";
  if (empty) empty.style.display = "none";

  var html = "";
  agents.forEach(function (a) {
    var lastSeen = fmtTime(a.last_seen_at || a.last_seen);
    var allow24 = a.allow_count_24h != null ? String(a.allow_count_24h) : "-";
    var deny24 = a.deny_count_24h != null ? String(a.deny_count_24h) : "-";
    var approve24 =
      a.approve_count_24h != null ? String(a.approve_count_24h) : "-";
    html +=
      "<tr>" +
      "<td>" +
      esc(a.id || a.agent_id || "-") +
      "</td>" +
      "<td>" +
      esc(a.name || "-") +
      "</td>" +
      "<td>" +
      esc(lastSeen) +
      "</td>" +
      '<td class="v-allow">' +
      esc(allow24) +
      "</td>" +
      '<td class="v-deny">' +
      esc(deny24) +
      "</td>" +
      '<td class="v-approve">' +
      esc(approve24) +
      "</td>" +
      "</tr>";
  });
  if (body) body.innerHTML = html;
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
    if (body) body.innerHTML = "";
    if (wrap) wrap.style.display = "none";
    if (empty) empty.style.display = "block";
    return;
  }

  if (wrap) wrap.style.display = "";
  if (empty) empty.style.display = "none";

  var html = "";
  secrets.forEach(function (s) {
    // Never render plaintext values — server only returns metadata.
    html +=
      "<tr>" +
      "<td>" +
      esc(s.name || "-") +
      "</td>" +
      "<td>" +
      esc(s.scope || "-") +
      "</td>" +
      "<td>" +
      esc(fmtTime(s.created_at || s.created)) +
      "</td>" +
      "<td>" +
      esc(fmtTime(s.rotated_at || s.rotated)) +
      "</td>" +
      "<td>" +
      esc(fmtTime(s.last_used_at || s.last_used)) +
      "</td>" +
      "<td>" +
      esc(s.ref_count != null ? String(s.ref_count) : "-") +
      "</td>" +
      "</tr>";
  });
  if (body) body.innerHTML = html;
}

// ---------- Init ----------

document.addEventListener("DOMContentLoaded", function () {
  initLiveFeed();
  switchTab("live");
});
