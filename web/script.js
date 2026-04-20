let allData = [];
let currentPage = 1;
let pageSize = 10;

const CHEVRON_SVG = `<svg width="14" height="14" viewBox="0 0 14 14" fill="none" xmlns="http://www.w3.org/2000/svg">
  <path d="M2 4.5L7 9.5L12 4.5" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>
</svg>`;

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
    let csv = "query,query_type,found_match,match_level,error_message,pubchem_cid,inchikey,inchi,smiles,compound_name,molecular_formula,monoisotopic_mass,literature_count,patent_count\n";
    allData.forEach(result => {
      if (result.matches && result.matches.length > 0) {
        result.matches.forEach(match => {
          csv += [
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
            csvField(match.monoisotopic_mass),
            csvField(match.literature_count),
            csvField(match.patent_count)
          ].join(",") + "\n";
        });
      } else {
        csv += [
          csvField(result.query), csvField(result.query_type), csvField(result.found_match),
          csvField(""), csvField(result.error_message),
          csvField(""), csvField(""), csvField(""), csvField(""), csvField(""), csvField(""), csvField(""), csvField(""), csvField("")
        ].join(",") + "\n";
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
  const appliedSettingsLabel = document.getElementById("top-hit-label");
  const downloadButtons = document.getElementById("download-buttons");
  const paginationControls = document.getElementById("pagination-controls");

  form.addEventListener("submit", async (event) => {
    event.preventDefault();

    const query = input.value.trim();
    document.getElementById("output-container").classList.add("visible");
    downloadButtons.style.display = "none";
    paginationControls.style.display = "none";

    if (!query) {
      outputLabel.textContent = "Error";
      appliedSettingsLabel.style.display = "none";
      output.textContent = "Please enter a query";
      return;
    }

    output.textContent = "Matching...";
    outputLabel.textContent = "Results";
    appliedSettingsLabel.style.display = "none";

    const topHitOnly = document.getElementById("top-hit-only").checked;
    const firstBlockMatches = document.getElementById("first-block-matches").checked;
    let url = "/match?";
    if (!topHitOnly) {
      url += "&top_hit_only=false";
    }
    if (!firstBlockMatches) {
      url += "&first_block_matches=false";
    }

    try {
      const response = await fetch(url, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ queries: query })
      });

      if (!response.ok) throw new Error(`Server returned ${response.status}`);

      allData = await response.json();
      currentPage = 1;

      output.textContent = "";
      downloadButtons.style.display = "flex";
      renderPage();

      const numMatches = countNumMatches(allData);
      outputLabel.innerHTML = `Results &mdash; ${numMatches} / ${allData.length} ${allData.length === 1 ? "match" : "matches"}`;
      const topHitText = topHitOnly ? "Top Hit Only" : "All Hits";
      const firstBlockText = firstBlockMatches ? "First Block Matches" : "Exact Matches Only";
      appliedSettingsLabel.title = "applied settings";
      appliedSettingsLabel.innerHTML = `<img src="assets/settings-icon.svg" alt="" width="14" height="14" style="vertical-align:middle;margin-right:4px;">${topHitText}, ${firstBlockText}`;
      appliedSettingsLabel.style.display = "block";

    } catch (err) {
      output.textContent = `Error: ${err.message}`;
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
      ? `<div class="error-message">${escapeHtml(result.error_message)}</div>`
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
              <div class="match-field"><label>Exact Mass:</label><span class="monospace">${match.monoisotopic_mass}</span></div>
              <div class="match-field"><label>Literature Count:</label><span class="monospace">${match.literature_count}</span></div>
              <div class="match-field"><label>Patent Count:</label><span class="monospace">${match.patent_count}</span></div>
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
          <button type="button" class="collapse-btn" aria-label="Toggle result">${CHEVRON_SVG}</button>
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
