const MAX_TOASTS = 3;

function dismissToast(toast) {
    if (!toast.isConnected) return;
    toast.classList.remove('show');
    toast.addEventListener('transitionend', () => toast.remove(), { once: true });
}

function showToast(message, { error = false, duration = 4000 } = {}) {
    const container = document.getElementById('toast-container');

    // Dedupe: if the most recent toast says the same thing, just keep it.
    const existing = container.querySelectorAll('.toast');
    const last = existing[existing.length - 1];
    if (last && last.textContent === message) return;

    // Cap the stack: dismiss the oldest until we're under the limit.
    while (container.querySelectorAll('.toast').length >= MAX_TOASTS) {
        dismissToast(container.querySelector('.toast'));
    }

    const toast = document.createElement('div');
    toast.className = 'toast' + (error ? ' error' : '');
    toast.textContent = message;
    container.appendChild(toast);
    requestAnimationFrame(() => toast.classList.add('show'));
    setTimeout(() => dismissToast(toast), duration);
}

const state = {
    lat: null,
    lng: null,
    seed: null,
    routeCoords: null,
    geojson: null,
};

const map = L.map('map', { zoomControl: false }).setView([51.505, -0.09], 13);

L.control.zoom({ position: 'topright' }).addTo(map);

const tileConfigs = {
    street:    'https://{s}.basemaps.cartocdn.com/rastertiles/voyager/{z}/{x}/{y}{r}.png',
    satellite: 'https://server.arcgisonline.com/ArcGIS/rest/services/World_Imagery/MapServer/tile/{z}/{y}/{x}',
    topo:      'https://{s}.tile.opentopomap.org/{z}/{x}/{y}.png',
};

const baseTile = L.tileLayer(tileConfigs.street, {
    attribution: '© <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors © <a href="https://carto.com/">CARTO</a>',
    maxZoom: 19,
}).addTo(map);

document.querySelectorAll('input[name="mapstyle"]').forEach(input => {
    input.closest('label').addEventListener('click', () => {
        baseTile.setUrl(tileConfigs[input.value]);
    });
});

let marker = null;
let casingLayer = null;
let routeLayer = null;
let arrowLayer = null;
let kmMarkers = [];

// ── Geometry helpers ─────────────────────────────────

function haversineMetres(a, b) {
    const R = 6371000;
    const toRad = (d) => d * Math.PI / 180;
    const dLat = toRad(b[0] - a[0]);
    const dLng = toRad(b[1] - a[1]);
    const s = Math.sin(dLat / 2) ** 2 +
        Math.cos(toRad(a[0])) * Math.cos(toRad(b[0])) * Math.sin(dLng / 2) ** 2;
    return R * 2 * Math.atan2(Math.sqrt(s), Math.sqrt(1 - s));
}

// Sum positive elevation deltas from ORS geometry coords ([lng, lat, elev]).
// 1m per-step threshold filters DEM noise.
function computeAscent(coords) {
    let total = 0;
    for (let i = 1; i < coords.length; i++) {
        const a = coords[i - 1], b = coords[i];
        if (a.length < 3 || b.length < 3) continue;
        const d = b[2] - a[2];
        if (d > 1) total += d;
    }
    return total;
}

// Walk the polyline and emit a marker every `stepM` metres, starting at km 1.
function buildKmMarkers(latlngs, stepM) {
    const out = [];
    let acc = 0;
    let nextMark = stepM;
    let label = 1;
    for (let i = 0; i < latlngs.length - 1; i++) {
        const a = latlngs[i];
        const b = latlngs[i + 1];
        const seg = haversineMetres(a, b);
        if (seg === 0) continue;
        while (acc + seg >= nextMark) {
            const t = (nextMark - acc) / seg;
            const lat = a[0] + (b[0] - a[0]) * t;
            const lng = a[1] + (b[1] - a[1]) * t;
            out.push({ latlng: [lat, lng], label: label++ });
            nextMark += stepM;
        }
        acc += seg;
    }
    return out;
}

// Build an inline SVG elevation chart from ORS coords ([lng, lat, ele]).
// Returns an HTML string, or null if elevation data is absent or the route is flat.
function buildElevationProfile(coords) {
    if (coords.length < 4 || coords[0].length < 3) return null;

    // Downsample to at most 150 points so the SVG stays compact.
    const step = Math.max(1, Math.floor(coords.length / 150));
    const pts = [];
    for (let i = 0; i < coords.length; i += step) pts.push(coords[i]);
    if (pts[pts.length - 1] !== coords[coords.length - 1]) {
        pts.push(coords[coords.length - 1]);
    }

    const eles = pts.map(c => c[2]);
    const minE = Math.min(...eles);
    const maxE = Math.max(...eles);
    if (maxE - minE < 5) return null; // too flat to be informative

    // Cumulative horizontal distance for x-axis positioning.
    const cumDist = [0];
    for (let i = 1; i < pts.length; i++) {
        const prev = pts[i - 1], curr = pts[i];
        cumDist.push(cumDist[i - 1] + haversineMetres([prev[1], prev[0]], [curr[1], curr[0]]));
    }
    const totalD = cumDist[cumDist.length - 1] || 1;

    const W = 260, H = 44, pad = 2;
    const polyPts = pts.map((c, i) => {
        const x = (cumDist[i] / totalD) * W;
        const y = pad + (H - pad * 2) * (1 - (c[2] - minE) / (maxE - minE));
        return `${x.toFixed(1)},${y.toFixed(1)}`;
    });

    const lineD = `M ${polyPts[0]} L ${polyPts.slice(1).join(' L ')}`;
    const areaD = `${lineD} L ${W},${H} L 0,${H} Z`;

    return `<svg viewBox="0 0 ${W} ${H}" preserveAspectRatio="none" height="${H}" style="width:100%;display:block;border-radius:4px" aria-hidden="true">
        <defs><linearGradient id="eg" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stop-color="#e84422" stop-opacity="0.16"/>
            <stop offset="100%" stop-color="#e84422" stop-opacity="0"/>
        </linearGradient></defs>
        <path d="${areaD}" fill="url(#eg)"/>
        <path d="${lineD}" fill="none" stroke="#e84422" stroke-width="1.5" stroke-linejoin="round" stroke-linecap="round" vector-effect="non-scaling-stroke"/>
    </svg>
    <div class="elevation-labels">
        <span>${Math.round(minE)} m</span>
        <span>${Math.round(maxE)} m</span>
    </div>`;
}

// ── Pace helper ──────────────────────────────────────

function getSelectedPace() {
    const el = document.querySelector('input[name="pace"]:checked');
    return el ? parseInt(el.value, 10) : 6;
}

// ── Location ─────────────────────────────────────────

function setLocation(lat, lng) {
    state.lat = lat;
    state.lng = lng;

    if (marker) marker.remove();
    const icon = L.divIcon({
        className: '',
        html: `<div style="
            width:22px;height:22px;border-radius:50%;
            background:#e84422;border:3px solid #fff;
            box-shadow:0 0 0 2px #e84422,0 2px 8px rgba(0,0,0,0.25);
        "></div>`,
        iconSize: [22, 22],
        iconAnchor: [11, 11],
    });
    marker = L.marker([lat, lng], { icon }).addTo(map);

    document.getElementById('location-text').textContent = `${lat.toFixed(5)}, ${lng.toFixed(5)}`;
    document.getElementById('location-pill').classList.remove('hidden');
    document.getElementById('location-empty-hint').classList.add('hidden');
    document.getElementById('generate-btn').disabled = false;
}

map.on('click', (e) => {
    setLocation(e.latlng.lat, e.latlng.lng);
    // Tapping the map should collapse the bottom sheet so the user can see
    // where they tapped without the sheet covering it.
    collapsePanel();
});

// ── Mobile bottom-sheet expand/collapse ──────────────────────────────
const panelEl = document.getElementById('panel');
const handleEl = document.getElementById('panel-handle');
const mobileMQ = window.matchMedia('(max-width: 640px)');

function isMobile() { return mobileMQ.matches; }

function expandPanel() {
    if (!isMobile()) return;
    panelEl.classList.add('expanded');
    handleEl.setAttribute('aria-expanded', 'true');
}
function collapsePanel() {
    panelEl.classList.remove('expanded');
    handleEl.setAttribute('aria-expanded', 'false');
}
function togglePanel() {
    if (panelEl.classList.contains('expanded')) collapsePanel();
    else expandPanel();
}

handleEl.addEventListener('click', togglePanel);
mobileMQ.addEventListener('change', () => {
    // Reset state when crossing the breakpoint so desktop is never stuck
    // with a stray .expanded class (harmless, but tidy).
    if (!isMobile()) collapsePanel();
});

// Returns padding for fitBounds that keeps the route clear of the panel —
// left-padded on desktop (panel is to the left), bottom-padded on mobile
// (panel is docked to the bottom). Without this, mobile inherited the desktop
// 344px left padding and zoomed the route into a sliver.
function routeFitPadding() {
    if (isMobile()) {
        const h = panelEl.getBoundingClientRect().height || 200;
        return { paddingTopLeft: [20, 60], paddingBottomRight: [20, h + 20] };
    }
    return { paddingTopLeft: [344, 60], paddingBottomRight: [60, 60] };
}

document.getElementById('locate-btn').addEventListener('click', () => {
    if (!navigator.geolocation) {
        showToast('Geolocation is not supported by your browser.', { error: true });
        return;
    }
    navigator.geolocation.getCurrentPosition(
        (pos) => {
            const { latitude, longitude } = pos.coords;
            map.setView([latitude, longitude], 14);
            setLocation(latitude, longitude);
        },
        () => showToast('Unable to retrieve your location.', { error: true })
    );
});

const distanceInput = document.getElementById('distance');
const distanceDisplay = document.getElementById('distance-display');
distanceInput.addEventListener('input', () => {
    distanceDisplay.textContent = `${(distanceInput.value / 1000).toFixed(1)} km`;
});

// ── Route generation ─────────────────────────────────

async function generateRoute(triggerBtn) {
    state.seed = Math.floor(Math.random() * 9999) + 1;

    const primary = document.getElementById('generate-btn');
    primary.classList.add('loading');
    primary.disabled = true;
    if (triggerBtn && triggerBtn !== primary) triggerBtn.disabled = true;

    try {
        const resp = await fetch('/api/route', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                lat: state.lat,
                lng: state.lng,
                distance: parseInt(distanceInput.value),
                seed: state.seed,
                allowLaps: document.getElementById('allow-laps').checked,
            }),
        });

        if (resp.status === 429) {
            const retry = parseInt(resp.headers.get('Retry-After') || '30', 10);
            showToast(`Slow down — try again in ${retry}s.`, { error: true });
            return;
        }
        if (!resp.ok) {
            showToast("Couldn't build a route from here. Try a different start or distance.", { error: true });
            return;
        }
        displayRoute(await resp.json());
    } catch (err) {
        showToast('Network error — please try again.', { error: true });
    } finally {
        primary.classList.remove('loading');
        primary.disabled = false;
        if (triggerBtn && triggerBtn !== primary) triggerBtn.disabled = false;
    }
}

document.getElementById('generate-btn').addEventListener('click', (e) => generateRoute(e.currentTarget));
document.getElementById('regen-btn').addEventListener('click', (e) => generateRoute(e.currentTarget));

// Press Enter to generate when no interactive element is focused.
document.addEventListener('keydown', (e) => {
    if (e.key !== 'Enter') return;
    const active = document.activeElement;
    if (active && active !== document.body && active.tagName !== 'DIV') return;
    const btn = document.getElementById('generate-btn');
    if (!btn.disabled) btn.click();
});

// ── Route display ─────────────────────────────────────

function displayRoute(geojson) {
    if (casingLayer) casingLayer.remove();
    if (routeLayer) routeLayer.remove();
    if (arrowLayer) arrowLayer.remove();
    kmMarkers.forEach(m => m.remove());
    kmMarkers = [];

    const feature = geojson.features?.[0];
    if (!feature) {
        showToast('No route returned. Try a different location or distance.', { error: true });
        return;
    }

    const coords = feature.geometry.coordinates;
    state.routeCoords = coords;
    state.geojson = geojson;
    const latlngs = coords.map(([lng, lat]) => [lat, lng]);

    casingLayer = L.polyline(latlngs, { color: '#fff', weight: 8, opacity: 1 }).addTo(map);
    routeLayer  = L.polyline(latlngs, { color: '#e84422', weight: 4.5, opacity: 1 }).addTo(map);

    // Collapse the sheet first so the panel is at its peek height when we
    // measure it for padding — otherwise an expanded sheet would over-pad.
    collapsePanel();
    map.fitBounds(routeLayer.getBounds(), routeFitPadding());

    arrowLayer = L.polylineDecorator(routeLayer, {
        patterns: [{
            offset: 25,
            repeat: 60,
            symbol: L.Symbol.arrowHead({
                pixelSize: 12,
                polygon: true,
                pathOptions: { fillOpacity: 1, fillColor: '#fff', color: '#e84422', weight: 1.5, stroke: true },
            }),
        }],
    }).addTo(map);

    // Numbered km markers so the running order is unambiguous even when
    // segments overlap.
    const totalKm = (feature.properties?.summary?.distance || 0) / 1000;
    const step = totalKm > 12 ? 2000 : 1000;
    const marks = buildKmMarkers(latlngs, step);
    kmMarkers = marks.map(m => {
        const icon = L.divIcon({
            className: 'km-marker',
            html: `<div class="km-marker-dot">${m.label}</div>`,
            iconSize: [24, 24],
            iconAnchor: [12, 12],
        });
        return L.marker(m.latlng, { icon, interactive: false, keyboard: false }).addTo(map);
    });

    const summary = feature.properties?.summary;
    if (summary) {
        const distKm = summary.distance / 1000;
        const ascent = computeAscent(coords);
        document.getElementById('stat-distance').textContent = `${distKm.toFixed(1)} km`;
        document.getElementById('stat-time').textContent = `~${Math.round(distKm * getSelectedPace())} min`;
        document.getElementById('stat-ascent').textContent = `${Math.round(ascent)} m`;

        // Elevation profile — shown only when there's meaningful terrain variation.
        const profileEl = document.getElementById('elevation-profile');
        const profileHtml = buildElevationProfile(coords);
        if (profileHtml) {
            profileEl.innerHTML = profileHtml;
            profileEl.classList.remove('hidden');
        } else {
            profileEl.innerHTML = '';
            profileEl.classList.add('hidden');
        }

        document.getElementById('results').classList.remove('hidden');
    }
}

// Live-update the time estimate when pace changes after a route is generated.
document.querySelectorAll('input[name="pace"]').forEach(input => {
    input.addEventListener('change', () => {
        const feature = state.geojson?.features?.[0];
        if (!feature) return;
        const distKm = feature.properties?.summary?.distance / 1000;
        document.getElementById('stat-time').textContent = `~${Math.round(distKm * getSelectedPace())} min`;
    });
});

// ── Share ─────────────────────────────────────────────

document.getElementById('share-btn').addEventListener('click', async () => {
    if (!state.geojson || !state.lat) return;

    const btn = document.getElementById('share-btn');
    const original = btn.textContent;
    btn.textContent = 'Sharing…';
    btn.disabled = true;

    try {
        const meta = {
            distance: parseInt(distanceInput.value),
        };

        const resp = await fetch('/api/share', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ route: state.geojson, meta }),
        });

        if (!resp.ok) throw new Error(await resp.text());

        const { id } = await resp.json();
        const url = `${window.location.origin}/share/${id}`;
        await navigator.clipboard.writeText(url);
        btn.textContent = 'Copied!';
        setTimeout(() => { btn.textContent = original; btn.disabled = false; }, 2000);
    } catch (err) {
        showToast('Could not share route. Please try again.', { error: true });
        btn.textContent = original;
        btn.disabled = false;
    }
});

// ── GPX export ────────────────────────────────────────

document.getElementById('export-btn').addEventListener('click', async () => {
    if (!state.routeCoords) return;

    try {
        const resp = await fetch('/api/export/gpx', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ coordinates: state.routeCoords }),
        });

        if (!resp.ok) throw new Error('Export failed');

        const distKm = ((state.geojson?.features?.[0]?.properties?.summary?.distance || 0) / 1000).toFixed(1);
        const filename = `loop-${distKm}km.gpx`;

        const url = URL.createObjectURL(await resp.blob());
        const a = document.createElement('a');
        a.href = url;
        a.download = filename;
        a.click();
        URL.revokeObjectURL(url);
    } catch (err) {
        showToast('Could not export GPX. Please try again.', { error: true });
    }
});
