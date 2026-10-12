let cursor = 0;
const $ = id => document.getElementById(id);
async function request(path, options = {}) {
  const res = await fetch(path, {...options, headers: {'Authorization': `Bearer ${$('token').value}`, 'Content-Type': 'application/json'}});
  const data = await res.json();
  if (!res.ok) throw new Error(data.error || `HTTP ${res.status}`);
  return data;
}
async function load(reset = false) {
  try {
    const rows = await request(`/api/events?after=${reset ? 0 : cursor}`);
    if (reset) { $('events').replaceChildren(); cursor = 0; }
    for (const e of rows) {
      const card = document.createElement('article');
      const title = document.createElement('h3'); title.textContent = `#${e.sequence} ${e.actor} / ${e.action}`;
      const meta = document.createElement('p'); meta.textContent = `${e.outcome} · ${e.time}`;
      const detail = document.createElement('p'); detail.textContent = e.details;
      const hash = document.createElement('code'); hash.textContent = e.hash;
      card.append(title, meta, detail, hash); $('events').append(card); cursor = e.sequence;
    }
    $('status').textContent = `Loaded ${rows.length} events. Cursor ${cursor}.`;
  } catch(e) { $('status').textContent = e.message; }
}
$('connect').onclick = () => load(true);
$('more').onclick = () => load();
$('verify').onclick = async () => { try { const v = await request('/api/verify'); $('status').textContent = `Chain ${v.valid ? 'valid' : 'INVALID'}: ${v.count} events. Head ${v.head || '(empty)'}.`; } catch(e) { $('status').textContent = e.message; } };
$('event').onsubmit = async event => {
  event.preventDefault();
  const payload = Object.fromEntries(new FormData(event.target));
  try { await request('/api/events', {method:'POST', body:JSON.stringify(payload)}); event.target.reset(); await load(true); } catch(e) { $('status').textContent = e.message; }
};
