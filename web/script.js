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

    // Create download buttons
    const buttonContainer = document.getElementById('download-buttons');
    buttonContainer.innerHTML = ''; // Clear any existing buttons

    if (!query) {
      outputLabel.textContent = "Error";
      output.textContent = "Please enter a query";
      return;
    }

    // Reset output texts
    output.textContent = "Matching...";
    outputLabel.textContent = "Results";

    try {
      // TODO: Change back to relative path when deploying
      // const response = await fetch(`/match?q=${encodeURIComponent(query)}`);
      const response = await fetch(`http://localhost:8080/match?q=${encodeURIComponent(query)}`);

      if (!response.ok) {
        throw new Error(`Server returned ${response.status}`);
      }

      const data = await response.json();

      // Clear previous content (gets rid of "Matching..." text)
      output.textContent = '';

      const downloadCSV = document.createElement('button');
      downloadCSV.className = 'download-btn';
      downloadCSV.type = 'button'; 
      downloadCSV.textContent = 'Download CSV';
      downloadCSV.addEventListener('click', () => {
        let csv = "";
        csv += "query,query_type,found_match,match_level,error_message,pubchem_cid,inchikey,first_block,inchi,smiles,compound_name,molecular_formula,monoisotopic_mass,pubmed_count,patent_count\n";
    
        data.forEach(result => {
          if (result.matches && result.matches.length > 0) {
            // Has matches - include each match
            result.matches.forEach(match => {
              const row = [
                result.query,
                result.query_type,
                result.found_match,
                (result.match_level || '').replace(/"/g, '""'),
                `"${(result.error_message || '').replace(/"/g, '""')}"`,
                (match.identifier || '').replace(/"/g, '""'),
                (match.inchikey || '').replace(/"/g, '""'),
                (match.first_block || '').replace(/"/g, '""'),
                `"${match.inchi.replace(/"/g, '""')}"`,
                match.smiles.replace(/"/g, '""'),
                `"${(match.compound_name || '').replace(/"/g, '""')}"`,
                (match.molecular_formula || '').replace(/"/g, '""'),
                match.monoisotopic_mass,
                match.pubmed_count,
                match.patent_count
              ];
              csv += row.join(",") + "\n";
            });
          } else {
            // No matches - include row with query info and empty match fields
            const row = [
              result.query,
              result.query_type,
              result.found_match,
              '', // match level
              `"${(result.error_message || '').replace(/"/g, '""')}"`,
              '', // pubchem_cid
              '', // inchikey
              '', // first_block
              '', // inchi
              '', // smiles
              '', // compound_name
              '', // molecular_formula
              '', // monoisotopic_mass
              '', // pubmed_count
              ''  // patent_count
            ];
            csv += row.join(",") + "\n";
          }
        });

        const encodedUri = "data:text/csv;charset=utf-8," + encodeURIComponent(csv);
        const downloadAnchorNode = document.createElement('a');
        downloadAnchorNode.setAttribute("href", encodedUri);
        downloadAnchorNode.setAttribute("download", `ctsl_${new Date().toISOString().slice(0, 19)}.csv`);
        document.body.appendChild(downloadAnchorNode); // required for firefox
        downloadAnchorNode.click();
        downloadAnchorNode.remove();
      });
      buttonContainer.appendChild(downloadCSV);

      const downloadJSON = document.createElement('button');
      downloadJSON.className = 'download-btn';
      downloadJSON.type = 'button'; 
      downloadJSON.textContent = 'Download JSON';
      downloadJSON.addEventListener('click', () => {
        const dataStr = "data:text/json;charset=utf-8," + encodeURIComponent(JSON.stringify(data, null, 2));
        const downloadAnchorNode = document.createElement('a');
        downloadAnchorNode.setAttribute("href", dataStr);
        downloadAnchorNode.setAttribute("download", `ctsl_${new Date().toISOString().slice(0, 19)}.json`);
        document.body.appendChild(downloadAnchorNode); // required for firefox
        downloadAnchorNode.click();
        downloadAnchorNode.remove();
      });
      buttonContainer.appendChild(downloadJSON);

      displayResults(data, output);
      let numMatches = countNumMatches(data);
      outputLabel.innerHTML = `Results &mdash; ${numMatches} / ${data.length} ${data.length === 1 ? 'match' : 'matches'}`;

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
      
      let i = 1; // just an index for visual
      result.matches.forEach((match) => {
        // Breakline for spacing between matches
        const breakLine = document.createElement('br');
        const matchDiv = document.createElement('div');

        matchDiv.className = 'match-item';
        matchDiv.innerHTML = `
          <div class="match-header">
            MATCH ${result.matches.length > 1 ? i : ""} &mdash; <strong><a href=https://pubchem.ncbi.nlm.nih.gov/compound/${match.identifier}#Known+Use+Information= target=_blank style="text-decoration: underline; color: #1a3e68">${escapeHtml(match.compound_name || 'Unnamed Compound').toUpperCase()}</a></strong>
          </div>
          <hr>
          <div class="match-details">
            <div class="match-field">
              <label>PubChem CID:</label>
              <span class="monospace">${escapeHtml(match.identifier)}</span>
            </div>
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
              <span class="monospace small-text">${escapeHtml(match.smiles)}</span>
            </div>
            <div class="match-field">
              <label>Compound Name:</label>
              <span class="monospace small-text">${escapeHtml(match.compound_name)}</span>
            </div>
            <div class="match-field">
              <label>Mol. Formula:</label>
              <span class="monospace">${escapeHtml(match.molecular_formula)}</span>
            </div>
            <div class="match-field">
              <label>Exact Mass:</label>
              <span class="monospace">${escapeHtml(match.monoisotopic_mass)}</span>
            </div>
            <div class="match-field">
              <label>PubMed Count:</label>
              <span class="monospace">${escapeHtml(match.pubmed_count)}</span>
            </div>
            <div class="match-field">
              <label>Patent Count:</label>
              <span class="monospace">${escapeHtml(match.patent_count)}</span>
            </div>
          </div>
        `;
        matchesSection.appendChild(matchDiv);
        if (i < result.matches.length) {
          matchesSection.appendChild(breakLine);
        }

        i++;
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