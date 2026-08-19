// Safe runtime for the declarative learning-app contract.
// App data is treated as untrusted JSON: this file does not use innerHTML,
// eval, Function, network APIs, parent/top, or storage outside its namespace.
(function () {
	'use strict';

	const MAX_PAYLOAD_BYTES = 64 * 1024;
	const MAX_DEPTH = 8;
	const MAX_MODULES = 24;
	const MAX_LIST_ITEMS = 24;
	const MAX_STATE_FIELDS = 32;
	const MAX_TEXT_LENGTH = 4000;
	const MAX_INTERACTIONS = 100;
	const MAX_DURATION_MS = 15 * 60 * 1000;
	const MIN_DURATION_MS = 1000;
	const DEFAULT_INTERACTIONS = 60;
	const DEFAULT_DURATION_MS = 10 * 60 * 1000;
	const ID_RE = /^[a-z][a-z0-9_-]{0,63}$/;
	const STATE_KEY_RE = /^[A-Za-z][A-Za-z0-9_.-]{0,63}$/;
	const MODULES = Object.freeze(['text', 'quiz', 'flashcards', 'choice', 'sequence', 'progress']);
	const FORBIDDEN_KEYS = new Set(['__proto__', 'prototype', 'constructor']);

	function has(object, key) {
		return Object.prototype.hasOwnProperty.call(object, key);
	}

	function isObject(value) {
		return value !== null && typeof value === 'object' && !Array.isArray(value);
	}

	function unsafeText(value) {
		const lower = value.toLowerCase();
		return lower.includes('<') || lower.includes('>') || lower.includes('javascript:') ||
			lower.includes('vbscript:') || lower.includes('data:') || lower.includes('http://') ||
			lower.includes('https://') || lower.includes('ftp://') || lower.includes('//') ||
			lower.includes('onerror=') || lower.includes('onclick=') || lower.includes('onload=');
	}

	function checkText(value, name, required) {
		if (typeof value !== 'string') throw new Error(name + ' must be a string');
		if (required && value.trim() === '') throw new Error(name + ' cannot be blank');
		if ([...value].length > MAX_TEXT_LENGTH) throw new Error(name + ' is too long');
		if (unsafeText(value)) throw new Error(name + ' contains unsafe content');
	}

	function checkDepth(value, depth) {
		if (depth > MAX_DEPTH) throw new Error('JSON structure is too deep');
		if (Array.isArray(value)) {
			value.forEach((item) => checkDepth(item, depth + 1));
			return;
		}
		if (isObject(value)) {
			Object.keys(value).forEach((key) => {
				if (FORBIDDEN_KEYS.has(key)) throw new Error('forbidden object key: ' + key);
				checkDepth(value[key], depth + 1);
			});
		}
	}

	function scalar(value, name) {
		if (typeof value === 'string') {
			checkText(value, name, false);
			return;
		}
		if (typeof value === 'boolean') return;
		if (typeof value === 'number' && Number.isFinite(value)) return;
		throw new Error(name + ' must be a scalar');
	}

	function scalarOrList(value, name) {
		if (!Array.isArray(value)) {
			scalar(value, name);
			return;
		}
		if (value.length > MAX_LIST_ITEMS) throw new Error(name + ' has too many items');
		value.forEach((item, index) => scalar(item, name + '[' + index + ']'));
	}

	function stringList(params, key, min, max, moduleName) {
		if (!has(params, key) || !Array.isArray(params[key])) {
			throw new Error(moduleName + '.params.' + key + ' must be a list');
		}
		const values = params[key];
		if (values.length < min || values.length > max) {
			throw new Error(moduleName + '.params.' + key + ' has an invalid length');
		}
		values.forEach((value, index) => checkText(value, moduleName + '.params.' + key + '[' + index + ']', true));
		return values;
	}

	function integer(value, name) {
		if (!Number.isInteger(value)) throw new Error(name + ' must be an integer');
		return value;
	}

	function checkParams(module, index) {
		const params = module.params;
		if (!isObject(params)) throw new Error('module ' + (index + 1) + ' params are required');
		const allowed = {
			text: ['text'],
			quiz: ['question', 'options', 'answer', 'explanation'],
			flashcards: ['fronts', 'backs', 'shuffle'],
			choice: ['prompt', 'options', 'answer'],
			sequence: ['prompt', 'items', 'order'],
			progress: ['label', 'value', 'max'],
		}[module.type];
		Object.keys(params).forEach((key) => {
			if (!STATE_KEY_RE.test(key) || !allowed.includes(key)) throw new Error('unknown or invalid parameter: ' + key);
			scalarOrList(params[key], 'module ' + module.id + '.params.' + key);
		});
		const required = (key) => {
			if (!has(params, key)) throw new Error('module ' + module.id + '.params.' + key + ' is required');
		};
		switch (module.type) {
		case 'text':
			required('text'); checkText(params.text, module.id + '.params.text', true); break;
		case 'quiz': {
			required('question'); required('options'); required('answer');
			checkText(params.question, module.id + '.params.question', true);
			const options = stringList(params, 'options', 2, 6, module.id);
			integer(params.answer, module.id + '.params.answer');
			if (params.answer < 0 || params.answer >= options.length) throw new Error('quiz answer is out of range');
			if (has(params, 'explanation')) checkText(params.explanation, module.id + '.params.explanation', false);
			break;
		}
		case 'flashcards': {
			required('fronts'); required('backs');
			const fronts = stringList(params, 'fronts', 1, MAX_LIST_ITEMS, module.id);
			const backs = stringList(params, 'backs', 1, MAX_LIST_ITEMS, module.id);
			if (fronts.length !== backs.length) throw new Error('flashcards fronts and backs differ in length');
			if (has(params, 'shuffle') && typeof params.shuffle !== 'boolean') throw new Error('flashcards shuffle must be boolean');
			break;
		}
		case 'choice': {
			required('prompt'); required('options');
			checkText(params.prompt, module.id + '.params.prompt', true);
			const options = stringList(params, 'options', 2, 8, module.id);
			if (has(params, 'answer')) {
				integer(params.answer, module.id + '.params.answer');
				if (params.answer < 0 || params.answer >= options.length) throw new Error('choice answer is out of range');
			}
			break;
		}
		case 'sequence': {
			required('prompt'); required('items'); required('order');
			checkText(params.prompt, module.id + '.params.prompt', true);
			const items = stringList(params, 'items', 2, 8, module.id);
			if (!Array.isArray(params.order) || params.order.length !== items.length) throw new Error('sequence order has an invalid length');
			const seen = new Set();
			params.order.forEach((value) => {
				integer(value, module.id + '.params.order item');
				if (value < 0 || value >= items.length || seen.has(value)) throw new Error('sequence order is not a permutation');
				seen.add(value);
			});
			break;
		}
		case 'progress': {
			required('label'); required('value'); required('max');
			checkText(params.label, module.id + '.params.label', true);
			if (typeof params.value !== 'number' || !Number.isFinite(params.value) || typeof params.max !== 'number' || !Number.isFinite(params.max) || params.max <= 0 || params.value < 0 || params.value > params.max) throw new Error('progress values are invalid');
			break;
		}
		}
	}

	function validateApp(app) {
		if (!isObject(app)) throw new Error('app must be an object');
		checkDepth(app, 0);
		if (typeof app.id !== 'string' || !ID_RE.test(app.id)) throw new Error('app id is invalid');
		checkText(app.title, 'app title', true);
		if (has(app, 'prompt')) checkText(app.prompt, 'prompt', false);
		if (!isObject(app.lesson)) throw new Error('lesson must be an object');
		['subject', 'objective', 'level'].forEach((key) => { if (has(app.lesson, key)) checkText(app.lesson[key], 'lesson.' + key, false); });
		if (has(app.lesson, 'tags')) {
			if (!Array.isArray(app.lesson.tags) || app.lesson.tags.length > 12) throw new Error('lesson.tags is invalid');
			app.lesson.tags.forEach((tag, index) => checkText(tag, 'lesson.tags[' + index + ']', true));
		}
		if (!Array.isArray(app.modules) || app.modules.length < 1 || app.modules.length > MAX_MODULES) throw new Error('modules count is invalid');
		if (!isObject(app.initial_state)) throw new Error('initial_state is required');
		if (Object.keys(app.initial_state).length > MAX_STATE_FIELDS) throw new Error('initial_state has too many fields');
		Object.keys(app.initial_state).forEach((key) => {
			if (!STATE_KEY_RE.test(key)) throw new Error('initial_state key is invalid');
			scalarOrList(app.initial_state[key], 'initial_state.' + key);
		});
		if (has(app, 'limits')) {
			if (!isObject(app.limits)) throw new Error('limits must be an object');
			if (has(app.limits, 'max_interactions') && (!Number.isInteger(app.limits.max_interactions) || app.limits.max_interactions < 1 || app.limits.max_interactions > MAX_INTERACTIONS)) throw new Error('limits.max_interactions is invalid');
			if (has(app.limits, 'max_duration_ms') && (!Number.isInteger(app.limits.max_duration_ms) || app.limits.max_duration_ms < MIN_DURATION_MS || app.limits.max_duration_ms > MAX_DURATION_MS)) throw new Error('limits.max_duration_ms is invalid');
		}
		const ids = new Set();
		app.modules.forEach((module, index) => {
			if (!isObject(module) || typeof module.id !== 'string' || !ID_RE.test(module.id) || ids.has(module.id)) throw new Error('module id is invalid or duplicated');
			if (!MODULES.includes(module.type)) throw new Error('module type is not allowlisted: ' + module.type);
			ids.add(module.id);
			checkParams(module, index);
		});
		return app;
	}

	function element(tag, text) {
		const node = document.createElement(tag);
		if (text !== undefined) node.textContent = String(text);
		return node;
	}

	function button(label, onClick) {
		const node = element('button', label);
		node.type = 'button';
		node.addEventListener('click', onClick);
		return node;
	}

	function mount(root, source) {
		if (!root || typeof root.replaceChildren !== 'function') throw new Error('a DOM root is required');
		const app = validateApp(typeof source === 'string' ? JSON.parse(source) : source);
		const reduceMotion = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;
		const maxInteractions = app.limits && app.limits.max_interactions || DEFAULT_INTERACTIONS;
		const maxDuration = app.limits && app.limits.max_duration_ms || DEFAULT_DURATION_MS;
		const storageKey = 'learnix:learning-app:' + app.id + ':state';
		const state = Object.create(null);
		Object.keys(app.initial_state).forEach((key) => { state[key] = Array.isArray(app.initial_state[key]) ? app.initial_state[key].slice() : app.initial_state[key]; });
		try {
			const saved = window.localStorage.getItem(storageKey);
			if (saved !== null) {
				const parsed = JSON.parse(saved);
				if (isObject(parsed) && Object.keys(parsed).length <= MAX_STATE_FIELDS) Object.keys(parsed).forEach((key) => { if (STATE_KEY_RE.test(key)) scalarOrList(parsed[key], 'saved state.' + key); state[key] = parsed[key]; });
			}
		} catch (_) { /* unavailable or invalid persisted state: use initial state */ }

		let interactions = 0;
		let stopped = false;
		const startedAt = Date.now();
		let timer = null;
		const controller = {
			stop(message) {
				if (stopped) return;
				stopped = true;
				if (timer !== null) window.clearTimeout(timer);
				root.setAttribute('aria-disabled', 'true');
				const notice = element('p', message || 'Limite de interação atingido.');
				notice.setAttribute('role', 'status');
				root.appendChild(notice);
			},
			destroy() {
				stopped = true;
				if (timer !== null) window.clearTimeout(timer);
				root.replaceChildren();
			},
		};
		const persist = () => {
			try { window.localStorage.setItem(storageKey, JSON.stringify(state)); } catch (_) { /* persistence is optional */ }
		};
		const update = (key, value) => { scalarOrList(value, 'state.' + key); state[key] = value; persist(); };
		const interact = (action) => {
			if (stopped) return;
			if (Date.now() - startedAt >= maxDuration) { controller.stop('Tempo máximo da atividade atingido.'); return; }
			if (interactions >= maxInteractions) { controller.stop('Limite de interações atingido.'); return; }
			interactions += 1;
			action();
		};
		const valueFor = (key, fallback) => has(state, key) ? state[key] : fallback;
		const moduleKey = (module, suffix) => module.id + '.' + suffix;

		root.replaceChildren();
		root.dataset.reducedMotion = reduceMotion ? 'true' : 'false';
		root.removeAttribute('aria-disabled');
		const heading = element('h2', app.title);
		root.appendChild(heading);
		if (app.prompt) root.appendChild(element('p', app.prompt));
		const content = element('div');
		root.appendChild(content);

		app.modules.forEach((module) => {
			const section = element('section');
			section.dataset.moduleId = module.id;
			const title = element('h3', module.id);
			section.appendChild(title);
			content.appendChild(section);
			switch (module.type) {
			case 'text': renderText(section, module); break;
			case 'quiz': renderQuiz(section, module); break;
			case 'flashcards': renderFlashcards(section, module); break;
			case 'choice': renderChoice(section, module); break;
			case 'sequence': renderSequence(section, module); break;
			case 'progress': renderProgress(section, module); break;
			}
		});
		persist();
		timer = window.setTimeout(() => controller.stop('Tempo máximo da atividade atingido.'), maxDuration);

		function renderText(section, module) { section.appendChild(element('p', module.params.text)); }

		function renderQuiz(section, module) {
			section.appendChild(element('p', module.params.question));
			const feedback = element('p');
			feedback.setAttribute('role', 'status');
			module.params.options.forEach((option, index) => section.appendChild(button(option, (event) => interact(() => {
				const correct = index === module.params.answer;
				feedback.textContent = correct ? 'Correto.' : 'Tente novamente.';
				if (correct && module.params.explanation) feedback.textContent += ' ' + module.params.explanation;
				event.currentTarget.setAttribute('aria-pressed', correct ? 'true' : 'false');
				if (correct) section.querySelectorAll('button').forEach((item) => { item.disabled = true; });
			}))));
			section.appendChild(feedback);
		}

		function renderFlashcards(section, module) {
			const front = element('p');
			const back = element('p');
			const controls = element('div');
			const indexKey = moduleKey(module, 'index');
			const revealedKey = moduleKey(module, 'revealed');
			const render = () => {
				const index = Math.min(Number(valueFor(indexKey, 0)), module.params.fronts.length - 1);
				front.textContent = 'Frente: ' + module.params.fronts[index];
				back.textContent = valueFor(revealedKey, false) ? 'Verso: ' + module.params.backs[index] : 'Verso oculto';
			};
			controls.appendChild(button('Revelar', () => interact(() => { update(revealedKey, !valueFor(revealedKey, false)); render(); })));
			controls.appendChild(button('Próximo', () => interact(() => { update(indexKey, (Number(valueFor(indexKey, 0)) + 1) % module.params.fronts.length); update(revealedKey, false); render(); })));
			section.appendChild(front); section.appendChild(back); section.appendChild(controls); render();
		}

		function renderChoice(section, module) {
			section.appendChild(element('p', module.params.prompt));
			const feedback = element('p');
			module.params.options.forEach((option, index) => section.appendChild(button(option, () => interact(() => {
				update(moduleKey(module, 'selected'), index);
				feedback.textContent = has(module.params, 'answer') ? (index === module.params.answer ? 'Correto.' : 'Escolha registrada.') : 'Escolha registrada.';
			}))));
			section.appendChild(feedback);
		}

		function renderSequence(section, module) {
			section.appendChild(element('p', module.params.prompt));
			const feedback = element('p');
			const picks = [];
			const pickButtons = module.params.items.map((item, index) => button(item, (event) => interact(() => {
				if (picks.includes(index)) return;
				picks.push(index); event.currentTarget.disabled = true;
				if (picks.length === module.params.items.length) {
					feedback.textContent = picks.every((value, position) => value === module.params.order[position]) ? 'Sequência correta.' : 'Sequência registrada; tente novamente na próxima atividade.';
					update(moduleKey(module, 'complete'), true);
				}
			})));
			pickButtons.forEach((item) => section.appendChild(item)); section.appendChild(feedback);
		}

		function renderProgress(section, module) {
			section.appendChild(element('p', module.params.label));
			const progress = element('progress');
			progress.max = module.params.max; progress.value = module.params.value;
			progress.setAttribute('aria-label', module.params.label);
			section.appendChild(progress);
			section.appendChild(element('span', String(module.params.value) + '/' + String(module.params.max)));
		}

		return controller;
	}

	function showEmbeddedError(root, error) {
		root.replaceChildren(element('p', 'Não foi possível abrir esta atividade: ' + error.message));
	}

	function mountEmbedded() {
		document.querySelectorAll('script[type="application/json"][data-learning-app]').forEach((script) => {
			const targetID = script.getAttribute('data-target');
			const root = targetID ? document.getElementById(targetID) : null;
			if (!root) return;
			try { mount(root, script.getAttribute('data-app-json') || script.textContent || ''); } catch (error) { showEmbeddedError(root, error); }
		});
	}

	window.LearningAppsRuntime = Object.freeze({ mount, validate: validateApp });
	if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', mountEmbedded, { once: true });
	else mountEmbedded();
}());
