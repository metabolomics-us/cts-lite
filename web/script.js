document.addEventListener("DOMContentLoaded", () => {
  const form = document.getElementById("query-form");
  const input = document.getElementById("query-input");
  const output = document.getElementById("output");

  form.addEventListener("submit", async (event) => {
    event.preventDefault(); 

    const query = input.value.trim();
    if (!query) {
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

      output.textContent = JSON.stringify(data, null, 2);
    } catch (err) {
      output.textContent = `Error: ${err.message}`;
    }
  });
});
