(function () {
	'use strict';

	const NS = 'http://www.w3.org/2000/svg';
	const NODE_WIDTH = 218;
	const NODE_HEIGHT = 58;
	const ROW_GAP = 26;
	const COL_GAP = 112;

	function svgElement(tag) {
		return document.createElementNS(NS, tag);
	}

	function textElement(tag, value, className) {
		const node = document.createElement(tag);
		if (className) node.className = className;
		node.textContent = value == null ? '' : String(value);
		return node;
	}

	function mapID() {
		return 'node-' + Date.now().toString(36) + '-' + Math.random().toString(36).slice(2, 7);
	}

	function childrenByParent(graph) {
		const children = Object.create(null);
		graph.nodes.forEach((node) => {
			if (node.parent_id) (children[node.parent_id] ||= []).push(node);
		});
		Object.values(children).forEach((items) => items.sort((a, b) => a.label.localeCompare(b.label)));
		return children;
	}

	function layoutGraph(graph) {
		const by = Object.fromEntries(graph.nodes.map((node) => [node.id, node]));
		const children = childrenByParent(graph);
		const depths = Object.create(null);
		let leafCount = 0;

		function collect(node, depth) {
			if (!node) return;
			depths[node.id] = depth;
			const kids = node.collapsed ? [] : (children[node.id] || []);
			if (!kids.length) {
				leafCount += 1;
				return;
			}
			kids.forEach((child) => collect(child, depth + 1));
		}
		collect(by[graph.root_id], 0);

		const positions = Object.create(null);
		let row = 0;
		function place(node) {
			if (!node) return;
			const kids = node.collapsed ? [] : (children[node.id] || []);
			if (!kids.length) {
				positions[node.id] = { x: 40 + depths[node.id] * (NODE_WIDTH + COL_GAP), y: 54 + row * (NODE_HEIGHT + ROW_GAP) };
				row += 1;
				return;
			}
			kids.forEach(place);
			const ys = kids.map((child) => positions[child.id].y);
			positions[node.id] = { x: 40 + depths[node.id] * (NODE_WIDTH + COL_GAP), y: (Math.min(...ys) + Math.max(...ys)) / 2 };
		}
		place(by[graph.root_id]);

		const maxDepth = Object.values(depths).length ? Math.max(...Object.values(depths)) : 0;
		return {
			by,
			children,
			positions,
			canvasWidth: Math.max(760, (maxDepth + 1) * (NODE_WIDTH + COL_GAP) + 80),
			canvasHeight: Math.max(360, Math.max(leafCount, 1) * (NODE_HEIGHT + ROW_GAP) + 80),
		};
	}

	function addNodeToGraph(graph, parentID, id) {
		if (!parentID || !graph.nodes.some((node) => node.id === parentID)) {
			throw new Error('Selecione um ramo pai antes de adicionar um nó');
		}
		const node = { id, parent_id: parentID, label: 'Novo conceito', description: '', metadata: {}, collapsed: false };
		graph.nodes.push(node);
		return node;
	}

	function init(root) {
		if (!root || root.dataset.mapReady) return;
		const svg = root.querySelector('[data-map-svg]');
		const viewport = root.querySelector('[data-map-viewport]');
		const inspector = root.querySelector('[data-map-inspector]');
		if (!svg || !viewport || !inspector) return;

		let graph;
		try { graph = JSON.parse(root.dataset.mapJson || ''); } catch (_) { return; }
		if (!graph || !Array.isArray(graph.nodes) || !graph.nodes.length) return;
		root.dataset.mapReady = '1';

		let selected = graph.root_id;
		let parentSelectionMode = false;
		let pendingParent = null;
		let scale = 1;
		let panX = 0;
		let panY = 0;
		let dragging = false;
		let moved = false;
		let startX = 0;
		let startY = 0;
		let startPanX = 0;
		let startPanY = 0;
		let positions = Object.create(null);
		let canvasWidth = 1200;
		let canvasHeight = 700;

		function visible(node, by) {
			let current = node;
			while (current && current.parent_id) {
				const parent = by[current.parent_id];
				if (!parent || parent.collapsed) return false;
				current = parent;
			}
			return true;
		}

		function truncate(label) {
			return label.length > 27 ? label.slice(0, 26) + '…' : label;
		}

		function draw() {
			const layout = layoutGraph(graph);
			const by = layout.by;
			const children = layout.children;
			positions = layout.positions;
			canvasWidth = layout.canvasWidth;
			canvasHeight = layout.canvasHeight;
			svg.setAttribute('viewBox', '0 0 ' + canvasWidth + ' ' + canvasHeight);
			svg.replaceChildren();
			const scene = svgElement('g');
			scene.setAttribute('transform', 'translate(' + panX + ' ' + panY + ') scale(' + scale + ')');
			svg.appendChild(scene);

			graph.nodes.forEach((node) => {
				if (!visible(node, by) || !node.parent_id || !positions[node.parent_id] || !positions[node.id]) return;
				const from = positions[node.parent_id];
				const to = positions[node.id];
				const path = svgElement('path');
				path.setAttribute('d', 'M ' + (from.x + NODE_WIDTH) + ' ' + from.y + ' C ' + (from.x + NODE_WIDTH + 58) + ' ' + from.y + ', ' + (to.x - 58) + ' ' + to.y + ', ' + to.x + ' ' + to.y);
				path.classList.add('mind-map-edge');
				scene.appendChild(path);
			});

			graph.nodes.forEach((node) => {
				if (!visible(node, by) || !positions[node.id]) return;
				const point = positions[node.id];
				const group = svgElement('g');
				group.classList.add('mind-map-node');
				if (node.id === selected) group.classList.add('is-selected');
				if (node.id === pendingParent) group.classList.add('is-parent-target');
				group.setAttribute('transform', 'translate(' + point.x + ' ' + (point.y - NODE_HEIGHT / 2) + ')');
				group.dataset.mapNodeId = node.id;
				group.setAttribute('tabindex', '0');
				group.setAttribute('role', 'button');
				group.setAttribute('aria-label', parentSelectionMode ? 'Selecionar ' + node.label + ' como pai do novo ramo' : node.label);
				const rect = svgElement('rect');
				rect.setAttribute('width', String(NODE_WIDTH));
				rect.setAttribute('height', String(NODE_HEIGHT));
				rect.setAttribute('rx', '0');
				group.appendChild(rect);
				const label = svgElement('text');
				label.setAttribute('x', '18');
				label.setAttribute('y', '27');
				label.textContent = truncate(node.label);
				group.appendChild(label);
				if (node.description) {
					const description = svgElement('text');
					description.setAttribute('x', '18');
					description.setAttribute('y', '46');
					description.classList.add('mind-map-description');
					description.textContent = truncate(node.description);
					group.appendChild(description);
				}
				if ((children[node.id] || []).length) {
					const badge = svgElement('text');
					badge.setAttribute('x', String(NODE_WIDTH - 22));
					badge.setAttribute('y', '35');
					badge.classList.add('mind-map-toggle');
					badge.setAttribute('role', 'button');
					badge.setAttribute('tabindex', '0');
					badge.setAttribute('aria-label', node.collapsed ? 'Expandir ramo' : 'Recolher ramo');
					badge.textContent = node.collapsed ? '+' : '−';
					const toggle = (event) => { event.stopPropagation(); node.collapsed = !node.collapsed; draw(); };
					badge.addEventListener('click', toggle);
					badge.addEventListener('keydown', (event) => { if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); toggle(event); } });
					group.appendChild(badge);
				}
				group.addEventListener('click', () => {
					if (moved) return;
					selected = node.id;
					if (parentSelectionMode) {
						pendingParent = node.id;
						setStatus('Pai selecionado: ' + node.label);
					}
					renderInspector();
					updateSelectionState();
					draw();
				});
				group.addEventListener('dblclick', () => { if ((children[node.id] || []).length) { node.collapsed = !node.collapsed; draw(); } });
				group.addEventListener('keydown', (event) => {
					if (event.key !== 'Enter' && event.key !== ' ') return;
					event.preventDefault();
					selected = node.id;
					if (parentSelectionMode) {
						pendingParent = node.id;
						setStatus('Pai selecionado: ' + node.label);
					}
					renderInspector();
					updateSelectionState();
					draw();
				});
				scene.appendChild(group);
			});
		}

		function setStatus(value, error) {
			const status = root.querySelector('[data-map-status]');
			if (status) { status.textContent = value; status.className = 'text-[11px] ' + (error ? 'text-coral' : 'text-muted'); }
		}

		function input(label, value, onChange, multiline) {
			const wrapper = document.createElement('label');
			wrapper.className = 'block space-y-1';
			wrapper.appendChild(textElement('span', label, 'block text-[11px] font-800 uppercase tracking-wider text-muted'));
			const field = document.createElement(multiline ? 'textarea' : 'input');
			field.className = 'input px-3 py-2 text-sm';
			field.value = value || '';
			if (multiline) field.rows = 4;
			field.addEventListener('input', () => { onChange(field.value); draw(); });
			wrapper.appendChild(field);
			return wrapper;
		}

		function renderInspector() {
			const node = graph.nodes.find((item) => item.id === selected) || graph.nodes[0];
			selected = node.id;
			inspector.replaceChildren();
			if (parentSelectionMode) {
				const message = pendingParent
					? 'Pai escolhido: ' + node.label + '. Clique em “Adicionar ramo” para criar o novo nó aqui.'
					: 'Modo de seleção ativo: clique no nó que será o pai do novo ramo.';
				inspector.appendChild(textElement('p', message, 'mind-map-selection-help border border-lamp bg-lamp/10 px-3 py-2 text-xs leading-relaxed text-lamp'));
			}
			inspector.appendChild(input('Título', node.label, (value) => { node.label = value; }, false));
			inspector.appendChild(input('Descrição', node.description, (value) => { node.description = value; }, true));
			const meta = textElement('p', node.parent_id ? 'Ramo de: ' + (graph.nodes.find((item) => item.id === node.parent_id) || {}).label : 'Raiz do estudo', 'text-[11px] text-muted leading-relaxed');
			inspector.appendChild(meta);
		}

		function zoom(factor) {
			scale = Math.max(.35, Math.min(2.6, scale * factor));
			draw();
		}

		function resetView() {
			scale = 1;
			panX = 0;
			panY = 0;
			draw();
		}

		const addButton = root.querySelector('[data-map-add]');
		const addButtonLabel = addButton?.querySelector('[data-map-add-label]');

		function updateAddButton() {
			if (!addButton) return;
			if (!parentSelectionMode) {
				if (addButtonLabel) addButtonLabel.textContent = 'Adicionar ramo';
				addButton.setAttribute('aria-label', 'Escolher pai para novo ramo');
				return;
			}
			if (pendingParent) {
				if (addButtonLabel) addButtonLabel.textContent = 'Adicionar neste pai';
				addButton.setAttribute('aria-label', 'Adicionar novo ramo no pai selecionado');
			} else {
				if (addButtonLabel) addButtonLabel.textContent = 'Cancelar seleção';
				addButton.setAttribute('aria-label', 'Cancelar seleção de pai');
			}
		}

		function addNode() {
			if (!parentSelectionMode) {
				parentSelectionMode = true;
				pendingParent = null;
				setStatus('Selecione o pai');
				renderInspector();
				updateSelectionState();
				draw();
				return;
			}
			if (!pendingParent) {
				parentSelectionMode = false;
				setStatus('Pronto');
				renderInspector();
				updateSelectionState();
				draw();
				return;
			}
			const id = mapID();
			addNodeToGraph(graph, pendingParent, id);
			selected = id;
			parentSelectionMode = false;
			pendingParent = null;
			setStatus('Novo ramo criado');
			renderInspector();
			updateSelectionState();
			draw();
		}

		function removeNode() {
			if (selected === graph.root_id) { setStatus('A raiz não pode ser removida', true); return; }
			if (!window.confirm('Remover este ramo e todos os seus descendentes?')) return;
			const removed = new Set([selected]);
			let changed = true;
			while (changed) { changed = false; graph.nodes.forEach((node) => { if (removed.has(node.parent_id) && !removed.has(node.id)) { removed.add(node.id); changed = true; } }); }
			graph.nodes = graph.nodes.filter((node) => !removed.has(node.id));
			selected = graph.root_id;
			renderInspector();
			draw();
		}

		async function save() {
			setStatus('Salvando…');
			try {
				const response = await fetch(root.dataset.mapUpdateUrl, { method: 'PUT', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': root.dataset.mapCsrf || '' }, body: JSON.stringify(graph) });
				if (!response.ok) throw new Error('Não foi possível salvar (' + response.status + ')');
				setStatus('Salvo agora');
			} catch (error) { setStatus(error.message || 'Falha ao salvar', true); }
		}

		addButton?.addEventListener('click', addNode);
		root.querySelector('[data-map-remove]')?.addEventListener('click', removeNode);
		document.querySelector('[data-map-save]')?.addEventListener('click', (event) => { event.preventDefault(); void save(); });
		viewport.addEventListener('wheel', (event) => { event.preventDefault(); zoom(event.deltaY < 0 ? 1.1 : .9); }, { passive: false });
		viewport.addEventListener('pointerdown', (event) => { dragging = true; moved = false; startX = event.clientX; startY = event.clientY; startPanX = panX; startPanY = panY; viewport.setPointerCapture(event.pointerId); });
		viewport.addEventListener('pointermove', (event) => { if (!dragging) return; const dx = event.clientX - startX; const dy = event.clientY - startY; if (Math.abs(dx) + Math.abs(dy) > 5) moved = true; panX = startPanX + dx; panY = startPanY + dy; draw(); });
		viewport.addEventListener('pointerup', () => { dragging = false; });
		viewport.addEventListener('pointercancel', () => { dragging = false; });
		viewport.addEventListener('keydown', (event) => { if (event.key === '+' || event.key === '=') zoom(1.15); else if (event.key === '-') zoom(.87); else if (event.key === '0') resetView(); });
		const controls = document.createElement('div');
		controls.className = 'mind-map-controls';
		[['−', () => zoom(.84), 'Diminuir zoom'], ['1:1', resetView, 'Recentrar mapa'], ['+', () => zoom(1.18), 'Aumentar zoom']].forEach(([label, action, aria]) => { const button = textElement('button', label); button.type = 'button'; button.title = aria; button.addEventListener('click', action); controls.appendChild(button); });
		viewport.appendChild(controls);
		const updateSelectionState = () => {
			viewport.classList.toggle('is-selecting-parent', parentSelectionMode);
			updateAddButton();
		};
		renderInspector();
		updateSelectionState();
		draw();
	}

	function boot() {
		document.querySelectorAll('[data-map-json]').forEach(init);
	}
	if (typeof module !== 'undefined' && module.exports) {
		module.exports = { addNodeToGraph, layoutGraph };
	} else if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot, { once: true });
	else boot();
})();
