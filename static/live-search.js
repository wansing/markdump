// reuse xhr, so livesearch() can abort it
var xhr = new XMLHttpRequest();

function livesearch() {
	// clear live search results
	let resultDiv = document.getElementById("live-search-result");
	resultDiv.innerHTML = "";

	// hide regular results
	let formSearchResult = document.getElementById("form-search-result");
	if(formSearchResult) {
		formSearchResult.style.display = "none";
	}

	// check input
	let input = document.getElementById("search").value.trim();
	if(input.length < 1) {
		// restore regular results
		if(formSearchResult) {
			formSearchResult.style.display = "block";
		}
		return
	}

	// send query
	xhr.abort();
	xhr.onreadystatechange = function() {
		if (this.readyState == 4 && this.status == 200) {
			resultDiv.insertAdjacentHTML("beforeend", `<h1>Search Results</h1>`);
			let result = JSON.parse(xhr.response);
			if(result != null && result.length > 0) {
				let dl = resultDiv.insertAdjacentElement("beforeend", document.createElement("dl"));
				for(const match of result) {
					dl.insertAdjacentHTML("beforeend", `<dt><a href="${match.href}"><strong>${match.path}${match.name}</strong></a></dt>`);
					if(match.content) {
						dl.insertAdjacentHTML("beforeend", `<dd>${match.content}</dd>`);
					}
				}
			} else {
				resultDiv.insertAdjacentHTML("beforeend", "<p>No search results.</p>");
			}
		}
	};
	xhr.open("GET", "/search/" + encodeURIComponent(input));
	xhr.send(null);
}
