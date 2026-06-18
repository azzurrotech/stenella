/* ==============================================================
   Shepherd – utility to read / write the stack from the URL query string
   ============================================================== */
function getStackFromUrl() {
    const p = new URLSearchParams(window.location.search);
    const raw = p.get('stack');
    return raw ? raw.split(',').map(decodeURIComponent) : [];
}
function setStackToUrl(stack) {
    const p = new URLSearchParams();
    if (stack.length) {
        const enc = stack.map(encodeURIComponent).join(',');
        p.set('stack', enc);
    }
    const newUrl = `${window.location.pathname}?${p.toString()}`;
    window.history.replaceState(null, '', newUrl);
}

/* ==============================================================
   Global state for Shepherd
   ============================================================== */
let lastSelectedProject = null;   // URL of the project whose notices are shown
let cachedRss = null;             // memoised RSS feed for the session

/* ==============================================================
   Rendering helpers
   ============================================================== */
const projectsUl = document.getElementById('projects');
const resultsDiv = document.getElementById('results');
const refreshBtn = document.getElementById('refresh-btn');

function renderProjectList(stack) {
    projectsUl.innerHTML = '';
    stack.forEach((url, idx) => {
        const li = document.createElement('li');
        li.textContent = url;
        li.dataset.idx = idx;
        li.addEventListener('click', () => {
            lastSelectedProject = url;
            refreshBtn.disabled = false;
            loadVulnerabilities(url);
        });
        projectsUl.appendChild(li);
    });
}

/* ==============================================================
   Centralised “add project” routine (used by all UI entry points)
   ============================================================== */
function addProject(url) {
    const stack = getStackFromUrl();
    if (!stack.includes(url)) {
        stack.push(url);
        setStackToUrl(stack);
        renderProjectList(stack);
    }
}

/* ==============================================================
   Manual project entry (form)
   ============================================================== */
document.getElementById('project-form')
        .addEventListener('submit', e => {
    e.preventDefault();
    const input = document.getElementById('project-url');
    const newUrl = input.value.trim();
    if (newUrl) {
        addProject(newUrl);
        input.value = '';
    }
});

/* ==============================================================
   Popular‑project buttons
   ============================================================== */
document.querySelectorAll('.pop-btn')
        .forEach(btn => btn.addEventListener('click', () => {
    addProject(btn.dataset.url);
}));

/* ==============================================================
   GitHub search UI
   ============================================================== */
const ghForm = document.getElementById('gh-search-form');
const ghQueryInput = document.getElementById('gh-query');
const ghResultsUl = document.getElementById('gh-results');

ghForm.addEventListener('submit', async e => {
    e.preventDefault();
    const query = ghQueryInput.value.trim();
    if (!query) return;

    ghResultsUl.innerHTML = '<li>Searching…</li>';

    try {
        const resp = await fetch(
            `https://api.github.com/search/repositories?q=${encodeURIComponent(query)}&per_page=5`
        );
        if (!resp.ok) throw new Error(`GitHub search failed (${resp.status})`);
        const data = await resp.json();

        if (!data.items || data.items.length === 0) {
            ghResultsUl.innerHTML = '<li>No repositories found.</li>';
            return;
        }

        ghResultsUl.innerHTML = '';
        data.items.forEach(repo => {
            const li = document.createElement('li');

            const info = document.createElement('span');
            info.textContent = `${repo.full_name}: ${repo.description || ''}`;

            const addBtn = document.createElement('button');
            addBtn.textContent = 'Add';
            addBtn.className = 'add-from-gh';
            addBtn.addEventListener('click', () => {
                addProject(repo.html_url);
            });

            li.appendChild(info);
            li.appendChild(addBtn);
            ghResultsUl.appendChild(li);
        });
    } catch (err) {
        console.error(err);
        ghResultsUl.innerHTML = `<li>Error searching GitHub.</li>`;
    }
});

/* ==============================================================
   Refresh button – reload the RSS feed for the currently selected
   ============================================================== */
refreshBtn.addEventListener('click', () => {
    if (lastSelectedProject) {
        // Force a fresh fetch by clearing the cache for this session
        cachedRss = null;
        loadVulnerabilities(lastSelectedProject);
    }
});

/* ==============================================================
   RSS‑feed processing (Canadian Cyber Centre)
   --------------------------------------------------------------
   Public RSS feed: https://www.cyber.gc.ca/rss/alerts.xml
   -------------------------------------------------------------- */
const CYBER_CENTRE_RSS = 'https://www.cyber.gc.ca/rss/alerts.xml';

async function fetchRssFeed() {
    const resp = await fetch(CYBER_CENTRE_RSS);
    if (!resp.ok) throw new Error(`RSS fetch failed (${resp.status})`);
    const txt = await resp.text();
    const parser = new DOMParser();
    return parser.parseFromString(txt, 'application/xml');
}

function extractItems(rssDoc) {
    const items = Array.from(rssDoc.querySelectorAll('item'));
    return items.map(it => ({
        title: it.querySelector('title')?.textContent?.trim() ?? '',
        link: it.querySelector('link')?.textContent?.trim() ?? '',
        pubDate: it.querySelector('pubDate')?.textContent?.trim() ?? '',
        description: it.querySelector('description')?.textContent?.trim() ?? ''
    }));
}

/**
 * Turn a project URL into a simple keyword that is likely to appear
 * in the RSS title/description.
 *   • GitHub → repository name (last path segment)
 *   • Anything else → hostname
 */
function deriveKeyword(projectUrl) {
    try {
        const u = new URL(projectUrl);
        if (u.hostname === 'github.com') {
            const parts = u.pathname.split('/').filter(Boolean);
            return parts[1] || parts[0]; // repo name
        }
        return u.hostname;
    } catch (_) {
        return projectUrl;
    }
}

/**
 * Load and display vulnerability notices for a given project URL.
 */
async function loadVulnerabilities(projectUrl) {
    resultsDiv.textContent = 'Fetching latest advisories…';

    const keyword = deriveKeyword(projectUrl).toLowerCase();

    try {
        const rssDoc = cachedRss ?? await fetchRssFeed();
        cachedRss = rssDoc; // keep for subsequent calls

        const items = extractItems(rssDoc);
        const matches = items.filter(it =>
            it.title.toLowerCase().includes(keyword) ||
            it.description.toLowerCase().includes(keyword)
        );

        if (matches.length === 0) {
            resultsDiv.textContent = `No advisories found for "${keyword}".`;
            return;
        }

        const lines = matches.map(it => {
            const date = new Date(it.pubDate).toLocaleDateString(undefined, {
                year: 'numeric', month: 'short', day: 'numeric'
            });
            return `• ${it.title}
  Date: ${date}
  Link: ${it.link}
  ${it.description}`;
        });

        resultsDiv.textContent = lines.join('\n\n');
    } catch (err) {
        console.error(err);
        resultsDiv.textContent = `Error loading advisories. Please try again later.`;
    }
}

/* ==============================================================
   Page initialisation – Shepherd
   ============================================================== */
const initialStack = getStackFromUrl();
renderProjectList(initialStack);
if (initialStack.length) {
    // Auto‑load the first project's notices for a quick start
    lastSelectedProject = initialStack[0];
    refreshBtn.disabled = false;
    loadVulnerabilities(initialStack[0]);
}