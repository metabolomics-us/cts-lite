document.addEventListener("DOMContentLoaded", () => {
  const form = document.getElementById("query-form");
  const input = document.getElementById("query-input");
  const output = document.getElementById("output-text");
  const outputContainer = document.getElementById("output-container");
  const outputLabel = document.getElementById("output-label");

  form.addEventListener("submit", async (event) => {
    event.preventDefault(); 

    const query = input.value.trim();
    outputContainer.classList.add("visible");
    if (!query) {
      outputLabel.textContent = "Error";
      output.textContent = "Please enter a query";
      return;
    }

    output.textContent = "Matching...";

    try {
      const response = await fetch(`http://localhost:8080/match?q=${encodeURIComponent(query)}`);

      if (!response.ok) {
        throw new Error(`Server returned ${response.status}`);
      }

      const data = await response.json();

      // Clear previous content (gets rid of "Matching..." text)
      output.textContent = '';
      
      // Create structured display of the results
      displayResults(data, output);
      let numMatches = countNumMatches(data);
      outputLabel.innerHTML = `Results &mdash; ${numMatches} / ${data.length} ${numMatches === 1 ? 'match' : 'matches'}`;

    } catch (err) {
      output.textContent = `Error: ${err.message}`;
    }
  });
});

function displayResults(data, outputElement) {
  // Handle array of results or single result
  const results = data;
  
  results.forEach((result, index) => {
    // Create container for each result
    const resultDiv = document.createElement('div');
    resultDiv.className = 'result-item';
    
    // Query info section
    const querySection = document.createElement('div');
    querySection.className = 'query-section';
    querySection.innerHTML = `
      <h3>Query ${index + 1}: ${escapeHtml(result.query)}</h3>
      <div class="query-details">
        <span class="query-type">Type: ${formatQueryType(escapeHtml(result.query_type))}</span>
        <span class="match-status ${getMatchStatusClass(result)}">
          ${getMatchStatusText(result)}
        </span>
      </div>
    `;
    
    // Error message if present
    if (result.error_message) {
      const errorDiv = document.createElement('div');
      errorDiv.className = 'error-message';
      errorDiv.textContent = result.error_message;
      querySection.appendChild(errorDiv);
    }
    
    // Matches section
    if (result.matches && result.matches.length > 0) {
      const matchesSection = document.createElement('div');
      matchesSection.className = 'matches-section';
      
      result.matches.forEach((match) => {
        const matchDiv = document.createElement('div');
        matchDiv.className = 'match-item';
        matchDiv.innerHTML = `
          <div class="match-header">
            MATCH &mdash; <strong>${escapeHtml(match.compound_name || 'Unnamed Compound').toUpperCase()}</strong>
          </div>
          <hr>
          <div class="match-details">
            <div class="match-field">
              <label>InChIKey:</label>
              <span class="monospace">${escapeHtml(match.inchikey)}</span>
            </div>
            <div class="match-field">
              <label>First Block:</label>
              <span class="monospace">${escapeHtml(match.first_block)}</span>
            </div>
            <div class="match-field">
              <label>InChI:</label>
              <span class="monospace small-text">${escapeHtml(match.inchi)}</span>
            </div>
            <div class="match-field">
              <label>SMILES:</label>
              <span class="monospace">${escapeHtml(match.smiles)}</span>
            </div>
            <div class="match-field">
              <label>Compound Name:</label>
              <span class="monospace small-text">${escapeHtml(match.compound_name)}</span>
            </div>
            <div class="match-field">
              <label>Mol. Formula:</label>
              <span class="monospace small-text">${escapeHtml(match.molecular_formula)}</span>
            </div>
            <div class="match-field">
              <label>PubMed Count:</label>
              <span class="monospace small-text">${escapeHtml(match.pubmed_count)}</span>
            </div>
            <div class="match-field">
              <label>Patent Count:</label>
              <span class="monospace small-text">${escapeHtml(match.patent_count)}</span>
            </div>
          </div>
        `;
        matchesSection.appendChild(matchDiv);
      });
      
      querySection.appendChild(matchesSection);
    }
    
    resultDiv.appendChild(querySection);
    outputElement.appendChild(resultDiv);
    
    // Add separator between multiple results
    if (index < results.length - 1) {
      const separator = document.createElement('hr');
      separator.className = 'result-separator';
      outputElement.appendChild(separator);
    }
  });
  
  // Add toggle for raw JSON view
  const toggleDiv = document.createElement('div');
  toggleDiv.className = 'json-toggle-container';
  toggleDiv.innerHTML = `
    <button id="json-toggle" class="json-toggle-btn">Show Raw JSON</button>
    <pre id="raw-json" class="raw-json hidden">${JSON.stringify(data, null, 2)}</pre>
  `;
  outputElement.appendChild(toggleDiv);
  
  // Add event listener for JSON toggle
  document.getElementById('json-toggle').addEventListener('click', () => {
    const rawJson = document.getElementById('raw-json');
    const toggleBtn = document.getElementById('json-toggle');
    
    if (rawJson.classList.contains('hidden')) {
      rawJson.classList.remove('hidden');
      toggleBtn.textContent = 'Hide Raw JSON';
    } else {
      rawJson.classList.add('hidden');
      toggleBtn.textContent = 'Show Raw JSON';
    }
  });

  // TODO: Only show copy button when json raw is toggled, and keep it at the top above the raw JSON
  const copyBtn = document.createElement('button');
  copyBtn.className = 'json-toggle-btn';
  copyBtn.textContent = 'Copy JSON';
  copyBtn.addEventListener('click', () => {
    const rawJson = document.getElementById('raw-json');
    navigator.clipboard.writeText(rawJson.textContent).then(() => {
      alert('JSON copied to clipboard');
    });
  });
  toggleDiv.appendChild(copyBtn);

}

function escapeHtml(text) {
  if (!text) return '';
  const div = document.createElement('div');
  div.textContent = text;
  return div.innerHTML;
}

function getMatchLevelClass(matchLevel) {
  if (!matchLevel) return '';
  
  const level = matchLevel.toLowerCase();
  
  if (level.includes('exact')) {
    return 'exact';
  } else if (level.includes('first block')) {
    return 'first-block';
  } else {
    // Default class for any other match levels
    return 'other';
  }
}

function getMatchStatusText(result) {
  if (!result.found_match) {
    return '✗ No match';
  }
  
  if (result.match_level) {
    const level = result.match_level.toLowerCase();
    if (level.includes('exact')) {
      return '✓ Match Found: Exact';
    } else if (level.includes('first block')) {
      return '✓ Match Found: First Block';
    } else {
      return `✓ Match Found: ${result.match_level}`;
    }
  }
  
  return '✓ Match Found';
}

function getMatchStatusClass(result) {
  if (!result.found_match) {
    return 'no-match';
  }
  
  if (result.match_level) {
    const level = result.match_level.toLowerCase();
    if (level.includes('exact')) {
      return 'exact-match';
    } else if (level.includes('first block')) {
      return 'first-block-match';
    }
  }
  
  return 'found';
}

function formatQueryType(queryType) {
  switch (queryType.toLowerCase()) {
    case 'inchikey':
      return 'InChIKey';
    case 'smiles':
      return 'SMILES';
    case 'inchi':
      return 'InChI';
    case 'bad_inchi':
      return 'Malformed InChI';
    case 'bad_inchikey':
      return 'Malformed InChIKey';
    case 'unidentified':
      return 'Unidentified';
    default:
      return queryType;
  }
}

function countNumMatches(data) {
  let count = 0;
  data.forEach(result => {
    if (result.found_match) {
      count += 1;
    }
  });
  return count;
}