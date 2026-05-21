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

document.getElementById('generate-btn').addEventListener('click', async () => {
    state.seed = Math.floor(Math.random() * 9999) + 1;

    const btn = document.getElementById('generate-btn');
    btn.classList.add('loading');
    btn.disabled = true;

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
            }),
        });

        if (!resp.ok) throw new Error(await resp.text());
        displayRoute(await resp.json());
    } catch (err) {
        alert(`Could not generate route: ${err.message}`);
    } finally {
        btn.classList.remove('loading');
        btn.disabled = false;
    }
});

function displayRoute(geojson) {
    if (casingLayer) casingLayer.remove();
    if (routeLayer) routeLayer.remove();
    if (arrowLayer) arrowLayer.remove();

    const feature = geojson.features?.[0];
    if (!feature) {
        alert('No route returned. Try a different location or distance.');
        return;
    }

    const coords = feature.geometry.coordinates;
    state.routeCoords = coords;
    state.geojson = geojson;
    const latlngs = coords.map(([lng, lat]) => [lat, lng]);

    casingLayer = L.polyline(latlngs, { color: '#fff', weight: 7, opacity: 1 }).addTo(map);
    routeLayer  = L.polyline(latlngs, { color: '#e84422', weight: 4, opacity: 1 }).addTo(map);

    map.fitBounds(routeLayer.getBounds(), { padding: [40, 340] });

    arrowLayer = L.polylineDecorator(routeLayer, {
        patterns: [{
            offset: '5%',
            repeat: '10%',
            symbol: L.Symbol.arrowHead({
                pixelSize: 10,
                polygon: true,
                pathOptions: { fillOpacity: 1, fillColor: '#fff', color: '#e84422', weight: 1 },
            }),
        }],
    }).addTo(map);

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
