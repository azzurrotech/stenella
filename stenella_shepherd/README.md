# stenella_shepherd

This package contains the legacy Shepherd security advisory tracker — a
zero-dependency client-side HTML/CSS/JS app that monitors RSS security feeds
for mentions of tracked open-source projects.

## Files

- `index.html` — UI markup
- `style.css` — Styling
- `script.js` — Interactive logic (RSS fetch, stack handling, GitHub search)

## Usage

Open `index.html` in any browser (no server required). Add project URLs to
track, and the app fetches the Canadian Cyber Centre RSS feed to find matching
advisories.

## Integration

In stenella, this could serve as a security monitoring subsystem — display
CVE alerts relevant to the packages used in the ATI platform.
