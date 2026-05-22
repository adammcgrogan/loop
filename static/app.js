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

function haversineMetres(a, b) {
    const R = 6371000;
    const toRad = (d) => d * Math.PI / 180;
    const dLat = toRad(b[0] - a[0]);
    const dLng = toRad(b[1] - a[1]);
    const s = Math.sin(dLat / 2) ** 2 +
        Math.cos(toRad(a[0])) * Math.cos(toRad(b[0])) * Math.sin(dLng / 2) ** 2;
    return R * 2 * Math.atan2(Math.sqrt(s), Math.sqrt(1 - s));
}

// Walk the polyline and emit a marker every `stepM` metres, starting at km 1.
// Each marker is positioned on the line and labelled with its sequence number.
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

map.on('click', (e) => setLocation(e.latlng.lat, e.latlng.lng));

document.getElementById('locate-btn').addEventListener('click', () => {
    if (!navigator.geolocation) {
        alert('Geolocation is not supported by your browser.');
        return;
    }
    navigator.geolocation.getCurrentPosition(
        (pos) => {
            const { latitude, longitude } = pos.coords;
            map.setView([latitude, longitude], 14);
            setLocation(latitude, longitude);
        },
        () => alert('Unable to retrieve your location.')
    );
});

const distanceInput = document.getElementById('distance');
const distanceDisplay = document.getElementById('distance-display');
distanceInput.addEventListener('input', () => {
    distanceDisplay.textContent = `${(distanceInput.value / 1000).toFixed(1)} km`;
});

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
                surface: document.querySelector('input[name="surface"]:checked').value,
                hills: document.querySelector('input[name="hills"]:checked').value,
                seed: state.seed,
                allowLaps: document.getElementById('allow-laps').checked,
            }),
        });

        if (!resp.ok) throw new Error(await resp.text());
        displayRoute(await resp.json());
    } catch (err) {
        alert(`Could not generate route: ${err.message}`);
    } finally {
        primary.classList.remove('loading');
        primary.disabled = false;
        if (triggerBtn && triggerBtn !== primary) triggerBtn.disabled = false;
    }
}

document.getElementById('generate-btn').addEventListener('click', (e) => generateRoute(e.currentTarget));
document.getElementById('regen-btn').addEventListener('click', (e) => generateRoute(e.currentTarget));

function displayRoute(geojson) {
    if (casingLayer) casingLayer.remove();
    if (routeLayer) routeLayer.remove();
    if (arrowLayer) arrowLayer.remove();
    kmMarkers.forEach(m => m.remove());
    kmMarkers = [];

    const feature = geojson.features?.[0];
    if (!feature) {
        alert('No route returned. Try a different location or distance.');
        return;
    }

    const coords = feature.geometry.coordinates;
    state.routeCoords = coords;
    state.geojson = geojson;
    const latlngs = coords.map(([lng, lat]) => [lat, lng]);

    casingLayer = L.polyline(latlngs, { color: '#fff', weight: 8, opacity: 1 }).addTo(map);
    routeLayer  = L.polyline(latlngs, { color: '#e84422', weight: 4.5, opacity: 1 }).addTo(map);

    map.fitBounds(routeLayer.getBounds(), { paddingTopLeft: [344, 60], paddingBottomRight: [60, 60] });

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

    // Numbered km markers — make the running order unambiguous even when
    // segments overlap (you can read "1 → 2 → 3" along the route).
    const totalKm = (feature.properties?.summary?.distance || 0) / 1000;
    const step = totalKm > 12 ? 2000 : 1000; // 1km, or 2km for long routes
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
        const paceMinPerKm = 6;
        document.getElementById('stat-distance').textContent = `${distKm.toFixed(1)} km`;
        document.getElementById('stat-time').textContent = `~${Math.round(distKm * paceMinPerKm)} min`;
        document.getElementById('results').classList.remove('hidden');
    }
}

document.getElementById('share-btn').addEventListener('click', async () => {
    if (!state.geojson || !state.lat) return;

    const btn = document.getElementById('share-btn');
    const original = btn.textContent;
    btn.textContent = 'Sharing…';
    btn.disabled = true;

    try {
        const meta = {
            distance: parseInt(distanceInput.value),
            surface: document.querySelector('input[name="surface"]:checked').value,
            hills: document.querySelector('input[name="hills"]:checked').value,
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
        alert(`Could not share route: ${err.message}`);
        btn.textContent = original;
        btn.disabled = false;
    }
});

document.getElementById('export-btn').addEventListener('click', async () => {
    if (!state.routeCoords) return;

    try {
        const resp = await fetch('/api/export/gpx', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ coordinates: state.routeCoords }),
        });

        if (!resp.ok) throw new Error('Export failed');

        const url = URL.createObjectURL(await resp.blob());
        const a = document.createElement('a');
        a.href = url;
        a.download = 'loop-route.gpx';
        a.click();
        URL.revokeObjectURL(url);
    } catch (err) {
        alert(`Could not export GPX: ${err.message}`);
    }
});
