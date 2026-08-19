// Safe client-side renderers for structured learning elements.
// Model text is assigned through textContent; no model HTML or code is run.
(function () {
	'use strict';
	const SVG_NS = 'http://www.w3.org/2000/svg';

	function text(parent, tag, value, className) {
		const el = document.createElement(tag);
		if (className) el.className = className;
		el.textContent = value == null ? '' : String(value);
		parent.appendChild(el);
		return el;
	}

	function tableCard(data) {
		const card = document.createElement('div');
		card.className = 'learning-table-card border border-line bg-ink overflow-hidden';
		card.dataset.learningElement = 'table';
		if (data.title || data.caption) {
			const heading = document.createElement('div');
			heading.className = 'px-4 py-3 border-b border-line bg-surface2';
			if (data.title) text(heading, 'h3', data.title, 'text-sm font-800 text-cream');
			if (data.caption) text(heading, 'p', data.caption, 'text-xs text-muted mt-1');
			card.appendChild(heading);
		}
		const scroll = document.createElement('div');
		scroll.className = 'overflow-x-auto';
		const table = document.createElement('table');
		table.className = 'learning-table w-full text-left text-sm';
		const head = table.createTHead().insertRow();
		(data.columns || []).forEach((value) => {
			const cell = document.createElement('th'); cell.scope = 'col'; cell.textContent = value; head.appendChild(cell);
		});
		const body = table.createTBody();
		(data.rows || []).forEach((row) => {
			const tr = body.insertRow();
			(row || []).forEach((value) => { const cell = tr.insertCell(); cell.textContent = value; });
		});
		scroll.appendChild(table); card.appendChild(scroll);
		return card;
	}

	function mapCard(data) {
		const card = document.createElement('div');
		card.className = 'learning-mind-map border border-line bg-ink overflow-hidden';
		card.dataset.learningElement = 'mind_map';
		const heading = document.createElement('div');
		heading.className = 'flex items-center justify-between gap-3 px-4 py-3 border-b border-line bg-surface2';
		const title = document.createElement('div');
		if (data.title) text(title, 'h3', data.title, 'text-sm font-800 text-cream');
		if (data.caption) text(title, 'p', data.caption, 'text-xs text-muted mt-1');
		heading.appendChild(title);
		const controls = document.createElement('div'); controls.className = 'flex items-center gap-1';
		[['zoom-out', '−', 'Diminuir zoom'], ['reset', '⟳', 'Redefinir mapa'], ['zoom-in', '+', 'Aumentar zoom']].forEach(([action, label, aria]) => {
			const button = document.createElement('button'); button.type = 'button'; button.dataset.mindMapAction = action;
			button.setAttribute('aria-label', aria); button.className = 'learning-map-button'; button.textContent = label; controls.appendChild(button);
		});
		heading.appendChild(controls); card.appendChild(heading);
		const viewport = document.createElement('div'); viewport.className = 'learning-mind-map-viewport'; viewport.dataset.mindMapViewport = ''; viewport.tabIndex = 0; viewport.setAttribute('aria-label', 'Mapa mental interativo');
		const svg = document.createElementNS(SVG_NS, 'svg'); svg.classList.add('learning-mind-map-svg'); svg.dataset.mindMapSvg = ''; svg.setAttribute('role', 'img'); svg.setAttribute('aria-hidden', 'true'); viewport.appendChild(svg); card.appendChild(viewport);
		const details = document.createElement('details'); details.className = 'border-t border-line px-4 py-3';
		const summary = document.createElement('summary'); summary.className = 'cursor-pointer text-xs font-700 text-lamp'; summary.textContent = 'Ver outline acessível'; details.appendChild(summary);
		const outline = document.createElement('ul'); outline.className = 'mt-2 space-y-1 text-xs text-muted';
		(data.nodes || []).forEach((node) => { const li = document.createElement('li'); li.className = 'pl-2'; text(li, 'span', node.label, 'text-cream/90'); if (node.description) text(li, 'span', ' — ' + node.description); outline.appendChild(li); });
		details.appendChild(outline); card.appendChild(details); initMindMap(card, data);
		return card;
	}

	function initMindMap(root, data) {
		if (!root || root.dataset.mindMapReady) return;
		const svg = root.querySelector('[data-mind-map-svg]'); const viewport = root.querySelector('[data-mind-map-viewport]');
		const nodes = Array.isArray(data.nodes) ? data.nodes : [];
		if (!svg || !viewport || !nodes.length) return;
		root.dataset.mindMapReady = '1';
		const byID = Object.fromEntries(nodes.map((node) => [node.id, node]));
		const children = {}; nodes.forEach((node) => { if (node.parent_id) (children[node.parent_id] ||= []).push(node); });
		let expanded = new Set(nodes.map((node) => node.id)); let scale = 1; let panX = 0; let panY = 0;
		let dragging = false; let moved = false; let startX = 0; let startY = 0; let startPanX = 0; let startPanY = 0;
		const positions = {}; const rowHeight = 72; const width = Math.max(640, Math.min(1200, (nodes.length + 1) * 210));
		const isVisible = (node) => { let current = node; while (current.parent_id) { const parent = byID[current.parent_id]; if (!parent || !expanded.has(parent.id)) return false; current = parent; } return true; };
		function layout() {
			Object.keys(positions).forEach((key) => delete positions[key]); let row = 0;
			function place(node, depth) {
				if (!isVisible(node)) return;
				const kids = (children[node.id] || []).filter(isVisible);
				if (!kids.length) { positions[node.id] = { x: 30 + depth * 210, y: 35 + row * rowHeight }; row++; return; }
				kids.forEach((child) => place(child, depth + 1));
				const ys = kids.map((child) => positions[child.id].y); positions[node.id] = { x: 30 + depth * 210, y: (Math.min(...ys) + Math.max(...ys)) / 2 };
			}
			const rootNode = nodes.find((node) => !node.parent_id) || nodes[0]; place(rootNode, 0); return Math.max(140, row * rowHeight + 30);
		}
		function draw() {
			const height = layout(); svg.setAttribute('viewBox', '0 0 ' + width + ' ' + height); svg.setAttribute('width', width); svg.setAttribute('height', height); svg.replaceChildren();
			const group = document.createElementNS(SVG_NS, 'g'); group.setAttribute('transform', 'translate(' + panX + ' ' + panY + ') scale(' + scale + ')'); svg.appendChild(group);
			Object.keys(positions).forEach((id) => { const node = byID[id]; if (!node.parent_id || !positions[node.parent_id]) return; const from = positions[node.parent_id]; const to = positions[id]; const path = document.createElementNS(SVG_NS, 'path'); path.setAttribute('d', 'M ' + (from.x + 170) + ' ' + from.y + ' C ' + (from.x + 190) + ' ' + from.y + ', ' + (to.x - 20) + ' ' + to.y + ', ' + to.x + ' ' + to.y); path.classList.add('learning-map-edge'); group.appendChild(path); });
			Object.keys(positions).forEach((id) => {
				const node = byID[id]; const p = positions[id]; const groupNode = document.createElementNS(SVG_NS, 'g'); groupNode.classList.add('learning-map-node');
				const rect = document.createElementNS(SVG_NS, 'rect'); rect.setAttribute('x', p.x); rect.setAttribute('y', p.y - 25); rect.setAttribute('width', '170'); rect.setAttribute('height', '50'); rect.setAttribute('rx', '0'); groupNode.appendChild(rect);
				const label = document.createElementNS(SVG_NS, 'text'); label.setAttribute('x', p.x + 12); label.setAttribute('y', p.y + 5); label.textContent = node.label.length > 25 ? node.label.slice(0, 24) + '…' : node.label; groupNode.appendChild(label);
				if ((children[id] || []).length) { const badge = document.createElementNS(SVG_NS, 'text'); badge.setAttribute('x', p.x + 150); badge.setAttribute('y', p.y - 8); badge.classList.add('learning-map-badge'); badge.textContent = expanded.has(id) ? '−' : '+'; groupNode.appendChild(badge); }
				groupNode.addEventListener('click', () => { if (moved || !(children[id] || []).length) return; if (expanded.has(id)) expanded.delete(id); else expanded.add(id); draw(); }); group.appendChild(groupNode);
			});
		}
		function zoom(factor) { scale = Math.max(.45, Math.min(2.2, scale * factor)); draw(); }
		root.querySelectorAll('[data-mind-map-action]').forEach((button) => button.addEventListener('click', () => { const action = button.dataset.mindMapAction; if (action === 'zoom-in') zoom(1.2); else if (action === 'zoom-out') zoom(.83); else { scale = 1; panX = 0; panY = 0; expanded = new Set(nodes.map((node) => node.id)); draw(); } }));
		viewport.addEventListener('wheel', (event) => { event.preventDefault(); zoom(event.deltaY < 0 ? 1.1 : .9); }, { passive: false });
		viewport.addEventListener('pointerdown', (event) => { dragging = true; moved = false; startX = event.clientX; startY = event.clientY; startPanX = panX; startPanY = panY; viewport.setPointerCapture(event.pointerId); });
		viewport.addEventListener('pointermove', (event) => { if (!dragging) return; const dx = event.clientX - startX; const dy = event.clientY - startY; if (Math.abs(dx) + Math.abs(dy) > 4) moved = true; panX = startPanX + dx; panY = startPanY + dy; draw(); });
		viewport.addEventListener('pointerup', () => { dragging = false; });
		viewport.addEventListener('keydown', (event) => { if (event.key === '+' || event.key === '=') zoom(1.15); else if (event.key === '-') zoom(.87); else if (event.key === '0') { scale = 1; panX = 0; panY = 0; draw(); } });
		draw();
	}

	function hydrate(scope) {
		(scope || document).querySelectorAll('[data-mind-map]:not([data-mind-map-ready])').forEach((root) => {
			const json = root.querySelector('[data-mind-map-json]'); if (!json) return;
			try { initMindMap(root, JSON.parse(json.textContent)); } catch (_) { /* outline remains available */ }
		});
	}

	window.learningElementCard = function (data) { return data && data.type === 'table' ? tableCard(data) : (data && data.type === 'mind_map' ? mapCard(data) : null); };
	window.hydrateLearningElements = hydrate;
})();
