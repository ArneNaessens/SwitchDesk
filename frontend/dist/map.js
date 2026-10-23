"use strict";
/* Network map: LLDP/CDP topology on a canvas with force layout, drag, pan and zoom. */

const SDMap = (() => {
  let cv, ctx, opts, topo = { nodes: [], edges: [] };
  let nodes = new Map(); // id -> node with x,y,vx,vy
  let view = { k: 1, tx: 0, ty: 0 };
  let alpha = 0, raf = 0, running = false, selected = null, hover = null;
  let drag = null, saveTimer = 0, ro = null, firstFit = true, pendingFit = false;
  const DPR = () => window.devicePixelRatio || 1;
  const C = { line: "#5d7088", text: "#e4e8ef", muted: "#8b95a7", panel: "#232a36", panel2: "#2a3240", border: "#3a4558", up: "#4cc38a", crit: "#f0616d", warn: "#e8b04a", info: "#6ea8fe", accent: "#8fb3ff" };
  const SW = { w: 136, h: 38 };

  function size() {
    const r = cv.getBoundingClientRect();
    cv.width = Math.max(1, r.width * DPR());
    cv.height = Math.max(1, r.height * DPR());
    draw();
  }

  async function reload() {
    if (!running) return;
    try {
      topo = await opts.load();
    } catch (e) {
      opts.status("Couldn't load the map: " + e.message);
      return;
    }
    const next = new Map();
    const depth = layers();
    const perLayer = new Map();
    let unplaced = 0;
    topo.nodes.forEach((n) => {
      const old = nodes.get(n.id);
      const node = Object.assign({}, n, { vx: 0, vy: 0, layer: depth.get(n.id) });
      if (old) { node.x = old.x; node.y = old.y; node.pinned = old.pinned; }
      else if (n.x != null) { node.x = n.x; node.y = n.y; node.pinned = true; }
      else {
        const i = perLayer.get(node.layer) || 0;
        perLayer.set(node.layer, i + 1);
        node.x = (i % 2 ? 1 : -1) * Math.ceil(i / 2) * 190 + (Math.random() - 0.5) * 10;
        node.y = node.layer * LAYER;
        unplaced++;
      }
      next.set(n.id, node);
    });
    nodes = next;
    if (selected && !nodes.has(selected)) selected = null;
    const managed = topo.nodes.filter((n) => n.managed).length;
    opts.status(`${managed} switches, ${topo.nodes.length - managed} other devices, ${topo.edges.length} links`);
    if (unplaced) kick(1);
    if (firstFit && topo.nodes.length) { firstFit = false; fit(); if (unplaced) pendingFit = true; }
    if (selected) opts.select(nodes.get(selected), topo);
    draw();
  }

  const LAYER = 170;

  // hop distance from the spanning-tree root (or the busiest switch), for a top-down layout
  function layers() {
    const adj = new Map(topo.nodes.map((n) => [n.id, []]));
    for (const e of topo.edges) { adj.get(e.from)?.push(e.to); adj.get(e.to)?.push(e.from); }
    let roots = topo.nodes.filter((n) => n.stpRoot).map((n) => n.id);
    if (!roots.length) {
      const m = topo.nodes.filter((n) => n.managed).sort((a, b) => adj.get(b.id).length - adj.get(a.id).length)[0];
      roots = m ? [m.id] : [];
    }
    const d = new Map(roots.map((r) => [r, 0]));
    const q = [...roots];
    while (q.length) {
      const id = q.shift();
      for (const nb of adj.get(id) || []) if (!d.has(nb)) { d.set(nb, d.get(id) + 1); q.push(nb); }
    }
    const max = Math.max(0, ...d.values());
    for (const n of topo.nodes) if (!d.has(n.id)) d.set(n.id, max + 1);
    return d;
  }

  function kick(a) {
    alpha = Math.max(alpha, a);
    if (!raf) raf = requestAnimationFrame(tick);
  }

  function tick() {
    raf = 0;
    if (!running) return;
    step();
    draw();
    if (alpha > 0.02) raf = requestAnimationFrame(tick);
    else { scheduleSave(); if (pendingFit) { pendingFit = false; fit(); } }
  }

  function step() {
    const arr = [...nodes.values()];
    const k = alpha;
    // repulsion
    for (let i = 0; i < arr.length; i++) {
      for (let j = i + 1; j < arr.length; j++) {
        const a = arr[i], b = arr[j];
        let dx = b.x - a.x, dy = b.y - a.y;
        let d2 = dx * dx + dy * dy;
        if (d2 < 1) { dx = Math.random() - 0.5; dy = Math.random() - 0.5; d2 = 1; }
        const min = (a.managed && b.managed) ? 200 : 120;
        if (d2 > 640000) continue;
        const f = (min * min * 0.9) / d2;
        const d = Math.sqrt(d2);
        const fx = (dx / d) * f * k, fy = (dy / d) * f * k;
        a.vx -= fx; a.vy -= fy; b.vx += fx; b.vy += fy;
      }
    }
    // keep boxes from overlapping (they are much wider than tall)
    for (let i = 0; i < arr.length; i++) {
      for (let j = i + 1; j < arr.length; j++) {
        const a = arr[i], b = arr[j];
        const wa = a.managed ? SW.w : 60, wb = b.managed ? SW.w : 60;
        const dx = b.x - a.x, dy = b.y - a.y;
        const ox = (wa + wb) / 2 + 40 - Math.abs(dx), oy = 90 - Math.abs(dy);
        if (ox > 0 && oy > 0) {
          const push = ox * 0.5 * Math.max(k, 0.2);
          const sx = dx === 0 ? (Math.random() < 0.5 ? -1 : 1) : Math.sign(dx);
          a.vx -= sx * push; b.vx += sx * push;
        }
      }
    }
    // springs
    for (const e of topo.edges) {
      const a = nodes.get(e.from), b = nodes.get(e.to);
      if (!a || !b) continue;
      const dx = b.x - a.x, dy = b.y - a.y;
      const d = Math.sqrt(dx * dx + dy * dy) || 1;
      const len = a.managed && b.managed ? 230 : 130;
      const f = ((d - len) / d) * 0.06 * k;
      a.vx += dx * f; a.vy += dy * f; b.vx -= dx * f; b.vy -= dy * f;
    }
    for (const n of arr) {
      if (n.layer !== undefined) n.vy += (n.layer * LAYER - n.y) * 0.08 * k;
      if (n.pinned || (drag && drag.node === n)) { n.vx = n.vy = 0; continue; }
      n.vx *= 0.6; n.vy *= 0.6;
      n.x += Math.max(-30, Math.min(30, n.vx));
      n.y += Math.max(-30, Math.min(30, n.vy));
    }
    alpha *= 0.97;
  }

  // ---------- drawing ----------
  function toScreen(x, y) { return [x * view.k + view.tx, y * view.k + view.ty]; }
  function toWorld(x, y) { return [(x - view.tx) / view.k, (y - view.ty) / view.k]; }

  function rr(x, y, w, h, r) {
    ctx.beginPath();
    ctx.moveTo(x + r, y);
    ctx.arcTo(x + w, y, x + w, y + h, r);
    ctx.arcTo(x + w, y + h, x, y + h, r);
    ctx.arcTo(x, y + h, x, y, r);
    ctx.arcTo(x, y, x + w, y, r);
    ctx.closePath();
  }

  function draw() {
    if (!ctx) return;
    const dpr = DPR();
    ctx.setTransform(1, 0, 0, 1, 0, 0);
    ctx.clearRect(0, 0, cv.width, cv.height);
    // faint grid, like a floor plan
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    const step = 40 * view.k;
    if (step > 12) {
      ctx.fillStyle = "rgba(139,149,167,.10)";
      const ox = view.tx % step, oy = view.ty % step;
      for (let x = ox; x < cv.clientWidth; x += step) for (let y = oy; y < cv.clientHeight; y += step) ctx.fillRect(x, y, 1.2, 1.2);
    }
    ctx.setTransform(dpr * view.k, 0, 0, dpr * view.k, dpr * view.tx, dpr * view.ty);

    // parallel edges get spread out
    const pairCount = new Map(), pairIdx = new Map(), labels = [];
    for (const e of topo.edges) { const key = [e.from, e.to].sort().join("|"); pairCount.set(key, (pairCount.get(key) || 0) + 1); }
    for (const e of topo.edges) {
      const a = nodes.get(e.from), b = nodes.get(e.to);
      if (!a || !b) continue;
      const key = [e.from, e.to].sort().join("|");
      const i = pairIdx.get(key) || 0; pairIdx.set(key, i + 1);
      const n = pairCount.get(key);
      const off = (i - (n - 1) / 2) * 22;
      const dx = b.x - a.x, dy = b.y - a.y, d = Math.hypot(dx, dy) || 1;
      const nx = -dy / d, ny = dx / d;
      const cx = (a.x + b.x) / 2 + nx * off, cy = (a.y + b.y) / 2 + ny * off;
      const f0 = selected || hover;
      const hl = f0 && (e.from === f0 || e.to === f0);
      ctx.beginPath();
      ctx.moveTo(a.x, a.y);
      ctx.quadraticCurveTo(cx, cy, b.x, b.y);
      ctx.lineWidth = (hl ? 2.6 : 1.8) / Math.max(0.6, view.k) * Math.min(1, view.k + 0.2);
      ctx.setLineDash(e.blocking ? [7, 5] : []);
      ctx.strokeStyle = !e.up ? C.crit : e.blocking ? C.warn : hl ? C.accent : C.line;
      ctx.stroke();
      ctx.setLineDash([]);
      const focus = selected || hover;
      if (view.k > 1.6 || (focus && (e.from === focus || e.to === focus))) labels.push([e.fromPort, a, cx, cy], [e.toPort, b, cx, cy]);
    }
    for (const n of nodes.values()) (n.managed ? drawSwitch : drawDevice)(n);
    for (const l of labels) label(...l);
  }

  // port name drawn a short way along the edge from node n toward the control point
  function label(text, n, cx, cy) {
    if (!text) return;
    const dx = cx - n.x, dy = cy - n.y, d = Math.hypot(dx, dy) || 1;
    // distance from the centre to where the edge leaves the node, plus a gap
    const edge = n.managed ? Math.min(SW.w / 2 / (Math.abs(dx / d) || 1e-6), SW.h / 2 / (Math.abs(dy / d) || 1e-6)) : 16;
    const dist = Math.min(edge + 16, d * 0.45);
    const x = n.x + (dx / d) * dist, y = n.y + (dy / d) * dist;
    ctx.font = '500 10.5px "Plex Mono", monospace';
    const w = ctx.measureText(text).width + 8;
    ctx.fillStyle = "rgba(27,33,43,.92)";
    rr(x - w / 2, y - 8, w, 16, 3);
    ctx.fill();
    ctx.fillStyle = C.muted;
    ctx.textAlign = "center";
    ctx.textBaseline = "middle";
    ctx.fillText(text, x, y + 0.5);
  }

  function drawSwitch(n) {
    const w = SW.w, h = SW.h, x = n.x - w / 2, y = n.y - h / 2;
    const sel = selected === n.id, hov = hover === n.id;
    ctx.fillStyle = sel ? "#2f3a4d" : hov ? C.panel2 : C.panel;
    ctx.strokeStyle = sel ? C.accent : n.disabled || n.online ? C.border : C.crit;
    ctx.lineWidth = sel ? 2 : 1.2;
    rr(x, y, w, h, 5);
    ctx.fill();
    ctx.stroke();
    // status strip and a row of tiny port LEDs, echoing the dashboard faceplate
    ctx.fillStyle = n.disabled ? "#5d677a" : n.online ? C.up : C.crit;
    rr(x + 6, y + 8, 4, h - 16, 2);
    ctx.fill();
    ctx.fillStyle = C.text;
    ctx.font = '500 12.5px "Plex Sans", sans-serif';
    ctx.textAlign = "left";
    ctx.textBaseline = "middle";
    ctx.fillText(clip(n.label, w - 34), x + 17, y + 14);
    ctx.fillStyle = C.muted;
    ctx.font = '400 10.5px "Plex Mono", monospace';
    ctx.fillText(clip(n.host, w - 34), x + 17, y + 27);
    if (n.severity) {
      ctx.fillStyle = { critical: C.crit, warning: C.warn, info: C.info }[n.severity];
      ctx.beginPath();
      ctx.arc(x + w - 11, y + 11, 4.5, 0, Math.PI * 2);
      ctx.fill();
    }
    if (n.stpRoot) {
      ctx.font = '600 10px "Plex Sans", sans-serif';
      const tw = ctx.measureText("STP root").width + 10;
      ctx.fillStyle = "#3b4a63";
      rr(n.x - tw / 2, y - 18, tw, 15, 7);
      ctx.fill();
      ctx.fillStyle = "#c9d8f5";
      ctx.textAlign = "center";
      ctx.fillText("STP root", n.x, y - 10);
    }
  }

  const KIND = { "external-switch": ["#7b8db0", "S"], router: ["#b08ad8", "R"], ap: ["#5bc2d6", "AP"], phone: ["#9aa5b8", "P"], host: ["#9aa5b8", "H"] };

  function drawDevice(n) {
    const [col, glyph] = KIND[n.kind] || KIND.host;
    const r = n.kind === "phone" || n.kind === "host" ? 11 : 15;
    const sel = selected === n.id;
    ctx.beginPath();
    ctx.arc(n.x, n.y, r, 0, Math.PI * 2);
    ctx.fillStyle = sel ? "#2f3a4d" : "#1f2530";
    ctx.fill();
    ctx.lineWidth = sel ? 2 : 1.5;
    ctx.strokeStyle = sel ? C.accent : col;
    ctx.stroke();
    ctx.fillStyle = col;
    ctx.font = `600 ${glyph.length > 1 ? 9.5 : 11}px "Plex Sans", sans-serif`;
    ctx.textAlign = "center";
    ctx.textBaseline = "middle";
    ctx.fillText(glyph, n.x, n.y + 0.5);
    if (view.k > 0.5) {
      ctx.fillStyle = C.muted;
      ctx.font = '400 11px "Plex Sans", sans-serif';
      ctx.fillText(clip(n.label, 150), n.x, n.y + r + 11);
    }
  }

  function clip(t, max) {
    t = t || "";
    if (ctx.measureText(t).width <= max) return t;
    while (t.length > 1 && ctx.measureText(t + "…").width > max) t = t.slice(0, -1);
    return t + "…";
  }

  // ---------- interaction ----------
  function hit(px, py) {
    const [x, y] = toWorld(px, py);
    let best = null;
    for (const n of nodes.values()) {
      if (n.managed) {
        if (Math.abs(x - n.x) <= SW.w / 2 && Math.abs(y - n.y) <= SW.h / 2) best = n;
      } else if (Math.hypot(x - n.x, y - n.y) <= 17) best = n;
    }
    return best;
  }

  function pos(e) {
    const r = cv.getBoundingClientRect();
    return [e.clientX - r.left, e.clientY - r.top];
  }

  function onDown(e) {
    const [px, py] = pos(e);
    const n = hit(px, py);
    drag = { node: n, sx: px, sy: py, tx: view.tx, ty: view.ty, moved: false };
    if (n) { const [wx, wy] = toWorld(px, py); drag.ox = n.x - wx; drag.oy = n.y - wy; }
    cv.classList.add("dragging");
    cv.setPointerCapture(e.pointerId);
  }

  function onMove(e) {
    const [px, py] = pos(e);
    if (!drag) {
      const n = hit(px, py);
      const id = n ? n.id : null;
      if (id !== hover) { hover = id; cv.style.cursor = n ? "pointer" : ""; draw(); }
      return;
    }
    if (Math.hypot(px - drag.sx, py - drag.sy) > 3) drag.moved = true;
    if (drag.node) {
      const [wx, wy] = toWorld(px, py);
      drag.node.x = wx + drag.ox;
      drag.node.y = wy + drag.oy;
      drag.node.pinned = true;
      kick(0.08);
    } else {
      view.tx = drag.tx + (px - drag.sx);
      view.ty = drag.ty + (py - drag.sy);
    }
    draw();
  }

  function onUp() {
    if (!drag) return;
    const d = drag;
    drag = null;
    cv.classList.remove("dragging");
    if (!d.moved) {
      selected = d.node ? d.node.id : null;
      opts.select(d.node ? nodes.get(selected) : null, topo);
      draw();
    } else if (d.node) scheduleSave();
  }

  function onWheel(e) {
    e.preventDefault();
    const [px, py] = pos(e);
    const factor = Math.exp(-e.deltaY * (e.ctrlKey ? 0.01 : 0.0015));
    const k = Math.min(3, Math.max(0.2, view.k * factor));
    const [wx, wy] = toWorld(px, py);
    view.k = k;
    view.tx = px - wx * k;
    view.ty = py - wy * k;
    draw();
  }

  function scheduleSave() {
    clearTimeout(saveTimer);
    saveTimer = setTimeout(() => {
      const l = {};
      for (const n of nodes.values()) if (n.pinned || n.managed) l[n.id] = { x: Math.round(n.x), y: Math.round(n.y) };
      opts.saveLayout(l).catch(() => {});
    }, 600);
  }

  function fit() {
    if (!nodes.size) return;
    let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
    for (const n of nodes.values()) { x0 = Math.min(x0, n.x - 80); y0 = Math.min(y0, n.y - 40); x1 = Math.max(x1, n.x + 80); y1 = Math.max(y1, n.y + 40); }
    const w = cv.clientWidth, h = cv.clientHeight - 90;
    const k = Math.min(1.25, Math.max(0.2, Math.min(w / (x1 - x0), h / (y1 - y0))));
    view.k = k;
    view.tx = w / 2 - ((x0 + x1) / 2) * k;
    view.ty = 70 + h / 2 - ((y0 + y1) / 2) * k;
    draw();
  }

  function relayout() {
    for (const n of nodes.values()) { n.pinned = false; n.x += (Math.random() - 0.5) * 40; n.y += (Math.random() - 0.5) * 40; }
    pendingFit = true;
    kick(1);
  }

  return {
    start(canvas, o) {
      cv = canvas; opts = o; ctx = cv.getContext("2d"); running = true; firstFit = true;
      cv.addEventListener("pointerdown", onDown);
      cv.addEventListener("pointermove", onMove);
      cv.addEventListener("pointerup", onUp);
      cv.addEventListener("pointercancel", onUp);
      cv.addEventListener("wheel", onWheel, { passive: false });
      cv.addEventListener("dblclick", (e) => { const n = hit(...pos(e)); if (n?.managed) location.hash = "#/switch/" + n.id; });
      ro = new ResizeObserver(size);
      ro.observe(cv);
      size();
      document.fonts?.ready.then(draw);
      reload();
    },
    stop() {
      running = false;
      if (raf) cancelAnimationFrame(raf);
      raf = 0;
      ro?.disconnect();
      ro = null;
    },
    reload, fit, relayout,
  };
})();
