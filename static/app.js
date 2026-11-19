"use strict";

const $ = (sel) => document.querySelector(sel);
const NS = "http://www.w3.org/2000/svg";

async function getJSON(url, opts) {
  const res = await fetch(url, opts);
  const body = await res.json();
  if (!res.ok) throw new Error(body.error || res.statusText);
  return body;
}

// Build a table body from rows; every value goes through textContent, never innerHTML.
function fillTable(sel, rows, cells, rowClass) {
  const tbody = $(sel + " tbody");
  tbody.replaceChildren(...rows.map((r) => {
    const tr = document.createElement("tr");
    if (rowClass) tr.className = rowClass(r);
    cells(r).forEach(([text, cls]) => {
      const td = document.createElement("td");
      td.textContent = text;
      if (cls) td.className = cls;
      tr.append(td);
    });
    return tr;
  }));
}

function fillList(sel, items) {
  $(sel).replaceChildren(...items.map((node) => {
    const li = document.createElement("li");
    li.append(...(Array.isArray(node) ? node : [node]));
    return li;
  }));
}

function svgEl(tag, attrs, text) {
  const el = document.createElementNS(NS, tag);
  for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, v);
  if (text !== undefined) el.textContent = text;
  return el;
}

function showError(sel, err) {
  const p = document.createElement("p");
  p.className = "error";
  p.textContent = "Unavailable: " + err.message;
  $(sel).replaceChildren(p);
}

const money = (n) => "€" + Number(n).toLocaleString(undefined, { maximumFractionDigits: 0 });

// ---- status + health ----

async function refreshStatus() {
  try {
    const s = await getJSON("/api/status");
    $("#conn").textContent = s.connected ? "Connected: " + s.detail : "Not connected: " + s.detail;
    $("#traffic").checked = s.traffic;
  } catch (e) {
    $("#conn").textContent = "App not reachable";
  }
}

async function refreshHealth() {
  const badge = $("#health-summary");
  try {
    const checks = await getJSON("/api/health");
    const bad = checks.filter((c) => !c.ok).length;
    badge.textContent = bad === 0 ? "All " + checks.length + " checks pass" : bad + " of " + checks.length + " checks fail";
    badge.className = "badge " + (bad === 0 ? "ok" : "bad");
    fillTable("#health", checks, (c) => [[c.model], [c.check], [c.expected], [c.actual], [c.ok ? "PASS" : "FAIL"]], (c) => (c.ok ? "ok" : "bad"));
  } catch (e) {
    badge.textContent = "Database unavailable";
    badge.className = "badge bad";
    $("#health tbody").replaceChildren();
  }
}

// ---- catalogue ----

let products = [];

async function refreshCatalogue() {
  try {
    const c = await getJSON("/api/catalogue");
    products = c.products || [];
    fillTable("#products", products, (p) => [[p.name], [p.category], [money(p.price), "num"]]);
    fillTable("#customers", c.customers || [], (x) => [[x.name], [x.city], [money(x.total), "num"]]);
    const sel = $("#similar-of");
    const current = sel.value;
    sel.replaceChildren(...products.map((p) => new Option(p.name, p.id)));
    sel.value = products.some((p) => p.id === current) ? current : "product:kettle";
  } catch (e) {
    $("#products tbody").replaceChildren();
    $("#customers tbody").replaceChildren();
  }
}

// ---- graph: customers on the left, products on the right ----

async function refreshGraph() {
  const svg = $("#graph");
  try {
    const edges = await getJSON("/api/graph");
    const custs = [...new Set(edges.map((e) => e.from))].sort();
    const prods = [...new Set(edges.map((e) => e.to))].sort();
    const name = (id) => id.split(":")[1];
    const y = (i, n) => 24 + (i * (380 - 48)) / Math.max(n - 1, 1);
    const cy = Object.fromEntries(custs.map((c, i) => [c, y(i, custs.length)]));
    const py = Object.fromEntries(prods.map((p, i) => [p, y(i, prods.length)]));
    const nodes = [];
    edges.forEach((e) => nodes.push(svgEl("line", { class: "edge", x1: 110, y1: cy[e.from], x2: 330, y2: py[e.to], "stroke-width": Math.min(1 + e.qty / 2, 5) })));
    custs.forEach((c) => {
      nodes.push(svgEl("circle", { class: "cust", cx: 110, cy: cy[c], r: 6 }));
      nodes.push(svgEl("text", { x: 98, y: cy[c] + 4, "text-anchor": "end" }, name(c)));
    });
    prods.forEach((p) => {
      const dot = svgEl("circle", { class: "prod", cx: 330, cy: py[p], r: 6 });
      dot.addEventListener("click", () => recommend(p));
      const label = svgEl("text", { x: 342, y: py[p] + 4, style: "cursor:pointer" }, name(p));
      label.addEventListener("click", () => recommend(p));
      nodes.push(dot, label);
    });
    svg.replaceChildren(...nodes);
  } catch (e) {
    svg.replaceChildren(svgEl("text", { x: 10, y: 20 }, "Unavailable: " + e.message));
  }
}

async function recommend(id) {
  try {
    const recs = await getJSON("/api/recommend?product=" + encodeURIComponent(id));
    const label = document.createElement("strong");
    label.textContent = id.split(":")[1] + ": ";
    fillList("#recs", recs.length ? recs.map((r) => r.name + " (" + r.count + " orders)") : ["No other purchases yet"]);
    $("#recs").prepend(label);
  } catch (e) {
    showError("#recs", e);
  }
}

// ---- full-text: \u0001 and \u0002 mark the highlighted words ----

function highlighted(text) {
  const frag = document.createDocumentFragment();
  text.split(/(\u0001[^\u0002]*\u0002)/).forEach((part) => {
    if (part.startsWith("\u0001")) {
      const m = document.createElement("mark");
      m.textContent = part.slice(1, -1);
      frag.append(m);
    } else if (part) {
      frag.append(document.createTextNode(part));
    }
  });
  return frag;
}

async function search(ev) {
  if (ev) ev.preventDefault();
  try {
    const hits = await getJSON("/api/search?q=" + encodeURIComponent($("#q").value));
    fillList("#hits", hits.length ? hits.map((h) => {
      const head = document.createElement("strong");
      head.textContent = h.product + " (" + h.rating + "/5): ";
      return [head, highlighted(h.text)];
    }) : ["No matches"]);
  } catch (e) {
    showError("#hits", e);
  }
}

// ---- vector ----

async function similar() {
  const id = $("#similar-of").value;
  if (!id) return;
  try {
    const sims = await getJSON("/api/similar?product=" + encodeURIComponent(id));
    fillList("#similar", sims.length ? sims.map((s) => s.name + " (cosine distance " + s.distance.toFixed(3) + ")") : ["No results: is the vector index there?"]);
  } catch (e) {
    showError("#similar", e);
  }
}

// ---- geospatial: plain equirectangular projection of western Europe ----

async function refreshStores() {
  const svg = $("#map");
  try {
    const g = await getJSON("/api/stores");
    const X = (lon) => 20 + ((lon + 6) / 22) * 420, Y = (lat) => 280 - ((lat - 38) / 18) * 260;
    const nodes = [];
    (g.customers || []).forEach((c) => {
      nodes.push(svgEl("circle", { class: "cust", cx: X(c.lon), cy: Y(c.lat), r: 4 }));
      nodes.push(svgEl("text", { x: X(c.lon) + 7, y: Y(c.lat) + 14 }, c.customer));
    });
    (g.stores || []).forEach((s) => {
      nodes.push(svgEl("rect", { class: "store", x: X(s.lon) - 5, y: Y(s.lat) - 5, width: 10, height: 10, rx: 2 }));
      nodes.push(svgEl("text", { x: X(s.lon) + 8, y: Y(s.lat) - 6 }, s.name));
    });
    svg.replaceChildren(...nodes);
    fillTable("#nearest", g.customers || [], (c) => [[c.customer + " (" + c.city + ")"], [c.store || "none"], [String(c.km), "num"]]);
  } catch (e) {
    svg.replaceChildren(svgEl("text", { x: 10, y: 20 }, "Unavailable: " + e.message));
    $("#nearest tbody").replaceChildren();
  }
}

// ---- time series + live feed ----

async function refreshOrders() {
  const svg = $("#chart");
  try {
    const o = await getJSON("/api/orders");
    const buckets = o.perMinute || [];
    const max = Math.max(1, ...buckets.map((b) => b.orders));
    const now = Date.now(), w = 440 / 30;
    const nodes = [svgEl("line", { class: "axis", x1: 10, y1: 120, x2: 450, y2: 120 })];
    buckets.forEach((b) => {
      const ago = (now - Date.parse(b.minute)) / 60000;
      if (ago > 30) return;
      const h = (b.orders / max) * 100;
      nodes.push(svgEl("rect", { class: "bar", x: 450 - (ago + 1) * w, y: 120 - h, width: w - 2, height: h }, undefined));
    });
    nodes.push(svgEl("text", { x: 10, y: 136 }, "30 min ago"), svgEl("text", { x: 450, y: 136, "text-anchor": "end" }, "now"), svgEl("text", { x: 10, y: 14 }, "max " + max + " orders/min"));
    svg.replaceChildren(...nodes);
    if (!$("#feed").children.length) (o.latest || []).slice().reverse().forEach(addFeed);
  } catch (e) {
    svg.replaceChildren(svgEl("text", { x: 10, y: 20 }, "Unavailable: " + e.message));
  }
}

function addFeed(ev) {
  const li = document.createElement("li");
  const what = document.createElement("span");
  what.textContent = (ev.customer || "?") + " bought " + ev.qty + " × " + (ev.product || "?");
  const when = document.createElement("span");
  when.className = "muted";
  when.textContent = new Date(ev.at).toLocaleTimeString() + " · " + money(ev.amount);
  li.append(what, when);
  const feed = $("#feed");
  feed.prepend(li);
  while (feed.children.length > 10) feed.lastChild.remove();
}

function connectLive() {
  const es = new EventSource("/api/orders/live");
  es.onmessage = (m) => addFeed(JSON.parse(m.data));
}

// ---- actions ----

$("#traffic").addEventListener("change", (e) => getJSON("/api/traffic?on=" + e.target.checked, { method: "POST" }).then(refreshStatus));
$("#incident").addEventListener("click", async () => {
  if (!confirm("Damage the data in every model? (Restore it with Kasten afterwards.)")) return;
  await getJSON("/api/incident", { method: "POST" }).catch((e) => alert(e.message));
  refreshAll();
});
$("#search-form").addEventListener("submit", search);
$("#similar-of").addEventListener("change", similar);

async function refreshAll() {
  await refreshStatus();
  await Promise.all([refreshHealth(), refreshCatalogue(), refreshGraph(), refreshStores(), refreshOrders()]);
  similar();
}

refreshAll().then(() => search());
connectLive();
setInterval(refreshAll, 5000);
