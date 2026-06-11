let allData = [];
let currentPage = 1;
let pageSize = 10;
let activeController = null;
let classyfireRequested = false; // did this request use ClassyFire? classyfireEnabled tracks current checkbox state

// Disable download buttons while ClassyFire is running
function setDownloadsLoading(loading) {
  const buttons = document.getElementById("download-buttons");
  if (!buttons) return;
  buttons.classList.toggle("loading", loading);
  buttons.querySelectorAll(".download-btn").forEach(b => { b.disabled = loading; });
}

// Update the ClassyFire progress bar
function showCfProgress(done, total) {
  const wrap = document.getElementById("cf-progress");
  if (!wrap) return;
  if (total <= 0) { wrap.hidden = true; return; }
  wrap.hidden = false;
  const pct = Math.round((done / total) * 100);
  wrap.querySelector(".cf-progress-fill").style.width = pct + "%";
  wrap.querySelector(".cf-progress-label").textContent =
    `Classifying with ClassyFire… ${done} / ${total}`;
}

// Hide the ClassyFire progress bar (i.e. when classification is done)
function hideCfProgress() {
  const wrap = document.getElementById("cf-progress");
  if (wrap) wrap.hidden = true;
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

// Read NDJSON stream, dispatching each message by type
async function consumeMatchStream(response, { onMatches, onClassyfire, onDone }) {
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  const dispatch = (line) => {
    const text = line.trim();
    if (!text) return;
    const msg = JSON.parse(text);
    if (msg.type === "matches") onMatches(msg);
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

  // Download buttons (set up once, always reference current allData)
  document.getElementById("download-csv").addEventListener("click", () => {
    const hasClassyfire = allData.some(r => r.matches && r.matches.some(m => m.classyfire));
    let csv = "query,query_type,found_match,match_level,error_message,pubchem_cid,inchikey,inchi,smiles,compound_name,molecular_formula,exact_mass,literature_count,patent_count";
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
            csvField(match.patent_count)
          ];
          if (hasClassyfire) {
            row.push(csvField(cf.kingdom), csvField(cf.superclass), csvField(cf.class),
                     csvField(cf.subclass), csvField(cf.direct_parent), csvField(cf.description), csvField(cf.error));
          }
          csv += row.join(",") + "\n";
        });
      } else {
        const row = [
          csvField(result.query), csvField(result.query_type), csvField(result.found_match),
          csvField(""), csvField(result.error_message),
          csvField(""), csvField(""), csvField(""), csvField(""), csvField(""), csvField(""), csvField(""), csvField(""), csvField("")
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
    paginationControls.style.display = "none";

    if (!query) {
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

    // ClassyFire is rate limited, max 100 queries
    if (document.getElementById("classyfire-enabled").checked && queryCount > 100) {
      outputLabel.textContent = "Error";
      appliedSettingsLabel.style.display = "none";
      output.innerHTML = `ClassyFire is enabled - please <strong>limit to 100 identifiers</strong> per submission (currently ${queryCount.toLocaleString()}).<br><br>Non-ClassyFire submissions may contain up to 100,000 identifiers.`;
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
      url += "&classyfire=true&stream=true";
    }

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
          : "<div class='doc-note'><strong>Sorry</strong>, this is taking longer than usual. This can be expected when querying ~100,000 entries.</div>";
        output.innerHTML += slowNote;
      }
    }, 5000);

    const finalizeHeader = () => {
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
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ queries: query }),
        signal,
      });

      if (!response.ok) {
        outputLabel.textContent = "Error";
        throw new Error(`Server returned ${response.status} - ${await response.text()}`);
      }

      if (classyfireEnabled) {
        clearTimeout(slowTimer);

        let inchikeyIndex = new Map(); // inchikey -> [match, ...] for O(1) live updates
        let totalUnique = 0;
        let classified = 0;
        let rafScheduled = false;
        const scheduleRender = () => {
          if (rafScheduled) return;
          rafScheduled = true;
          requestAnimationFrame(() => { rafScheduled = false; renderPage(); });
        };

        await consumeMatchStream(response, {
          onMatches: (msg) => {
            allData = msg.results || [];
            totalUnique = msg.unique || 0;
            classified = 0;
            currentPage = 1;
            inchikeyIndex = new Map();
            allData.forEach(r => (r.matches || []).forEach(m => {
              // Matches beyond the top three cap already carry a note; don't queue
              // them for live updates or a streamed line would overwrite it
              if (m.classyfire) return;
              if (!inchikeyIndex.has(m.inchikey)) inchikeyIndex.set(m.inchikey, []);
              inchikeyIndex.get(m.inchikey).push(m);
            }));
            output.textContent = "";
            downloadButtons.style.display = "flex";
            setDownloadsLoading(true);
            renderPage();
            finalizeHeader();
            if (totalUnique > 0) showCfProgress(0, totalUnique);
            updateCfQueue(msg.queue || 0);
          },
          onClassyfire: (msg) => {
            const matches = inchikeyIndex.get(msg.inchikey);
            if (matches) matches.forEach(m => { m.classyfire = msg.info; });
            classified++;
            showCfProgress(classified, totalUnique);
            updateCfQueue(msg.queue || 0);
            scheduleRender();
          },
          onDone: () => {
            renderPage();
            hideCfProgress();
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
        finalizeHeader();
      }

    } catch (err) {
      clearTimeout(slowTimer);
      hideCfProgress();
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

function renderPage() {
  const output = document.getElementById("output-text");
  output.textContent = "";
  const start = (currentPage - 1) * pageSize;
  displayResults(allData.slice(start, start + pageSize), output, start);
  updatePagination();
}

function updatePagination() {
  const totalPages = Math.ceil(allData.length / pageSize);
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

function displayResults(data, outputElement, offset = 0) {
  data.forEach((result, index) => {
    const errorHtml = result.error_message
      ? `<div class="error-message">${escapeHtml(result.error_message).replace("see documentation", '<a href="/docs#query-types" target="_blank">see documentation</a>')}</div>`
      : "";

    const matchesHtml = (result.matches && result.matches.length > 0)
      ? `<div class="matches-section">${result.matches.map((match, i) => `
          ${i > 0 ? "<br>" : ""}
          <div class="match-item">
            <div class="match-header">
              MATCH${result.matches.length > 1 ? ` ${i + 1}` : ""} &mdash;
              <strong><a href="https://pubchem.ncbi.nlm.nih.gov/compound/${match.identifier}#Known+Use+Information=" target="_blank" style="text-decoration:underline;color:#1a3e68">${escapeHtml(match.compound_name || "Unnamed Compound").toUpperCase()}</a></strong>
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
              ${classyfireRequested ? `
              <div class="match-field classyfire-heading"><label>Chemical Classification</label></div>
              ${!match.classyfire ? `<div class="match-field cf-queued"><span>Queued</span><span class="inline-spinner" aria-hidden="true"></span></div>`
              : match.classyfire.error ? `<div class="match-field"><label>${match.classyfire.error.startsWith("Only the top") ? "Skipped:" : "Error:"}</label><span class="monospace small-text">${escapeHtml(match.classyfire.error)}</span></div>`
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

    const resultDiv = document.createElement("div");
    resultDiv.className = "result-item";
    resultDiv.innerHTML = `
      <div class="query-section">
        <div class="query-header">
          <h3>Query ${offset + index + 1}: ${escapeHtml(result.query)}</h3>
          <button type="button" class="collapse-btn" aria-label="Toggle result"><img src="assets/chevron-icon.svg" alt=""></button>
        </div>
        <div class="query-details">
          <span class="query-type">Type: ${formatQueryType(escapeHtml(result.query_type))}</span>
          <span class="match-status ${getMatchStatusClass(result)}">${getMatchStatusText(result)}</span>
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

function getMatchStatusText(result) {
  if (!result.found_match) return "✗ No match";
  if (result.match_level) {
    const level = result.match_level.toLowerCase();
    if (level.includes("exact")) return "✓ Match Found: Exact";
    if (level.includes("first block")) return "✓ Match Found: First Block";
    return `✓ Match Found: ${result.match_level}`;
  }
  return "✓ Match Found";
}

function getMatchStatusClass(result) {
  if (!result.found_match) return "no-match";
  if (result.match_level) {
    const level = result.match_level.toLowerCase();
    if (level.includes("exact")) return "exact-match";
    if (level.includes("first block")) return "first-block-match";
  }
  return "found";
}

function formatQueryType(queryType) {
  switch (queryType.toLowerCase()) {
    case "pubchem_id":      return "PubChem CID";
    case "inchikey":        return "InChIKey";
    case "smiles":          return "SMILES";
    case "inchi":           return "InChI";
    case "formula":         return "Molecular Formula";
    case "smiles_or_formula": return "SMILES/Mol. Formula";
    case "bad_inchi":       return "Malformed InChI";
    case "bad_inchikey":    return "Malformed InChIKey";
    case "unidentified":    return "Unidentified";
    default:                return queryType;
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
