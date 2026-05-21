const state = {
    lat: null,
    lng: null,
    seed: null,
    routeCoords: null,
};

const map = L.map('map', { zoomControl: true }).setView([51.505, -0.09], 13);

L.tileLayer('https://{s}.basemaps.cartocdn.com/rastertiles/voyager/{z}/{x}/{y}{r}.png', {
    attribution: '© <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors © <a href="https://carto.com/">CARTO</a>',
    maxZoom: 19,
}).addTo(map);

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
            box-shadow:0 0 0 2px #e84422,0 2px 6px rgba(0,0,0,0.3);
            display:flex;align-items:center;justify-content:center;
        "><div style="width:6px;height:6px;border-radius:50%;background:#fff"></div></div>`,
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
distanceInput.addEventListener('input', () => {
    document.getElementById('distance-display').textContent =
        `${(distanceInput.value / 1000).toFixed(1)} km`;
});

document.getElementById('generate-btn').addEventListener('click', async () => {
    state.seed = Math.floor(Math.random() * 9999) + 1;

    const btn = document.getElementById('generate-btn');
    btn.textContent = 'Generating...';
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
        btn.textContent = 'Generate route';
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
    const latlngs = coords.map(([lng, lat]) => [lat, lng]);

    casingLayer = L.polyline(latlngs, { color: '#fff', weight: 7, opacity: 1 }).addTo(map);
    routeLayer  = L.polyline(latlngs, { color: '#e84422', weight: 4, opacity: 1 }).addTo(map);

    map.fitBounds(routeLayer.getBounds(), { padding: [40, 40] });

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
        a.download = 'circuit-route.gpx';
        a.click();
        URL.revokeObjectURL(url);
    } catch (err) {
        alert(`Could not export GPX: ${err.message}`);
    }
});
