// Read-only dashboard. Every request here is a GET; nothing on this page can
// change policy. Rules are edited in rules.json and reloaded with SIGHUP.
(() => {
  "use strict";

  const MAX_LIVE_ROWS = 500;

  const $ = (id) => document.getElementById(id);

  // text() rather than innerHTML anywhere a value from the audit log is
  // rendered: hosts, paths and query strings are attacker-influenced.
  const cell = (value, className) => {
    const td = document.createElement("td");
    if (className) td.className = className;
    td.textContent = value === null || value === undefined || value === "" ? "—" : String(value);
    return td;
  };

  const badge = (text, kind) => {
    const td = document.createElement("td");
    if (!text) {
      td.textContent = "—";
      return td;
    }
    const span = document.createElement("span");
    span.className = `badge ${kind || ""}`.trim();
    span.textContent = text;
    td.appendChild(span);
    return td;
  };

  const shortTime = (iso) => {
    if (!iso) return "—";
    const d = new Date(iso);
    return Number.isNaN(d.getTime()) ? "—" : d.toLocaleTimeString();
  };

  function trafficRow(rec) {
    const tr = document.createElement("tr");
    tr.appendChild(cell(shortTime(rec.ts)));
    tr.appendChild(badge(rec.outcome, rec.outcome));
    tr.appendChild(cell(rec.host, "host"));
    tr.appendChild(cell(rec.port));
    tr.appendChild(cell(rec.method));
    tr.appendChild(cell(rec.path, "path"));
    tr.appendChild(cell(rec.status || ""));
    tr.appendChild(cell(rec.matched_rule));
    tr.appendChild(badge(rec.mode, "mode"));
    tr.appendChild(cell(rec.credential_ref, "cred"));
    tr.appendChild(cell(rec.duration_ms));
    return tr;
  }

  function setEmpty(tbody, colspan, message) {
    const tr = document.createElement("tr");
    const td = document.createElement("td");
    td.colSpan = colspan;
    td.className = "empty";
    td.textContent = message;
    tr.appendChild(td);
    tbody.appendChild(tr);
  }

  async function getJSON(path) {
    const resp = await fetch(path, { credentials: "same-origin" });
    if (!resp.ok) throw new Error(`${path}: ${resp.status}`);
    return resp.json();
  }

  async function loadTraffic() {
    const params = new URLSearchParams();
    const host = $("filter-host").value.trim();
    const outcome = $("filter-outcome").value;
    if (host) params.set("host", host);
    if (outcome) params.set("outcome", outcome);

    const tbody = $("traffic-rows");
    tbody.replaceChildren();

    try {
      const data = await getJSON(`/dashboard/api/audit?${params}`);
      if (!data.records || data.records.length === 0) {
        setEmpty(tbody, 11, "No matching requests yet.");
      } else {
        for (const rec of data.records) tbody.appendChild(trafficRow(rec));
      }
      $("traffic-meta").textContent = `${data.records ? data.records.length : 0} shown of ${data.total} total`;
    } catch (err) {
      setEmpty(tbody, 11, `Could not load traffic: ${err.message}`);
    }
  }

  async function loadRules() {
    const tbody = $("rules-rows");
    tbody.replaceChildren();
    try {
      const data = await getJSON("/dashboard/api/rules");
      $("rules-fallthrough").textContent = `Unmatched hosts: ${data.fallthrough}`;
      if (!data.rules || data.rules.length === 0) {
        setEmpty(tbody, 7, "No rules configured. Every host follows the fallthrough policy.");
        return;
      }
      for (const r of data.rules) {
        const tr = document.createElement("tr");
        tr.appendChild(cell(r.name));
        tr.appendChild(badge(r.mode, "mode"));
        tr.appendChild(cell(r.host, "host"));
        tr.appendChild(cell(r.path, "path"));
        tr.appendChild(cell(r.method));
        tr.appendChild(cell(r.ports ? r.ports.join(", ") : ""));
        const injects = r.inject && r.inject.set ? Object.keys(r.inject.set).join(", ") : "";
        tr.appendChild(cell(injects));
        tbody.appendChild(tr);
      }
    } catch (err) {
      setEmpty(tbody, 7, `Could not load rules: ${err.message}`);
    }
  }

  async function loadCredentials() {
    const tbody = $("credential-rows");
    tbody.replaceChildren();
    try {
      const data = await getJSON("/dashboard/api/credentials");
      if (!data.credentials || data.credentials.length === 0) {
        setEmpty(tbody, 3, "No credentials configured.");
        return;
      }
      for (const c of data.credentials) {
        const tr = document.createElement("tr");
        tr.appendChild(cell(c.name, "cred"));
        tr.appendChild(cell(c.source));
        tr.appendChild(cell(c.hosts ? c.hosts.join(", ") : "", "host"));
        tbody.appendChild(tr);
      }
    } catch (err) {
      setEmpty(tbody, 3, `Could not load credentials: ${err.message}`);
    }
  }

  function connectStream() {
    const dot = $("stream-dot");
    const label = $("stream-label");
    const source = new EventSource("/dashboard/api/events");

    source.onopen = () => {
      dot.className = "dot live";
      label.textContent = "live";
    };
    source.onerror = () => {
      dot.className = "dot down";
      label.textContent = "reconnecting";
    };
    source.addEventListener("audit", (event) => {
      let rec;
      try {
        rec = JSON.parse(event.data);
      } catch {
        return;
      }
      // Only prepend to the unfiltered view, so a live event cannot appear to
      // contradict an active filter.
      if ($("filter-host").value.trim() || $("filter-outcome").value) return;
      const tbody = $("traffic-rows");
      const empty = tbody.querySelector(".empty");
      if (empty) tbody.replaceChildren();
      tbody.prepend(trafficRow(rec));
      while (tbody.children.length > MAX_LIVE_ROWS) tbody.lastElementChild.remove();
    });
  }

  function selectPanel(name) {
    for (const tab of document.querySelectorAll(".tab")) {
      tab.classList.toggle("selected", tab.dataset.panel === name);
    }
    for (const panel of document.querySelectorAll(".panel")) {
      panel.classList.toggle("hidden", panel.id !== `panel-${name}`);
    }
    if (name === "rules") loadRules();
    if (name === "credentials") loadCredentials();
  }

  document.addEventListener("DOMContentLoaded", () => {
    for (const tab of document.querySelectorAll(".tab")) {
      tab.addEventListener("click", () => selectPanel(tab.dataset.panel));
    }
    $("apply-filters").addEventListener("click", loadTraffic);
    $("filter-host").addEventListener("keydown", (e) => {
      if (e.key === "Enter") loadTraffic();
    });

    loadTraffic();
    connectStream();
  });
})();
