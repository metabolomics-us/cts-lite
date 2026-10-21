let allData = [];
let currentPage = 1;
let pageSize = 10;
let activeController = null;
let classyfireRequested = false; // did this request use ClassyFire? classyfireEnabled tracks current checkbox state
let activeFilter = "all"; // match-status class from getMatchStatus, defaults to "all"

// Disable download buttons while ClassyFire is running
function setDownloadsLoading(loading) {
  const buttons = document.getElementById("download-buttons");
  if (!buttons) return;
  buttons.classList.toggle("loading", loading);
  buttons.querySelectorAll(".download-btn").forEach(b => { b.disabled = loading; });
}

// Update the ClassyFire progress bar. While PubChem rows are pending the total is
// an upper bound that shrinks as they settle, so it carries an asterisk explained
// by the note below the PubChem line
function showCfProgress(done, total, upperBound = false) {
  const wrap = document.getElementById("cf-progress");
  const note = document.querySelector("#cf-progress .cf-progress-note");
  if (note) note.hidden = !(upperBound && total > 0);
  if (!wrap) return;
  if (total <= 0) { wrap.hidden = true; return; }
  wrap.hidden = false;
  const pct = Math.round((done / total) * 100);
  wrap.querySelector(".cf-progress-fill").style.width = pct + "%";
  wrap.querySelector(".cf-progress-label").textContent =
    `Classifying with ClassyFire: ${done} / ${total}${upperBound ? "*" : ""}`;
}

// Update the PubChem Live status line
function showPcbProgress(done, total) {
  const wrap = document.getElementById("pcb-progress");
  if (!wrap) return;
  if (total <= 0 || done >= total) { wrap.hidden = true; return; }
  wrap.hidden = false;
  wrap.querySelector(".pcb-progress-label").textContent =
    `PubChem Live lookups: ${done} / ${total}`;
}

// Hide the ClassyFire progress bar (i.e. when classification is done)
function hideCfProgress() {
  const wrap = document.getElementById("cf-progress");
  if (wrap) wrap.hidden = true;
  const note = document.querySelector("#cf-progress .cf-progress-note");
  if (note) note.hidden = true;
}

// Show how many requests are sharing the ClassyFire queue
function updateCfQueue(depth) {
  const el = document.getElementById("cf-queue");
  const others = depth - 1;
  if (!el) return;
  if (others >= 1) {
    el.textContent = `Your request may take longer: ClassyFire is taking turns with ${others} other ${others === 1 ? "request" : "requests"}`;
    el.hidden = false;
  } else {
    el.hidden = true;
  }
}

// Show the "ClassyFire is down" popup and move focus to its close button
function showCfDownModal() {
  const m = document.getElementById("cf-down-modal");
  if (!m) return;
  m.hidden = false;
  document.getElementById("cf-modal-close").focus();
}

// Hide the "ClassyFire is down" popup
function hideCfDownModal() {
  const m = document.getElementById("cf-down-modal");
  if (m) m.hidden = true;
}

// Check whether the ClassyFire backend is reachable. If it is down, disable the
// settings toggle and popup the modal when the row is clicked
async function initClassyfireStatus() {
  const row = document.getElementById("classyfire-row");
  const checkbox = document.getElementById("classyfire-enabled");
  if (!row || !checkbox) return;

  let up = true;
  try {
    const res = await fetch("/classyfire/status");
    if (res.ok) up = (await res.json()).up !== false;
  } catch (e) {
    // Network error reaching our own status endpoint, leave the toggle usable
    return;
  }
  if (up) return;

  checkbox.checked = false;
  checkbox.disabled = true;
  row.classList.add("cf-disabled");
  row.title = "ClassyFire is currently unavailable";
  row.addEventListener("click", (e) => {
    if (e.target.closest("a")) return; // let the docs question-mark link work
    e.preventDefault();
    showCfDownModal();
  });
}

// Read NDJSON stream, dispatching each message by type
async function consumeMatchStream(response, { onMatches, onPubchem, onClassifying, onClassyfire, onDone }) {
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  const dispatch = (line) => {
    const text = line.trim();
    if (!text) return;
    const msg = JSON.parse(text);
    if (msg.type === "matches") onMatches(msg);
    else if (msg.type === "pubchem") onPubchem(msg);
    else if (msg.type === "classifying") onClassifying(msg);
    else if (msg.type === "classyfire") onClassyfire(msg);
    else if (msg.type === "done") onDone();
  };
  while (true) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    let nl;
    while ((nl = buffer.indexOf("\n")) >= 0) {
      dispatch(buffer.slice(0, nl));
      buffer = buffer.slice(nl + 1);
    }
  }
  dispatch(buffer); // flush any trailing line without a newline
}

document.addEventListener("DOMContentLoaded", () => {
  // Settings panel
  const settingsToggle = document.getElementById("settings-toggle");
  const settingsPanel = document.getElementById("settings-panel");
  settingsToggle.addEventListener("click", () => {
    const isOpen = settingsPanel.classList.toggle("open");
    settingsToggle.setAttribute("aria-expanded", String(isOpen));
  });
  document.addEventListener("click", (e) => {
    if (!settingsToggle.contains(e.target) && !settingsPanel.contains(e.target)) {
      settingsPanel.classList.remove("open");
      settingsToggle.setAttribute("aria-expanded", "false");
    }
  });

  // ClassyFire availability: lock the toggle and wire the down-modal
  initClassyfireStatus();
  const cfModal = document.getElementById("cf-down-modal");
  document.getElementById("cf-modal-close").addEventListener("click", hideCfDownModal);
  cfModal.addEventListener("click", (e) => {
    if (e.target === cfModal) hideCfDownModal(); // click on the backdrop
  });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape") hideCfDownModal();
  });

  // Pagination buttons (set up once, always reference current allData/currentPage)
  document.getElementById("prev-page").addEventListener("click", () => {
    goToPage(currentPage - 1);
  });
  document.getElementById("next-page").addEventListener("click", () => {
    goToPage(currentPage + 1);
  });

  // Results-per-page dropdown
  document.getElementById("page-size-select").addEventListener("change", (e) => {
    pageSize = parseInt(e.target.value, 10);
    currentPage = 1;
    if (allData.length > 0) renderPage();
  });

  // Result filter menu, opened like the settings panel
  const filterToggle = document.getElementById("filter-toggle");
  const filterPanel = document.getElementById("filter-panel");
  filterToggle.addEventListener("click", () => {
    const isOpen = filterPanel.classList.toggle("open");
    filterToggle.setAttribute("aria-expanded", String(isOpen));
  });
  document.addEventListener("click", (e) => {
    if (!filterToggle.contains(e.target) && !filterPanel.contains(e.target)) closeFilterPanel();
  });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape") closeFilterPanel();
  });
  filterPanel.querySelectorAll(".filter-option").forEach(btn => {
    btn.addEventListener("click", () => {
      setFilter(btn.dataset.filter);
      closeFilterPanel();
    });
  });
  // Download buttons (set up once, always reference current allData)
  document.getElementById("download-csv").addEventListener("click", () => {
    const hasClassyfire = allData.some(r => r.matches && r.matches.some(m => m.classyfire));
    let csv = "query,query_type,converted_query,found_match,match_level,error_message,pubchem_cid,inchikey,inchi,smiles,compound_name,molecular_formula,exact_mass,literature_count,patent_count,annotation_type_count";
    if (hasClassyfire) {
      csv += ",classyfire_kingdom,classyfire_superclass,classyfire_class,classyfire_subclass,classyfire_direct_parent,classyfire_description,classyfire_error";
    }
    csv += "\n";
    allData.forEach(result => {
      if (result.matches && result.matches.length > 0) {
        result.matches.forEach(match => {
          const cf = match.classyfire || {};
          const row = [
            csvField(result.query),
            csvField(result.query_type),
            csvField(result.converted_query),
            csvField(result.found_match),
            csvField(result.match_level),
            csvField(result.error_message),
            csvField(match.identifier),
            csvField(match.inchikey),
            csvField(match.inchi),
            csvField(match.smiles),
            csvField(match.compound_name),
            csvField(match.molecular_formula),
            csvField(match.exact_mass),
            csvField(match.literature_count),
            csvField(match.patent_count),
            csvField(match.annotation_type_count)
          ];
          if (hasClassyfire) {
            row.push(csvField(cf.kingdom), csvField(cf.superclass), csvField(cf.class),
                     csvField(cf.subclass), csvField(cf.direct_parent), csvField(cf.description), csvField(cf.error));
          }
          csv += row.join(",") + "\n";
        });
      } else {
        const row = [
          csvField(result.query), csvField(result.query_type), csvField(result.converted_query), csvField(result.found_match),
          csvField(""), csvField(result.error_message),
          csvField(""), csvField(""), csvField(""), csvField(""), csvField(""), csvField(""), csvField(""), csvField(""), csvField(""), csvField("")
        ];
        if (hasClassyfire) {
          row.push(csvField(""), csvField(""), csvField(""), csvField(""), csvField(""), csvField(""), csvField(""));
        }
        csv += row.join(",") + "\n";
      }
    });
    triggerDownload("data:text/csv;charset=utf-8," + encodeURIComponent(csv), `ctsl_${timestamp()}.csv`);
  });

  document.getElementById("download-json").addEventListener("click", () => {
    triggerDownload(
      "data:text/json;charset=utf-8," + encodeURIComponent(JSON.stringify(allData, null, 2)),
      `ctsl_${timestamp()}.json`
    );
  });

  // Form submission
  const form = document.getElementById("query-form");
  const input = document.getElementById("query-input");
  const output = document.getElementById("output-text");
  const outputLabel = document.getElementById("output-label");
  const appliedSettingsLabel = document.getElementById("applied-settings-label");
  const downloadButtons = document.getElementById("download-buttons");
  const paginationControls = document.getElementById("pagination-controls");
  const submitButton = form.querySelector("button[type='submit']");

  form.addEventListener("submit", async (event) => {
    event.preventDefault();

    const query = input.value.trim();
    document.getElementById("output-container").classList.add("visible");
    downloadButtons.style.display = "none";
    setDownloadsLoading(false);
    hideCfProgress();
    showPcbProgress(0, 0);
    paginationControls.style.display = "none";
    document.getElementById("result-filters").hidden = true;
    setFilterChip("all");

    // Bare quotes are dropped by the API, so a query of only those counts as blank
    const isBareQuote = (t) => t === '"' || t === '""' || t === '"""';
    const hasQuery = query.split(/\s+/).some((t) => t && !isBareQuote(t));

    if (!hasQuery) {
      outputLabel.textContent = "Error";
      appliedSettingsLabel.style.display = "none";
      output.textContent = "Please enter a query";
      return;
    }

    const maxQueryLength = 100000;
    const queryCount = query.trim().split(/\s+/).filter(Boolean).length;
    if (queryCount > maxQueryLength) {
      outputLabel.textContent = "Error";
      appliedSettingsLabel.style.display = "none";
      output.textContent = `Query contains ${queryCount.toLocaleString()} identifiers - please limit to ${maxQueryLength.toLocaleString()} per submission`;
      return;
    }

    // ClassyFire is rate limited, max 1000 queries
    if (document.getElementById("classyfire-enabled").checked && queryCount > 1000) {
      outputLabel.textContent = "Error";
      appliedSettingsLabel.style.display = "none";
      output.innerHTML = `ClassyFire is enabled - please <strong>limit to 1,000 identifiers</strong> per submission (currently ${queryCount.toLocaleString()}).<br><br>Non-ClassyFire submissions may contain up to 100,000 identifiers.`;
      return;
    }

    output.innerHTML = "<span class='ellipsis-animate'>Matching</span>";
    outputLabel.textContent = "Results";
    appliedSettingsLabel.style.display = "none";

    const topHitOnly = document.getElementById("top-hit-only").checked;
    const firstBlockMatches = document.getElementById("first-block-matches").checked;
    const classyfireEnabled = document.getElementById("classyfire-enabled").checked;
    classyfireRequested = classyfireEnabled; // snapshot for displayResults (survives later toggling from the settings panel)
    let url = "/match?";
    if (!topHitOnly) {
      url += "&top_hit_only=false";
    }
    if (!firstBlockMatches) {
      url += "&first_block_matches=false";
    }
    if (classyfireEnabled) {
      url += "&classyfire=true";
    }

    url += "&stream=true"; // Always stream frontend results for pubchem fallback

    if (activeController) {
      console.log("New request received. Aborting previous request.");
      activeController.abort();
    }
    submitButton.disabled = true;

    const controller = new AbortController();
    activeController = controller;
    const signal = controller.signal;

    const slowTimer = setTimeout(() => {
      if (output.querySelector(".ellipsis-animate")) {
        const slowNote = classyfireEnabled
          ? "<div class='doc-note'>You have <strong>ClassyFire</strong> classification enabled, which increases latency significantly. You can expect ~3s per query.</div>"
          : "<div class='doc-note'><strong>Sorry</strong>, this is taking longer than usual. Hold tight!</div>";
        output.innerHTML += slowNote;
      }
    }, 5000);

    const updateHeader = () => {
      const numMatches = countNumMatches(allData);
      outputLabel.innerHTML = `Results &mdash; ${numMatches} / ${allData.length} ${allData.length === 1 ? "match" : "matches"}`;
      const topHitText = topHitOnly ? "Top Hit Only" : "All Hits";
      const firstBlockText = firstBlockMatches ? "First Block Matches" : "Exact Matches Only";
      const classyfireText = classyfireEnabled ? ", ClassyFire" : "";
      appliedSettingsLabel.title = "applied settings";
      appliedSettingsLabel.innerHTML = `<img src="assets/settings-icon.svg" alt="" width="14" height="14" style="vertical-align:middle;margin-right:4px;">${topHitText}, ${firstBlockText}${classyfireText}`;
      appliedSettingsLabel.style.display = "block";
    };

    try {
      const response = await fetch(url, {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-CTSL-Client": "frontend" },
        body: JSON.stringify({ queries: query }),
        signal,
      });

      if (!response.ok) {
        outputLabel.textContent = "Error";
        throw new Error(`Server returned ${response.status} - ${await response.text()}`);
      }

      // A server without flushing support falls back to a plain JSON array
      if ((response.headers.get("Content-Type") || "").includes("application/x-ndjson")) {
        clearTimeout(slowTimer);

        let inchikeyIndex = new Map(); // inchikey -> [match, ...] for O(1) live updates
        const cfInfo = new Map(); // inchikey -> info already streamed, for PubChem rows that arrive later
        let totalUnique = 0;
        let classified = 0;
        let pcbTotal = 0;
        let pcbDone = 0;
        let rafScheduled = false;
        const scheduleRender = () => {
          if (rafScheduled) return;
          rafScheduled = true;
          requestAnimationFrame(() => { rafScheduled = false; renderPage(); updateHeader(); });
        };
        const refreshCfProgress = () => showCfProgress(classified, totalUnique, pcbDone < pcbTotal);
        const indexMatches = (r) => (r.matches || []).forEach(m => {
          // Matches beyond the top hit cap already carry a note; don't overwrite them
          if (m.classyfire) return;
          // A key the stream already classified for another row won't get a second line
          if (cfInfo.has(m.inchikey)) { m.classyfire = cfInfo.get(m.inchikey); return; }
          if (!inchikeyIndex.has(m.inchikey)) inchikeyIndex.set(m.inchikey, []);
          inchikeyIndex.get(m.inchikey).push(m);
        });

        await consumeMatchStream(response, {
          onMatches: (msg) => {
            allData = msg.results || [];
            currentPage = 1;
            output.textContent = "";
            downloadButtons.style.display = "flex";
            setDownloadsLoading(true);
            pcbTotal = allData.filter(r => r.pending).length;
            showPcbProgress(0, pcbTotal);
            if (classyfireEnabled) {
              allData.forEach(indexMatches);
              totalUnique = msg.unique || 0;
              refreshCfProgress();
              updateCfQueue(msg.queue || 0);
            }
            renderPage();
            updateHeader();
          },
          onPubchem: (msg) => {
            allData[msg.index] = msg.result;
            pcbDone++;
            showPcbProgress(pcbDone, pcbTotal);
            if (classyfireEnabled) {
              indexMatches(msg.result);
              // The last settled row makes the total exact even when it did not change
              refreshCfProgress();
            }
            scheduleRender();
          },
          // The total is an upper bound while PubChem rows are pending and only shrinks
          onClassifying: (msg) => {
            totalUnique = msg.unique || 0;
            refreshCfProgress();
            updateCfQueue(msg.queue || 0);
          },
          onClassyfire: (msg) => {
            cfInfo.set(msg.inchikey, msg.info);
            const matches = inchikeyIndex.get(msg.inchikey);
            if (matches) matches.forEach(m => { m.classyfire = msg.info; });
            inchikeyIndex.delete(msg.inchikey);
            classified++;
            refreshCfProgress();
            updateCfQueue(msg.queue || 0);
            scheduleRender();
          },
          onDone: () => {
            renderPage();
            updateHeader();
            hideCfProgress();
            showPcbProgress(0, 0);
            setDownloadsLoading(false);
          },
        });
      } else {
        // Non-streaming path, a single JSON array
        allData = await response.json();
        currentPage = 1;
        clearTimeout(slowTimer);

        output.textContent = "";
        downloadButtons.style.display = "flex";
        renderPage();
        updateHeader();
      }

    } catch (err) {
      clearTimeout(slowTimer);
      hideCfProgress();
      showPcbProgress(0, 0);
      setDownloadsLoading(false);
      if (err.name !== "AbortError") {
        output.textContent = `Error: ${err.message}`;
      }
    } finally {
      if (activeController === controller) activeController = null;
      submitButton.disabled = false;
    }
  });
});

function goToPage(n) {
  const paginationEl = document.getElementById("pagination-controls");
  const before = paginationEl.getBoundingClientRect().bottom;
  currentPage = n;
  renderPage();
  const after = paginationEl.getBoundingClientRect().bottom;
  window.scrollBy(0, after - before);
}

function closeFilterPanel() {
  document.getElementById("filter-panel").classList.remove("open");
  document.getElementById("filter-toggle").setAttribute("aria-expanded", "false");
}

// Mark a menu option as the active filter without rendering
function setFilterChip(filter) {
  activeFilter = filter;
  let activeName = "";
  document.querySelectorAll("#filter-panel .filter-option").forEach(btn => {
    const active = btn.dataset.filter === filter;
    btn.classList.toggle("active", active);
    btn.setAttribute("aria-pressed", String(active));
    if (active) activeName = btn.querySelector(".filter-name").textContent;
  });
  const toggle = document.getElementById("filter-toggle");
  const label = filter === "all" ? "Filter results" : `Filter results (showing ${activeName})`;
  toggle.classList.toggle("filtered", filter !== "all");
  toggle.dataset.filter = filter;
  toggle.title = label;
  toggle.setAttribute("aria-label", label);
}

function setFilter(filter) {
  setFilterChip(filter);
  currentPage = 1;
  renderPage();
}

// Results under the active filter, each paired with its index in allData so query numbers stay stable.
function filteredResults() {
  const entries = allData.map((result, i) => ({ result, i }));
  if (activeFilter === "all") return entries;
  return entries.filter(e => getMatchStatus(e.result).className === activeFilter);
}

// Refresh the count on every menu option and show the filter button once there are results
function updateFilterCounts() {
  const counts = { all: allData.length };
  allData.forEach(r => {
    const cls = getMatchStatus(r).className;
    counts[cls] = (counts[cls] || 0) + 1;
  });
  const wrap = document.getElementById("result-filters");
  wrap.hidden = allData.length === 0;
  wrap.querySelectorAll(".filter-option").forEach(btn => {
    btn.querySelector(".filter-count").textContent = counts[btn.dataset.filter] || 0;
  });
}

function renderPage() {
  const output = document.getElementById("output-text");
  output.textContent = "";
  const rows = filteredResults();
  const start = (currentPage - 1) * pageSize;
  if (rows.length === 0 && allData.length > 0) {
    output.innerHTML = "<div class='filter-empty'>No results for this filter &mdash; <button type='button' class='filter-empty-clear'>Clear filter</button></div>";
    output.querySelector(".filter-empty-clear").addEventListener("click", () => setFilter("all"));
  } else {
    displayResults(rows.slice(start, start + pageSize), output);
  }
  updateFilterCounts();
  updatePagination(rows.length);
}

function updatePagination(count) {
  const totalPages = Math.ceil(count / pageSize);
  const controls = document.getElementById("pagination-controls");

  if (totalPages <= 1) {
    controls.style.display = "none";
    return;
  }

  document.getElementById("prev-page").disabled = currentPage <= 1;
  document.getElementById("next-page").disabled = currentPage >= totalPages;

  const pageInfo = document.getElementById("page-info");
  pageInfo.innerHTML = "";
  getPaginationRange(currentPage, totalPages).forEach(p => {
    if (p === "...") {
      const ellipsis = document.createElement("span");
      ellipsis.textContent = "…";
      ellipsis.className = "page-ellipsis";
      pageInfo.appendChild(ellipsis);
    } else {
      const btn = document.createElement("button");
      btn.type = "button";
      btn.textContent = p;
      btn.className = "pagination-btn page-num-btn" + (p === currentPage ? " active" : "");
      btn.disabled = p === currentPage;
      btn.addEventListener("click", () => { goToPage(p); });
      pageInfo.appendChild(btn);
    }
  });

  controls.style.display = "flex";
}

function getPaginationRange(current, total) {
  if (total <= 7) {
    return Array.from({ length: total }, (_, i) => i + 1);
  }
  // Always returns exactly 7 slots so the pagination bar never changes width.
  if (current <= 4)        return [1, 2, 3, 4, 5, "...", total];
  if (current >= total - 3) return [1, "...", total-4, total-3, total-2, total-1, total];
  return [1, "...", current - 1, current, current + 1, "...", total];
}

// data holds { result, i } entries from filteredResults, i being the index in allData
function displayResults(data, outputElement) {
  data.forEach(({ result, i: queryIndex }, index) => {
    // A pending row still carries the local miss message, which the PubChem answer will replace
    const errorHtml = result.error_message && !result.pending
      ? `<div class="error-message">${escapeHtml(result.error_message).replace("see documentation", '<a href="/docs#query-types" target="_blank">see documentation</a>')}</div>`
      : "";

    const matchesHtml = (result.matches && result.matches.length > 0)
      ? `<div class="matches-section">${result.matches.map((match, i) => `
          ${i > 0 ? "<br>" : ""}
          <div class="match-item">
            <div class="match-header">
              MATCH${result.matches.length > 1 ? ` ${i + 1}` : ""} &mdash;
              <strong><a href="https://pubchem.ncbi.nlm.nih.gov/compound/${match.identifier}" target="_blank" style="text-decoration:underline;color:#1a3e68">${escapeHtml(match.compound_name || "Unnamed Compound").toUpperCase()}</a></strong>
            </div>
            <hr>
            <div class="match-details">
              <div class="match-field"><label>PubChem CID:</label><span class="monospace">${escapeHtml(match.identifier)}</span></div>
              <div class="match-field"><label>InChIKey:</label><span class="monospace">${escapeHtml(match.inchikey)}</span></div>
              <div class="match-field"><label>InChI:</label><span class="monospace small-text">${escapeHtml(match.inchi)}</span></div>
              <div class="match-field"><label>SMILES:</label><span class="monospace small-text">${escapeHtml(match.smiles)}</span></div>
              <div class="match-field"><label>Compound Name:</label><span class="monospace small-text">${escapeHtml(match.compound_name)}</span></div>
              <div class="match-field"><label>Mol. Formula:</label><span class="monospace">${escapeHtml(match.molecular_formula)}</span></div>
              <div class="match-field"><label>Exact Mass:</label><span class="monospace">${match.exact_mass}</span></div>
              <div class="match-field"><label>Literature Count:</label><span class="monospace">${match.literature_count}</span></div>
              <div class="match-field"><label>Patent Count:</label><span class="monospace">${match.patent_count}</span></div>
              <div class="match-field"><label>Anno. Type Count:</label><span class="monospace">${match.annotation_type_count}</span></div>
              ${classyfireRequested ? `
              <div class="match-field classyfire-heading"><label>Chemical Classification</label></div>
              ${!match.classyfire ? `<div class="match-field cf-queued"><span>Queued</span><span class="inline-spinner" aria-hidden="true"></span></div>`
              : match.classyfire.error ? `<div class="match-field"><label>${match.classyfire.error.startsWith("Only the top") ? "Skipped:" : "Error:"}</label><span class="monospace small-text" style="word-break: normal;">${escapeHtml(match.classyfire.error)}</span></div>`
              : (match.classyfire.kingdom || match.classyfire.superclass || match.classyfire.class || match.classyfire.subclass || match.classyfire.direct_parent || match.classyfire.description) ? `
              ${match.classyfire.kingdom ? `<div class="match-field"><label>Kingdom:</label><span class="monospace">${escapeHtml(match.classyfire.kingdom)}</span></div>` : ""}
              ${match.classyfire.superclass ? `<div class="match-field"><label>Superclass:</label><span class="monospace">${escapeHtml(match.classyfire.superclass)}</span></div>` : ""}
              ${match.classyfire.class ? `<div class="match-field"><label>Class:</label><span class="monospace">${escapeHtml(match.classyfire.class)}</span></div>` : ""}
              ${match.classyfire.subclass ? `<div class="match-field"><label>Subclass:</label><span class="monospace">${escapeHtml(match.classyfire.subclass)}</span></div>` : ""}
              ${match.classyfire.direct_parent ? `<div class="match-field"><label>Direct Parent:</label><span class="monospace">${escapeHtml(match.classyfire.direct_parent)}</span></div>` : ""}
              ${match.classyfire.description ? `<div class="match-field"><label>Description:</label><span class="small-text" style="word-break: normal; overflow-wrap: anywhere;">${escapeHtml(match.classyfire.description)}</span></div>` : ""}
              ` : `<div class="match-field"><span class="monospace small-text">No classification found</span></div>`}
              ` : ""}
            </div>
          </div>`).join("")}
        </div>`
      : "";

    const isConverted = (result.query_type === "converted_smiles" || result.query_type === "converted_inchi") && result.converted_query;
    const transId = `trans-${queryIndex}`;

    const queryTypeBubble = isConverted
      ? `<div class="query-type-expandable-wrapper" title="Converted with RDKit"><button type="button" class="query-type-expandable-btn" aria-expanded="false" aria-controls="${transId}">Type: ${formatQueryType(escapeHtml(result.query_type))}<span class="query-type-chevron" aria-hidden="true"><img src="assets/chevron-icon.svg" alt=""></span></button><div id="${transId}" class="query-type-conversion" hidden>to InChIKey:<br>${escapeHtml(result.converted_query)}</div></div>`
      : `<span class="query-type">Type: ${formatQueryType(escapeHtml(result.query_type))}</span>`;

    const matchStatus = getMatchStatus(result);

    const resultDiv = document.createElement("div");
    resultDiv.className = "result-item";
    resultDiv.innerHTML = `
      <div class="query-section">
        <div class="query-header">
          <h3>Query ${queryIndex + 1}: ${escapeHtml(result.query)}</h3>
          <button type="button" class="collapse-btn" aria-label="Toggle result"><img src="assets/chevron-icon.svg" alt=""></button>
        </div>
        <div class="query-details">
          ${queryTypeBubble}
          <span class="match-status ${matchStatus.className}">${matchStatus.text}${result.pending ? '<span class="inline-spinner" aria-hidden="true"></span>' : ""}</span>
        </div>
      </div>
      <div class="result-body">
        ${errorHtml}
        ${matchesHtml}
      </div>
    `;

    resultDiv.querySelector(".collapse-btn").addEventListener("click", () => {
      resultDiv.classList.toggle("collapsed");
    });

    const expandBtn = resultDiv.querySelector(".query-type-expandable-btn");
    if (expandBtn) {
      expandBtn.addEventListener("click", () => {
        const expanded = expandBtn.getAttribute("aria-expanded") === "true";
        expandBtn.setAttribute("aria-expanded", String(!expanded));
        document.getElementById(expandBtn.getAttribute("aria-controls")).hidden = expanded;
      });
    }

    outputElement.appendChild(resultDiv);

    if (index < data.length - 1) {
      outputElement.insertAdjacentHTML("beforeend", '<hr class="result-separator">');
    }
  });
}

function escapeHtml(text) {
  if (!text) return "";
  const div = document.createElement("div");
  div.textContent = text;
  return div.innerHTML;
}

// Classifies a result once and returns both the badge text and its CSS class
function getMatchStatus(result) {
  if (result.pending) return { text: "Using PubChem Live lookup", className: "pending-lookup" };
  if (!result.found_match) return { text: "✗ No match", className: "no-match" };
  const level = (result.match_level || "").toLowerCase();
  if (level.startsWith("pubchem live:")) {
    return { text: "✓ Match Found: PubChem Live", className: "pubchem-live-match" };
  }
  if (level.includes("exact")) return { text: "✓ Match Found: Exact", className: "exact-match" };
  if (level.includes("first block")) return { text: "✓ Match Found: First Block", className: "first-block-match" };
  if (result.match_level) return { text: `✓ Match Found: ${result.match_level}`, className: "found" };
  return { text: "✓ Match Found", className: "found" };
}

function formatQueryType(queryType) {
  switch (queryType.toLowerCase()) {
    case "pubchem_id":        return "PubChem CID";
    case "inchikey":          return "InChIKey";
    case "smiles":            return "SMILES";
    case "converted_smiles":  return "Converted SMILES";
    case "inchi":             return "InChI";
    case "converted_inchi":   return "Converted InChI";
    case "formula":           return "Molecular Formula";
    case "smiles_or_formula": return "SMILES/Mol. Formula";
    case "bad_inchi":         return "Malformed InChI";
    case "bad_inchikey":      return "Malformed InChIKey";
    case "bad_pubchem_id":    return "Invalid PubChem CID";
    case "unidentified":      return "Unidentified";
    default:                  return queryType;
  }
}

function countNumMatches(data) {
  return data.filter(r => r.found_match).length;
}

function csvField(value) {
  const s = value == null ? "" : String(value);
  if (s.includes(",") || s.includes('"')) {
    return '"' + s.replace(/"/g, '""') + '"';
  }
  return s;
}

function timestamp() {
  return new Date().toISOString().slice(0, 19);
}

function triggerDownload(dataUri, filename) {
  const a = document.createElement("a");
  a.href = dataUri;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
}
