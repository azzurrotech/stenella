Stenella – Combined RSS AggregatorA lightweight Go server that aggregates multiple RSS feeds, presents them in a clean web UI, and lets you add or remove feeds on‑the‑fly. It uses only the Go standard library and plain HTML/CSS/JavaScript, making it easy to run anywhere without external dependencies.Table of Contents

Features
Prerequisites
Installation & Running
Configuration
API Endpoints
Extending the Server
License
Contact & Support


Features

Aggregates multiple RSS feeds into a single, chronologically sorted view.
Dynamic UI to add or remove RSS sources without restarting the server.
JSON API for feed items and source management.
Thread‑safe in‑memory source list (easy to swap for persistent storage).
Plug‑in style extra handlers – add custom routes with minimal code.
No third‑party libraries – pure Go standard library + vanilla web tech.


Prerequisites

Go 1.22 or newer installed (download Go).
Internet connectivity for fetching RSS feeds.


Installation & Running
# Clone the repository (or copy stenella.go into a folder)
git clone https://github.com/azzurro-tech/stenella.git
cd stenella

# Run the server
go run stenella.go
The server starts on http://localhost:8080. Open that URL in any modern browser.

Configuration
Edit the feedSources slice near the top of stenella.go to pre‑populate default RSS URLs:
var (
    feedSources = []string{
        "https://news.ycombinator.com/rss",
        "https://www.reddit.com/r/golang/.rss",
        // Add more URLs here
    }
)
Changes take effect after restarting the server, unless you add/remove feeds via the UI (which updates the in‑memory list at runtime).

API Endpoints
MethodPathDescriptionGET/Serves the HTML UI.GET/api/feedsReturns merged feed items as JSON.GET/api/sourcesReturns the current list of RSS URLs.POST/api/sources/addBody { "url": "<RSS_URL>" } – adds a source.POST/api/sources/removeBody { "url": "<RSS_URL>" } – removes a source.
All responses are JSON (except the root UI). Errors are reported with appropriate HTTP status codes.

Extending the Server
To add custom routes, define a handler function and register it in the extraHandlers slice:
func helloHandler(w http.ResponseWriter, r *http.Request) {
    fmt.Fprintln(w, "👋 Hello from a custom endpoint!")
}

var extraHandlers = []extraHandler{
    {Pattern: "/hello", Handler: helloHandler},
}
Re‑run the server and the new endpoint will be available at http://localhost:8080/hello.

License
MIT License

Copyright (c) 2025 Azzurro Technology Inc

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in
all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL
THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING
FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER
DEALINGS IN THE SOFTWARE.


Contact & Support
Azzurro Technology Inc
📧 Email: info@azzurro.tech
Feel free to reach out for bug reports, feature requests, or licensing questions. Happy coding!