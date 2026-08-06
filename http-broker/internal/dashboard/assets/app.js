// Read-only dashboard. Every request here is a GET; nothing on this page can
// change policy. Rules are edited in rules.json and reloaded with SIGHUP.
//
// The traffic view is deliberately the same shape as mcp-broker's audit log:
// one page at a time, a live strip that can be paused, filters that apply as
// you type, and rows that expand to the detail the columns leave out. Values
// from the audit log are attacker-influenced, so every one of them is written
// with textContent — this file builds DOM, it never assembles HTML strings.
(() => {
  "use strict";

  // One page of traffic, matching mcp-broker's audit log.
  const PAGE_SIZE = 20;
  const TRAFFIC_COLUMNS = 6;
  const FILTER_DEBOUNCE_MS = 300;
  const PANELS = ["traffic", "rules", "credentials"];

  const $ = (id) => document.getElementById(id);

  let activePanel = "traffic";

  // Traffic state. offset and total drive pagination; paused, pausedByExpand
  // and newCount drive the live strip.
  let records = [];
  let total = 0;
  let offset = 0;
  let expandedIdx = -1;
  let paused = false;
  let pausedByExpand = false;
  let newCount = 0;
  let filterTimer = null;

  const dash = (value) =>
    value === null || value === undefined || value === "" ? "—" : String(value);

  const cell = (value) => {
    const td = document.createElement("td");
    td.textContent = dash(value);
    return td;
  };

  // Date as well as time: a dashboard left open overnight otherwise shows two
  // days of traffic with nothing to tell them apart.
  const stamp = (iso) => {
    if (!iso) return "—";
    const d = new Date(iso);
    return Number.isNaN(d.getTime()) ? "—" : d.toLocaleString();
  };

  // --- Filters ---

  const filters = () => ({
    host: $("filter-host").value.trim(),
    source: $("filter-source").value,
    mode: $("filter-mode").value,
    outcome: $("filter-outcome").value,
  });

  const filtersActive = () => {
    const f = filters();
    return !!(f.host || f.source || f.mode || f.outcome);
  };

  function updateResetButton() {
    $("reset-filters").disabled = !filtersActive();
  }

  function clearFilters() {
    $("filter-host").value = "";
    $("filter-source").value = "";
    $("filter-mode").value = "";
    $("filter-outcome").value = "";
    updateResetButton();
  }

  // matchesFilters mirrors the server's WHERE clause so a streamed record is
  // only prepended when a refetch would have returned it. Host is a
  // case-insensitive substring there, so it is one here too.
  function matchesFilters(rec) {
    const f = filters();
    if (
      f.host &&
      !String(rec.host || "")
        .toLowerCase()
        .includes(f.host.toLowerCase())
    ) {
      return false;
    }
    if (f.source && rec.source !== f.source) return false;
    if (f.mode && rec.mode !== f.mode) return false;
    if (f.outcome && rec.outcome !== f.outcome) return false;
    return true;
  }

  function filtersChanged() {
    offset = 0;
    updateResetButton();
    loadTraffic();
  }

  function debounceFilters() {
    clearTimeout(filterTimer);
    updateResetButton();
    filterTimer = setTimeout(filtersChanged, FILTER_DEBOUNCE_MS);
  }

  function resetFilters() {
    clearFilters();
    offset = 0;
    paused = false;
    pausedByExpand = false;
    loadTraffic();
  }

  // --- Rows ---

  // Row tint follows the outcome, so a page of traffic reads at a glance.
  function rowClass(rec) {
    if (rec.outcome === "error") return "row-error";
    if (rec.outcome === "blocked") return "row-blocked";
    return "row-allowed";
  }

  function trafficRow(rec, idx) {
    const tr = document.createElement("tr");
    tr.className = rowClass(rec);
    tr.dataset.idx = String(idx);
    tr.tabIndex = 0;
    tr.setAttribute("aria-expanded", "false");
    tr.append(
      cell(stamp(rec.ts)),
      cell(rec.host),
      // The Source column: the rule that decided, or what decided instead. A
      // rule is a source, so the rule name goes here rather than in a column
      // that would be blank on every row policy decided.
      cell(rec.matched_rule || rec.source),
      cell(rec.mode),
      cell(rec.outcome),
      cell(rec.status || ""),
    );
    return tr;
  }

  // lines drops empty values, so a field never renders "Query: —".
  function lines(pairs) {
    const out = [];
    for (const [label, value] of pairs) {
      if (value === null || value === undefined || value === "") continue;
      out.push(`${label}: ${value}`);
    }
    return out;
  }

  function detailField(label, body, kind) {
    const field = document.createElement("div");
    field.className = `detail-field${kind ? " " + kind : ""}`;
    const heading = document.createElement("div");
    heading.className = "detail-label";
    heading.textContent = label;
    const value = document.createElement("pre");
    value.className = "detail-value";
    value.textContent = body.join("\n");
    field.append(heading, value);
    return field;
  }

  // failureLines is why the expanded row exists for anything that went wrong:
  // outcome, status and the recorded reason, which is the only place a deny or
  // a refused dial explains itself.
  function failureLines(rec) {
    const body = lines([
      ["Outcome", rec.outcome],
      ["Status", rec.status || ""],
      ["Reason", rec.error],
    ]);
    if (!rec.error) {
      body.push(
        rec.outcome === "error"
          ? "No further detail was recorded for this failure."
          : "No reason was recorded. Rows written before reasons were audited look like this.",
      );
    }
    return body;
  }

  function detailRow(rec, idx) {
    const tr = document.createElement("tr");
    tr.id = `traffic-detail-${idx}`;
    tr.className = `${rowClass(rec)} detail-row`;
    const td = document.createElement("td");
    td.colSpan = TRAFFIC_COLUMNS;

    const content = document.createElement("div");
    content.className = "detail-content";
    content.append(
      detailField(
        "Request",
        lines([
          ["Method", rec.method],
          ["Path", rec.path],
          ["Query", rec.query],
          ["Target", `${rec.host}:${rec.port}`],
          ["Interception", rec.interception],
        ]),
      ),
      detailField(
        "Decision",
        lines([
          ["Rule", rec.matched_rule || "none"],
          ["Source", rec.source],
          ["Mode", rec.mode],
          ["Credential", rec.credential_ref],
          ["Headers injected", rec.injection],
        ]),
      ),
      detailField(
        "Transfer",
        lines([
          ["Status", rec.status || ""],
          [
            "Duration",
            rec.duration_ms === null || rec.duration_ms === undefined
              ? ""
              : `${rec.duration_ms} ms`,
          ],
          ["Bytes in", rec.bytes_in],
          ["Bytes out", rec.bytes_out],
        ]),
      ),
    );
    if (rec.outcome !== "allowed" || rec.error) {
      content.append(detailField("Failure", failureLines(rec), "failure"));
    }

    td.appendChild(content);
    tr.appendChild(td);
    return tr;
  }

  function collapseDetail() {
    if (expandedIdx < 0) return;
    const open = $(`traffic-detail-${expandedIdx}`);
    if (open) open.remove();
    const source = $("traffic-rows").querySelector(
      `tr[data-idx="${expandedIdx}"]`,
    );
    if (source) source.setAttribute("aria-expanded", "false");
    expandedIdx = -1;
  }

  function toggleDetail(idx) {
    const wasExpanded = expandedIdx === idx;
    collapseDetail();

    if (wasExpanded) {
      // Resume only if this expansion is what paused the feed; a deliberate
      // pause survives an expand and collapse.
      if (pausedByExpand) togglePause();
      return;
    }

    // Pause while a row is open. A live prepend shifts every row index, so the
    // alternative is collapsing the row the reader just opened.
    if (!paused) {
      paused = true;
      pausedByExpand = true;
      updateStrip();
    }

    const source = $("traffic-rows").querySelector(`tr[data-idx="${idx}"]`);
    if (!source) return;
    expandedIdx = idx;
    source.setAttribute("aria-expanded", "true");
    source.after(detailRow(records[idx], idx));
  }

  // --- Live strip ---

  function stripButton(label, onClick) {
    const button = document.createElement("button");
    button.className = "strip-btn";
    button.textContent = label;
    button.addEventListener("click", onClick);
    return button;
  }

  // updateStrip rewrites the strip from the current state. Three states:
  //   Live    — page 1, no filter, not paused.
  //   Paused  — paused, whether by the button or by an open row.
  //   Banner  — filtered or paginated, so out of the live view.
  function updateStrip() {
    const strip = $("traffic-strip");
    strip.replaceChildren();
    if (activePanel !== "traffic") return;

    if (paused) {
      const inner = document.createElement("div");
      inner.className = "strip-inner";
      const label = document.createElement("span");
      label.textContent =
        "⏸ Paused" +
        (pausedByExpand ? " — row expanded" : "") +
        (newCount > 0 ? ` — ${newCount} new` : "");
      inner.append(label, stripButton("▶ Resume", togglePause));
      strip.appendChild(inner);
      return;
    }

    if (offset > 0 || filtersActive()) {
      const banner = document.createElement("button");
      banner.className = "strip-banner";
      banner.textContent =
        (newCount > 0 ? `${newCount} new records — ` : "") +
        "return to live view";
      banner.addEventListener("click", resetFilters);
      strip.appendChild(banner);
      return;
    }

    const inner = document.createElement("div");
    inner.className = "strip-inner";
    const dot = document.createElement("span");
    dot.className = "strip-dot";
    const label = document.createElement("span");
    label.textContent = "Live";
    inner.append(dot, label, stripButton("❚❚ Pause", togglePause));
    strip.appendChild(inner);
  }

  function togglePause() {
    if (paused) {
      paused = false;
      pausedByExpand = false;
      loadTraffic(); // clears newCount and redraws the strip
    } else {
      paused = true;
      updateStrip();
    }
  }

  // --- Loading ---

  function setEmpty(name, message) {
    const empty = $(`${name}-empty`);
    empty.textContent = message;
    empty.hidden = false;
    $(`${name}-table-wrap`).hidden = true;
  }

  function clearEmpty(name) {
    $(`${name}-empty`).hidden = true;
    $(`${name}-table-wrap`).hidden = false;
  }

  function updatePagination() {
    const pagination = $("traffic-pagination");
    if (records.length === 0) {
      pagination.hidden = true;
      return;
    }
    pagination.hidden = false;
    const start = offset + 1;
    const end = Math.min(offset + records.length, total);
    $("traffic-page-info").textContent = `Showing ${start}-${end} of ${total}`;
    $("traffic-prev").disabled = offset === 0;
    $("traffic-next").disabled = offset + PAGE_SIZE >= total;
  }

  async function getJSON(path) {
    const resp = await fetch(path, { credentials: "same-origin" });
    if (!resp.ok) throw new Error(`${path}: ${resp.status}`);
    return resp.json();
  }

  async function loadTraffic() {
    const f = filters();
    const params = new URLSearchParams();
    if (f.host) params.set("host", f.host);
    if (f.source) params.set("source", f.source);
    if (f.mode) params.set("mode", f.mode);
    if (f.outcome) params.set("outcome", f.outcome);
    params.set("limit", String(PAGE_SIZE));
    params.set("offset", String(offset));

    // A refetch drops the open row and the "new" counter, so an expand-induced
    // pause has nothing left to protect.
    newCount = 0;
    expandedIdx = -1;
    if (pausedByExpand) {
      paused = false;
      pausedByExpand = false;
    }
    updateResetButton();

    const tbody = $("traffic-rows");
    try {
      const data = await getJSON(`/dashboard/api/audit?${params}`);
      records = data.records || [];
      total = data.total || 0;
      tbody.replaceChildren();

      if (records.length === 0) {
        setEmpty("traffic", "No matching requests yet.");
      } else {
        clearEmpty("traffic");
        records.forEach((rec, idx) => tbody.appendChild(trafficRow(rec, idx)));
      }
    } catch (err) {
      records = [];
      total = 0;
      tbody.replaceChildren();
      setEmpty("traffic", `Could not load traffic: ${err.message}`);
    }
    updatePagination();
    updateStrip();
  }

  function page(delta) {
    offset = Math.max(0, offset + delta * PAGE_SIZE);
    loadTraffic();
  }

  async function loadRules() {
    const tbody = $("rules-rows");
    tbody.replaceChildren();
    try {
      const data = await getJSON("/dashboard/api/rules");
      $("rules-fallthrough").textContent =
        `Unmatched hosts: ${data.fallthrough}`;
      if (!data.rules || data.rules.length === 0) {
        setEmpty(
          "rules",
          "No rules configured. Every host follows the fallthrough policy.",
        );
        return;
      }
      clearEmpty("rules");
      for (const r of data.rules) {
        const tr = document.createElement("tr");
        tr.append(
          cell(r.name),
          cell(r.mode),
          cell(r.host),
          // allow_private is omitempty, so an absent field means false.
          cell(r.allow_private ? "true" : "false"),
          cell(r.path),
          cell(r.method),
          cell(r.ports ? r.ports.join(", ") : ""),
          cell(
            r.inject && r.inject.set
              ? Object.keys(r.inject.set).join(", ")
              : "",
          ),
        );
        tbody.appendChild(tr);
      }
    } catch (err) {
      setEmpty("rules", `Could not load rules: ${err.message}`);
    }
  }

  async function loadCredentials() {
    const tbody = $("credential-rows");
    const indexError = $("credentials-index-error");
    tbody.replaceChildren();
    indexError.hidden = true;
    try {
      const data = await getJSON("/dashboard/api/credentials");
      // A credential index that could not be read shortens this table. Saying
      // so beats a list that looks complete while missing entries.
      if (data.index_error) {
        indexError.textContent = data.index_error;
        indexError.hidden = false;
      }
      if (!data.credentials || data.credentials.length === 0) {
        setEmpty("credentials", "No credentials configured.");
        return;
      }
      clearEmpty("credentials");
      for (const c of data.credentials) {
        const tr = document.createElement("tr");
        tr.append(
          cell(c.name),
          cell(c.source),
          cell(c.hosts ? c.hosts.join(", ") : ""),
          // Plain true/false, not a chip that vanishes when false: an absent
          // chip reads as missing data rather than as "no", the same reason
          // allow_private is rendered this way above.
          cell(c.referenced ? "true" : "false"),
        );
        tbody.appendChild(tr);
      }
    } catch (err) {
      setEmpty("credentials", `Could not load credentials: ${err.message}`);
    }
  }

  // --- Live feed ---

  // handleRecord applies one streamed record:
  //   Traffic panel inactive        → ignore.
  //   Live, page 1, matching filter → prepend, drop the last row.
  //   Anything else                 → count it and say so in the strip.
  function handleRecord(rec) {
    if (activePanel !== "traffic") return;

    const matches = matchesFilters(rec);
    if (matches) total++;
    if (offset !== 0 || paused || !matches) {
      newCount++;
      updateStrip();
      return;
    }

    // Expanding pauses the feed, so no row should be open here; collapse
    // defensively in case an event was already in flight when it opened.
    collapseDetail();

    const tbody = $("traffic-rows");
    if (records.length === 0) clearEmpty("traffic");

    // Shift the existing indices so they still point into records after the
    // unshift below.
    for (const row of tbody.querySelectorAll("tr[data-idx]")) {
      row.dataset.idx = String(Number(row.dataset.idx) + 1);
    }

    records.unshift(rec);
    if (records.length > PAGE_SIZE) records.pop();

    const row = trafficRow(rec, 0);
    tbody.prepend(row);
    while (tbody.children.length > PAGE_SIZE) tbody.lastElementChild.remove();

    row.classList.add("row-new");
    requestAnimationFrame(() => {
      requestAnimationFrame(() => row.classList.remove("row-new"));
    });

    updatePagination();
    updateStrip();
  }

  function connectStream() {
    const dot = $("stream-dot");
    const label = $("stream-label");
    const source = new EventSource("/dashboard/api/events");

    // Two states, matching mcp-broker. EventSource reconnects on its own, so
    // a dropped stream reads as Disconnected until it comes back.
    source.onopen = () => {
      dot.className = "dot connected";
      label.textContent = "Connected";
    };
    source.onerror = () => {
      dot.className = "dot";
      label.textContent = "Disconnected";
    };
    source.addEventListener("audit", (event) => {
      let rec;
      try {
        rec = JSON.parse(event.data);
      } catch {
        return;
      }
      handleRecord(rec);
    });
  }

  // --- Panels ---

  function selectPanel(name) {
    activePanel = name;
    for (const tab of document.querySelectorAll(".tab")) {
      tab.classList.toggle("active", tab.dataset.panel === name);
    }
    for (const panel of document.querySelectorAll(".panel")) {
      panel.classList.toggle("hidden", panel.id !== `panel-${name}`);
    }

    if (location.hash !== `#${name}`) {
      history.replaceState(null, "", `#${name}`);
    }

    if (name === "traffic") {
      offset = 0;
      loadTraffic();
    }
    if (name === "rules") loadRules();
    if (name === "credentials") loadCredentials();
    updateStrip();
  }

  document.addEventListener("DOMContentLoaded", () => {
    for (const tab of document.querySelectorAll(".tab")) {
      tab.addEventListener("click", () => selectPanel(tab.dataset.panel));
    }

    $("filter-host").addEventListener("input", debounceFilters);
    $("filter-source").addEventListener("change", filtersChanged);
    $("filter-mode").addEventListener("change", filtersChanged);
    $("filter-outcome").addEventListener("change", filtersChanged);
    $("reset-filters").addEventListener("click", resetFilters);
    $("traffic-prev").addEventListener("click", () => page(-1));
    $("traffic-next").addEventListener("click", () => page(1));

    // Delegated so the handler survives every re-render, and so a row added by
    // the live feed needs no wiring of its own.
    const tbody = $("traffic-rows");
    const rowFor = (target) =>
      target.closest ? target.closest("tr[data-idx]") : null;
    tbody.addEventListener("click", (event) => {
      const row = rowFor(event.target);
      if (row) toggleDetail(Number(row.dataset.idx));
    });
    tbody.addEventListener("keydown", (event) => {
      if (event.key !== "Enter" && event.key !== " ") return;
      const row = rowFor(event.target);
      if (!row) return;
      event.preventDefault();
      toggleDetail(Number(row.dataset.idx));
    });

    const fromHash = location.hash.replace(/^#/, "");
    selectPanel(PANELS.includes(fromHash) ? fromHash : "traffic");
    window.addEventListener("hashchange", () => {
      const name = location.hash.replace(/^#/, "");
      if (PANELS.includes(name) && name !== activePanel) selectPanel(name);
    });

    connectStream();
  });
})();
