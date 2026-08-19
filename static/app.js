// Learnix client logic

const LEGACY_STORAGE_PREFIX = 'twstix';
const STORAGE_PREFIX = 'learnix';

function readMigratedStorage(key) {
	try {
		const current = localStorage.getItem(key);
		if (current !== null) return current;
		const legacyKey = key.replace(STORAGE_PREFIX, LEGACY_STORAGE_PREFIX);
		const legacy = localStorage.getItem(legacyKey);
		if (legacy !== null) localStorage.setItem(key, legacy);
		return legacy;
	} catch { return null; }
}

if (window.marked) {
	marked.setOptions({ gfm: true, breaks: true });
}

// Markdown output can carry AI- or web-injected HTML; every render goes
// through DOMPurify before touching the DOM.
function safeMarkdown(src) {
	const html = window.marked ? marked.parse(src) : String(src);
	return window.DOMPurify ? DOMPurify.sanitize(html) : html;
}

function renderMarkdown() {
	document.querySelectorAll('[data-render]').forEach((el) => {
		if (el.dataset.rendered) return;
		el.innerHTML = safeMarkdown(el.textContent);
		el.dataset.rendered = '1';
	});
}

function refreshIcons() {
	if (window.lucide) lucide.createIcons();
}

function formatTokenCount(value) {
	const n = Number(value);
	return Number.isFinite(n) && n > 0 ? n.toLocaleString('pt-BR') : '—';
}

function formatKnownTokenCount(value) {
	const n = Number(value);
	return Number.isFinite(n) && n >= 0 ? n.toLocaleString('pt-BR') : '—';
}

function updateTokenUsage(usage) {
	if (!usage) return;
	const total = Number(usage.token_total || usage.total_tokens || 0) ||
		(Number(usage.prompt_tokens || 0) + Number(usage.completion_tokens || 0));
	document.querySelectorAll('[data-token-total]').forEach((el) => {
		el.textContent = formatTokenCount(total);
	});
}

function appendStreamElement(host, data) {
	if (!window.learningElementCard || !host || !data) return;
	const card = window.learningElementCard(data);
	if (!card) return;
	let target = host.querySelector('[data-live-elements]');
	if (!target) {
		target = document.createElement('div');
		target.dataset.liveElements = '';
		target.className = 'learning-elements space-y-4 mt-5';
		host.appendChild(target);
	}
	target.appendChild(card);
}

function readWebOn() {
	return readMigratedStorage('learnix_web') !== '0';
}

/* ============================================================
 * Quiz feel: keyboard shortcuts, draft persistence, elapsed timer
 * ============================================================ */

// quizCard backs QuestionCard's Alpine state: option/confidence selection plus
// a per-question draft in localStorage (TODO #7) so a reload or accidental
// navigation never loses an unsubmitted answer.
function quizCard(quizID, idx) {
	return {
		selected: null,
		confidence: null,
		_draftKey: 'learnix:draft:' + quizID + ':' + idx,
		init() {
			try {
				const raw = localStorage.getItem(this._draftKey);
				if (!raw) return;
				const draft = JSON.parse(raw);
				if (draft && Number.isInteger(draft.selected) && draft.selected >= 0) this.selected = draft.selected;
				if (draft && Number.isInteger(draft.confidence) && draft.confidence >= 1 && draft.confidence <= 4) this.confidence = draft.confidence;
			} catch { /* private browsing or corrupt draft: start clean */ }
		},
		_saveDraft() {
			try { localStorage.setItem(this._draftKey, JSON.stringify({ selected: this.selected, confidence: this.confidence })); } catch { /* silent */ }
		},
		pick(oi) { this.selected = oi; this._saveDraft(); },
		setConfidence(v) { this.confidence = v; this._saveDraft(); },
	};
}

// quizTimer shows elapsed time on the current quiz. The start instant is kept
// in localStorage so reloading mid-quiz does not reset the clock.
function quizTimer(quizID) {
	return {
		label: '00:00',
		init() {
			const key = 'learnix:quiz-started:' + quizID;
			let startedAt = 0;
			try { startedAt = Number(localStorage.getItem(key)) || 0; } catch { /* silent */ }
			const now = Date.now();
			if (!startedAt || startedAt > now) {
				startedAt = now;
				try { localStorage.setItem(key, String(startedAt)); } catch { /* silent */ }
			}
			const tick = () => {
				const seconds = Math.max(0, Math.floor((Date.now() - startedAt) / 1000));
				const minutes = Math.floor(seconds / 60);
				this.label = String(minutes).padStart(2, '0') + ':' + String(seconds % 60).padStart(2, '0');
			};
			tick();
			this._timer = window.setInterval(tick, 1000);
		},
		destroy() { if (this._timer) window.clearInterval(this._timer); },
	};
}

// examBoard drives the exam-simulation surface: the countdown ticks down from
// the server-rendered remaining seconds, flips to coral in the final minute,
// and auto-delivers the attempt at zero. The popover state (open) guards the
// "Entregar prova" confirmation.
function examBoard(remaining) {
	return {
		left: Math.max(0, remaining),
		label: '--:--',
		low: false,
		open: false,
		init() {
			this._render();
			this._timer = window.setInterval(() => {
				this.left -= 1;
				if (this.left <= 0) {
					this.left = 0;
					this._render();
					this.submitExam();
					return;
				}
				this._render();
			}, 1000);
		},
		_render() {
			const h = Math.floor(this.left / 3600);
			const m = Math.floor((this.left % 3600) / 60);
			const s = this.left % 60;
			this.label = (h > 0 ? h + ':' : '') + String(m).padStart(2, '0') + ':' + String(s).padStart(2, '0');
			this.low = this.left < 60;
		},
		submitExam() {
			if (this._timer) window.clearInterval(this._timer);
			this._timer = null;
			const form = document.querySelector('form[data-exam-deliver]');
			if (form) form.submit();
		},
		destroy() { if (this._timer) window.clearInterval(this._timer); },
	};
}

// examCard backs ExamQuestion's Alpine state: selection only. Exam answers are
// persisted server-side on confirm and feedback waits for delivery, so there
// is no draft and no confidence axis.
function examCard(initial) {
	return {
		selected: Number.isInteger(initial) && initial >= 0 ? initial : null,
		pick(oi) { this.selected = oi; },
	};
}

// tutorDrawer powers the standalone-test tutor: one drawer per question,
// posting JSON to the attempt's /tutor endpoint and swapping in the rendered
// thread fragment the server returns.
function tutorDrawer(index, url) {
	return {
		index,
		url,
		open: false,
		busy: false,
		error: '',
		message: '',
		async send() {
			const text = this.message.trim();
			if (!text || this.busy) return;
			this.busy = true;
			this.error = '';
			try {
				const res = await fetch(this.url, {
					method: 'POST',
					headers: { 'Content-Type': 'application/json' },
					body: JSON.stringify({ index: this.index, message: text }),
				});
				const type = res.headers.get('Content-Type') || '';
				if (!res.ok) {
					let msg = 'Não foi possível falar com o tutor agora.';
					if (type.includes('application/json')) {
						try {
							const body = await res.json();
							if (body && body.error) msg = body.error;
						} catch { /* keep the default message */ }
					}
					this.error = msg;
					return;
				}
				const html = await res.text();
				const box = this.$el.querySelector('[data-tutor-thread]');
				if (box) {
					box.innerHTML = html;
					box.scrollTop = box.scrollHeight;
				}
				this.message = '';
			} catch {
				this.error = 'Erro de rede ao falar com o tutor.';
			} finally {
				this.busy = false;
			}
		},
	};
}

// clearQuizDrafts removes a single question draft (draftKey) or every draft and
// the elapsed-time anchor for a quiz ('learnix:draft:{quizID}:*' prefix).
function clearQuizDrafts(target) {
	if (!target) return;
	try {
		if (target.startsWith('learnix:draft:') && !target.endsWith(':*')) {
			localStorage.removeItem(target);
			return;
		}
		const prefix = target.replace(/\*$/, '');
		for (let i = localStorage.length - 1; i >= 0; i--) {
			const key = localStorage.key(i);
			if (key && key.startsWith(prefix)) localStorage.removeItem(key);
		}
	} catch { /* silent */ }
}

let quizDraftClearTarget = null;

// initQuizKeyboard drives the quiz without a mouse: 1–5/A–E pick an option,
// C/I/Q/T pick confidence, Enter confirms or advances. Clicks are used under
// the hood so Alpine state stays authoritative.
function initQuizKeyboard() {
	document.addEventListener('keydown', (e) => {
		if (e.metaKey || e.ctrlKey || e.altKey || e.shiftKey) return;
		const area = document.getElementById('quiz-area');
		if (!area) return;
		const active = document.activeElement;
		if (active && (active.matches('input:not([type="hidden"]), textarea, select, [contenteditable="true"]') || active.isContentEditable)) return;
		const optionIndex = '12345'.indexOf(e.key);
		if (optionIndex >= 0) {
			const btn = area.querySelectorAll('[data-quiz-option]')[optionIndex];
			if (btn) { e.preventDefault(); btn.click(); }
			return;
		}
		const key = e.key.toLowerCase();
		const letterIndex = 'abcde'.indexOf(key);
		if (letterIndex >= 0) {
			const btn = area.querySelectorAll('[data-quiz-option]')[letterIndex];
			if (btn) { e.preventDefault(); btn.click(); }
			return;
		}
		const exam = area.hasAttribute('data-exam');
		if (exam && key === 'm') {
			const flag = area.querySelector('[data-quiz-flag]');
			if (flag) { e.preventDefault(); flag.click(); }
			return;
		}
		if (exam && (e.key === 'ArrowLeft' || e.key === 'ArrowRight')) {
			const buttons = Array.from(area.querySelectorAll('[data-quiz-goto]'));
			if (buttons.length) {
				const current = buttons.findIndex((b) => b.getAttribute('aria-current') === 'true');
				const target = buttons[current + (e.key === 'ArrowRight' ? 1 : -1)];
				if (target) { e.preventDefault(); target.click(); }
			}
			return;
		}
		const confidenceBy = { c: '1', i: '2', q: '4', t: '3' };
		if (!exam && confidenceBy[key]) {
			const btn = area.querySelector('[data-quiz-confidence="' + confidenceBy[key] + '"]');
			if (btn) { e.preventDefault(); btn.click(); }
			return;
		}
		if (e.key === 'Enter') {
			if (active && active.tagName === 'BUTTON') return; // let a focused button keep its native behavior
			const submit = area.querySelector('[data-quiz-submit]');
			if (submit && !submit.disabled) { e.preventDefault(); submit.click(); return; }
			const next = area.querySelector('[data-quiz-next]');
			if (next) { e.preventDefault(); next.click(); }
		}
	});
}

function writeWebOn(v) {
	try { localStorage.setItem('learnix_web', v ? '1' : '0'); } catch { /* silent */ }
}

const BUILD_CHECK_INTERVAL_MS = 30000;

function currentBuildVersion() {
	const meta = document.querySelector('meta[name="learnix-build"]');
	return meta ? (meta.getAttribute('content') || '').trim() : '';
}

function showBuildUpdateNotice() {
	if (document.getElementById('build-update-notice')) return;
	const notice = document.createElement('aside');
	notice.id = 'build-update-notice';
	notice.className = 'fixed inset-x-3 bottom-[calc(5.5rem+env(safe-area-inset-bottom))] z-[80] border border-line bg-surface shadow-hard p-4 md:bottom-5 md:left-auto md:right-5 md:w-[min(24rem,calc(100vw-2rem))]';
	notice.setAttribute('role', 'status');
	notice.innerHTML = '<div class="flex items-start gap-3">' +
		'<span class="grid h-9 w-9 shrink-0 place-items-center bg-lamp text-ink border border-ink"><i data-lucide="refresh-cw" class="h-4 w-4"></i></span>' +
		'<div class="min-w-0 flex-1"><p class="text-sm font-800 text-cream">Nova versão disponível</p>' +
		'<p class="mt-1 text-xs leading-relaxed text-muted">Atualizamos o Learnix. Toque para continuar com a versão mais recente.</p>' +
		'<button type="button" data-build-update class="btn-primary mt-3 min-h-11 px-4 py-2 text-xs">Atualizar agora</button></div></div>';
	document.body.appendChild(notice);
	const update = notice.querySelector('[data-build-update]');
	if (update) update.addEventListener('click', () => window.location.reload());
	refreshIcons();
}

async function checkBuildVersion() {
	const current = currentBuildVersion();
	if (!current || current === 'dev') return;
	try {
		const response = await fetch('/version?check=' + Date.now(), {
			cache: 'no-store',
			headers: { Accept: 'text/plain', 'Cache-Control': 'no-store' },
		});
		if (!response.ok) return;
		const live = (await response.text()).trim();
		if (live && live !== current) showBuildUpdateNotice();
	} catch {
		// A temporary offline state must not interrupt study; retry later.
	}
}

function initBuildVersionCheck() {
	if (window.__learnixBuildVersionCheck) return;
	window.__learnixBuildVersionCheck = true;
	window.setInterval(checkBuildVersion, BUILD_CHECK_INTERVAL_MS);
	document.addEventListener('visibilitychange', () => {
		if (!document.hidden) checkBuildVersion();
	});
	checkBuildVersion();
}

function paintWebToggle() {
	const on = readWebOn();
	document.querySelectorAll('[data-web-toggle], #web-toggle').forEach((btn) => {
		btn.innerHTML = '<i data-lucide="' + (on ? 'globe' : 'globe-off') + '" class="w-4 h-4"></i>' + (btn.dataset.webToggle === 'mobile' ? '<span>Pesquisa na web</span><span class="text-xs text-muted">' + (on ? 'ligada' : 'desligada') + '</span>' : '');
		btn.classList.toggle('text-lamp', on);
		btn.classList.toggle('border-lamp', on);
		btn.classList.toggle('bg-lamp/10', on);
		btn.classList.toggle('text-muted', !on);
		btn.classList.toggle('border-line', !on);
		btn.setAttribute('aria-pressed', on ? 'true' : 'false');
		btn.classList.remove('opacity-0');
	});
	refreshIcons();
}

function toggleWeb() {
	writeWebOn(!readWebOn());
	paintWebToggle();
}

function escapeHTML(s) {
	return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

function showLoader(msg) {
	const l = document.getElementById('loader');
	if (!l) return;
	const t = document.getElementById('loader-text');
	if (t && msg) t.textContent = msg;
	l.classList.remove('hidden');
}

async function streamStudy(url, message, onToken, onDone, onError, onEvent) {
	try {
		const res = await fetch(url, {
			method: 'POST',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ message, web: readWebOn() }),
		});
		if (!res.ok || !res.body) {
			onError('Falha na conexão (' + res.status + ')');
			return;
		}
		const reader = res.body.getReader();
		const dec = new TextDecoder();
		let buf = '';
		let terminal = false;
		while (true) {
			const { done, value } = await reader.read();
			if (done) break;
			buf += dec.decode(value, { stream: true });
			let idx;
			while ((idx = buf.indexOf('\n\n')) !== -1) {
				const raw = buf.slice(0, idx);
				buf = buf.slice(idx + 2);
				const line = raw.replace(/^data:\s?/, '').trim();
				if (!line) continue;
				let data;
				try { data = JSON.parse(line); } catch { continue; }
				if (data.token) onToken(data.token);
				else if (data.error) { terminal = true; onError(data.error); return; }
				else if (data.done) { terminal = true; onDone(); return; }
				else if (onEvent) onEvent(data);
			}
		}
		if (terminal) return;
		onError('A conexão foi encerrada antes de concluir a resposta.');
	} catch (e) {
		onError(e.message || 'Erro de rede');
	}
}

const QUIZ_STAGES = [
	{ key: 'research', label: 'Pesquisando o tema', icon: 'search' },
	{ key: 'author', label: 'Criando as questões', icon: 'pen-line' },
	{ key: 'review', label: 'Revisando o quiz', icon: 'check-check' },
	{ key: 'save', label: 'Salvando o quiz', icon: 'database' },
];

const QUIZ_PRESETS = {
	fast: 'Rápido · ~5 questões',
	moderate: 'Moderado · ~10 questões',
	long: 'Longo · ~15 questões',
};

function openQuizChooser(baseURL) {
	const old = document.getElementById('quiz-chooser');
	if (old) old.remove();
	let selectedMode = 'practice';
	let selectedExam = false;
	const presets = [
		{ key: 'fast', icon: 'zap', name: 'Fast', desc: '~5 questões' },
		{ key: 'moderate', icon: 'gauge', name: 'Moderate', desc: '~10 questões', rec: true },
		{ key: 'long', icon: 'rocket', name: 'Long', desc: '~15 questões' },
	];
	const root = document.createElement('div');
	root.id = 'quiz-chooser';
	root.className = 'fixed inset-0 z-[60] grid place-items-center bg-ink/95 p-4';
	root.innerHTML =
		'<div class="pop relative w-full max-w-md border-2 border-line bg-surface shadow-hard p-7">' +
			'<button type="button" data-dismiss title="Fechar" class="absolute top-4 right-4 grid place-items-center w-8 h-8 border border-line bg-surface2 text-muted hover:text-coral hover:border-coral transition">' +
				'<i data-lucide="x" class="w-4 h-4"></i>' +
			'</button>' +
			'<div class="flex items-center gap-3.5 pr-10">' +
				'<span class="grid place-items-center w-14 h-14 bg-lamp text-ink border border-ink shadow-hard shrink-0">' +
					'<i data-lucide="graduation-cap" class="w-7 h-7"></i>' +
				'</span>' +
				'<div class="min-w-0">' +
					'<p class="font-display text-xl font-800 tracking-tight">Como você quer sua prova?</p>' +
					'<p class="text-xs text-muted mt-0.5">Escolha um tamanho para gerar o quiz.</p>' +
				'</div>' +
			'</div>' +
			'<div class="mt-6 border border-line bg-ink p-1.5 grid grid-cols-2 gap-1">' +
				'<button type="button" data-mode="practice" class="bg-lamp/15 text-lamp px-3 py-2.5 text-left transition">' +
					'<span class="flex items-center gap-2 text-xs font-800"><i data-lucide="book-open" class="w-4 h-4"></i> Prática</span>' +
					'<span class="block text-[10px] text-muted mt-1">Resultado privado</span>' +
				'</button>' +
				'<button type="button" data-mode="ranked" class="text-muted hover:text-cream px-3 py-2.5 text-left transition">' +
					'<span class="flex items-center gap-2 text-xs font-800"><i data-lucide="trophy" class="w-4 h-4"></i> Ranqueado</span>' +
					'<span class="block text-[10px] text-muted mt-1">Vale pontos públicos</span>' +
				'</button>' +
			'</div>' +
			'<div class="mt-3 border border-line bg-ink p-1.5 grid grid-cols-2 gap-1">' +
				'<button type="button" data-exam="off" class="bg-lamp/15 text-lamp px-3 py-2.5 text-left transition">' +
					'<span class="flex items-center gap-2 text-xs font-800"><i data-lucide="zap" class="w-4 h-4"></i> Feedback imediato</span>' +
					'<span class="block text-[10px] text-muted mt-1">Certo ou errado na hora</span>' +
				'</button>' +
				'<button type="button" data-exam="on" class="text-muted hover:text-cream px-3 py-2.5 text-left transition">' +
					'<span class="flex items-center gap-2 text-xs font-800"><i data-lucide="timer" class="w-4 h-4"></i> Simulado</span>' +
					'<span class="block text-[10px] text-muted mt-1">Tempo limite · correção só no fim</span>' +
				'</button>' +
			'</div>' +
			'<p data-mode-note class="text-[11px] text-muted mt-3">Escolha um tamanho para gerar seu quiz de prática.</p>' +
			'<div class="mt-3 grid gap-2.5">' +
				presets.map((p) =>
					'<button type="button" data-preset="' + p.key + '" class="group flex items-center gap-3.5 border px-4 py-3.5 text-left transition-all ' +
						(p.rec
							? 'border-lamp bg-lamp/10 shadow-hard-lamp'
							: 'border-line bg-ink hover:border-lamp') + '">' +
						'<span class="grid place-items-center w-10 h-10 shrink-0 ' +
							(p.rec
								? 'bg-lamp text-ink border border-ink'
								: 'bg-surface2 border border-line text-lamp') + '">' +
							'<i data-lucide="' + p.icon + '" class="w-5 h-5"></i>' +
						'</span>' +
						'<span class="flex-1 min-w-0">' +
							'<span class="flex items-center gap-2 text-sm font-800 ' + (p.rec ? 'text-cream' : 'text-cream/85') + '">' +
								p.name +
								(p.rec ? '<span class="bg-lamp/20 text-lamp text-[9px] font-800 px-2 py-0.5">Recomendado</span>' : '') +
							'</span>' +
							'<span class="block text-[11px] font-600 text-muted mt-0.5">' + p.desc + '</span>' +
						'</span>' +
						'<i data-lucide="chevron-right" class="w-4 h-4 text-muted group-hover:text-lamp shrink-0"></i>' +
					'</button>'
				).join('') +
			'</div>' +
		'</div>';
	document.body.appendChild(root);
	refreshIcons();
	const close = () => root.remove();
	root.addEventListener('click', (e) => { if (e.target === root) close(); });
	root.querySelector('[data-dismiss]').addEventListener('click', close);
	const chooserNote = () => {
		if (selectedExam) return 'Simulado estilo prova: navegação livre, cronômetro com tempo limite e correção só na entrega. Questões em branco contam como erro.';
		return selectedMode === 'ranked'
			? 'Sua nota será normalizada e publicada no leaderboard.'
			: 'Escolha um tamanho para gerar seu quiz de prática.';
	};
	root.querySelectorAll('[data-mode]').forEach((button) => {
		button.addEventListener('click', () => {
			selectedMode = button.dataset.mode || 'practice';
			root.querySelectorAll('[data-mode]').forEach((item) => {
				const active = item === button;
				item.classList.toggle('bg-lamp/15', active);
				item.classList.toggle('text-lamp', active);
				item.classList.toggle('text-muted', !active);
			});
			const note = root.querySelector('[data-mode-note]');
			if (note) note.textContent = chooserNote();
		});
	});
	root.querySelectorAll('[data-exam]').forEach((button) => {
		button.addEventListener('click', () => {
			selectedExam = button.dataset.exam === 'on';
			root.querySelectorAll('[data-exam]').forEach((item) => {
				const active = item === button;
				if (item.dataset.exam === 'on') {
					item.classList.toggle('bg-coral/15', active);
					item.classList.toggle('text-coral', active);
				} else {
					item.classList.toggle('bg-lamp/15', active);
					item.classList.toggle('text-lamp', active);
				}
				item.classList.toggle('text-muted', !active);
			});
			const note = root.querySelector('[data-mode-note]');
			if (note) note.textContent = chooserNote();
		});
	});
	root.querySelectorAll('[data-preset]').forEach((btn) => {
		btn.addEventListener('click', () => {
			const preset = btn.dataset.preset;
			close();
			startQuiz(baseURL, preset, readWebOn(), selectedMode, selectedExam);
		});
	});
}

const QUIZ_JOB_STORAGE = 'learnix_active_quiz_job';
let quizWatcher = null;

function readActiveQuizJob() {
	try { return JSON.parse(readMigratedStorage(QUIZ_JOB_STORAGE) || 'null'); } catch { return null; }
}

function writeActiveQuizJob(job) {
	try { localStorage.setItem(QUIZ_JOB_STORAGE, JSON.stringify(job)); } catch { /* private browsing */ }
}

function clearActiveQuizJob(jobID) {
	try {
		const current = readActiveQuizJob();
		if (!jobID || !current || current.job_id === jobID) localStorage.removeItem(QUIZ_JOB_STORAGE);
	} catch { /* private browsing */ }
}

function formatElapsed(startedAt) {
	const seconds = Math.max(0, Math.floor((Date.now() - new Date(startedAt || Date.now()).getTime()) / 1000));
	const minutes = Math.floor(seconds / 60);
	return String(minutes).padStart(2, '0') + ':' + String(seconds % 60).padStart(2, '0');
}

function quizToast(title, message, link, level) {
	let host = document.getElementById('toast-host');
	if (!host) {
		host = document.createElement('div');
		host.id = 'toast-host';
		host.className = 'fixed bottom-5 right-5 z-[90] w-[min(24rem,calc(100vw-2rem))] space-y-2';
		document.body.appendChild(host);
	}
	const toast = document.createElement('div');
	toast.className = 'pop border-2 ' + (level === 'error' ? 'border-coral bg-coral/10' : 'border-teal bg-surface') + ' shadow-hard p-4';
	toast.innerHTML =
		'<div class="flex items-start gap-3">' +
			'<span class="grid place-items-center w-9 h-9 ' + (level === 'error' ? 'bg-coral text-ink' : 'bg-teal text-ink') + ' shrink-0"><i data-lucide="' + (level === 'error' ? 'alert-triangle' : 'check') + '" class="w-4 h-4"></i></span>' +
			'<div class="min-w-0 flex-1"><p class="font-800 text-sm ' + (level === 'error' ? 'text-coral' : 'text-cream') + '">' + escapeHTML(title) + '</p>' +
			'<p class="text-xs text-muted mt-1 break-words">' + escapeHTML(message) + '</p>' +
			(link ? '<a href="' + escapeHTML(link) + '" class="inline-flex mt-3 text-xs font-800 text-lamp hover:text-cream">Abrir quiz <i data-lucide="arrow-right" class="w-3.5 h-3.5 ml-1"></i></a>' : '') +
			'</div><button type="button" data-dismiss class="text-muted hover:text-cream"><i data-lucide="x" class="w-4 h-4"></i></button>' +
		'</div>';
	toast.querySelector('[data-dismiss]').addEventListener('click', () => toast.remove());
	host.appendChild(toast);
	refreshIcons();
	setTimeout(() => toast.remove(), 12000);
}

function quizStatusView(root, preset) {
	const rows = {};
	root.querySelectorAll('[data-stage]').forEach((el) => { rows[el.dataset.stage] = el; });
	const msgEl = root.querySelector('[data-msg]');
	const activityEl = root.querySelector('[data-activity]');
	const statsEl = root.querySelector('[data-stats]');
	const errorEl = root.querySelector('[data-err]');
	const closeBtn = root.querySelector('[data-close]');
	const backgroundBtn = root.querySelector('[data-background]');
	const view = { root, detached: false, finished: false };

	const paint = (key, state) => {
		const el = rows[key];
		if (!el) return;
		const dot = el.querySelector('[data-dot]');
		const label = el.querySelector('[data-label]');
		const meta = QUIZ_STAGES.find((s) => s.key === key);
		if (state === 'done') {
			el.className = 'flex items-center gap-3 border border-teal bg-teal/10 px-4 py-3 transition-all duration-300';
			dot.className = 'grid place-items-center w-8 h-8 bg-teal text-ink shrink-0';
			dot.innerHTML = '<i data-lucide="check" class="w-4 h-4"></i>';
			label.className = 'text-sm font-700 text-teal';
		} else if (state === 'active') {
			el.className = 'flex items-center gap-3 border border-lamp bg-lamp/10 px-4 py-3 transition-all duration-300';
			dot.className = 'grid place-items-center w-8 h-8 bg-lamp/15 border border-lamp text-lamp shrink-0';
			dot.innerHTML = '<span class="w-3.5 h-3.5 border-2 border-lamp/40 border-t-lamp animate-spin"></span>';
			label.className = 'text-sm font-700 text-cream';
		} else {
			el.className = 'flex items-center gap-3 border border-line bg-ink px-4 py-3 transition-all duration-300';
			dot.className = 'grid place-items-center w-8 h-8 bg-surface2 border border-line text-muted shrink-0';
			dot.innerHTML = '<i data-lucide="' + meta.icon + '" class="w-4 h-4"></i>';
			label.className = 'text-sm font-700 text-muted';
		}
	};

	view.render = (data) => {
		const stage = data.stage === 'validate' ? 'review' : data.stage;
		const idx = QUIZ_STAGES.findIndex((s) => s.key === stage);
		QUIZ_STAGES.forEach((s, i) => paint(s.key, idx < 0 || i < idx ? 'done' : (i === idx ? 'active' : 'pending')));
		msgEl.textContent = data.message || 'A IA está trabalhando...';
		const heartbeatAt = new Date(data.heartbeat_at || data.updated_at || Date.now()).getTime();
		const heartbeatAge = Math.max(0, Math.floor((Date.now() - heartbeatAt) / 1000));
		statsEl.innerHTML =
			'<span><b>' + (data.current || 0) + '/' + (data.total || 0) + '</b> questões</span>' +
			'<span><b>' + (data.sources || 0) + '</b> fontes</span>' +
			'<span><b>' + (data.searches || 0) + '</b> buscas</span>' +
			'<span><b>' + (data.pages || 0) + '</b> páginas</span>' +
			'<span><b>' + formatKnownTokenCount(data.tokens) + '</b> tokens</span>' +
			'<span><b>' + formatElapsed(data.started_at) + '</b> decorridos</span>' +
			'<span class="col-span-2 sm:col-span-3 ' + (heartbeatAge > 12 ? 'text-lamp' : 'text-teal') + '"><b>' + (heartbeatAge > 12 ? 'Aguardando resposta...' : 'Ativo agora') + '</b> · última atualização há ' + heartbeatAge + 's</span>';
		const activities = (data.activities || []).slice(-14).reverse();
		activityEl.innerHTML = activities.map((a) =>
			'<div class="flex gap-2 text-[11px] ' + (a.level === 'error' ? 'text-coral' : (a.level === 'warning' ? 'text-lamp' : 'text-muted')) + '">' +
				'<span class="text-muted/50 shrink-0">' + new Date(a.at).toLocaleTimeString('pt-BR', { hour: '2-digit', minute: '2-digit', second: '2-digit' }) + '</span>' +
				'<span class="break-words">' + escapeHTML(a.message) + '</span></div>'
		).join('');
		if (data.level === 'error') msgEl.className = 'mt-5 min-h-4 text-center text-xs text-coral';
		else if (data.level === 'warning') msgEl.className = 'mt-5 min-h-4 text-center text-xs text-lamp';
		else msgEl.className = 'mt-5 min-h-4 text-center text-xs text-muted';
		refreshIcons();
	};

	view.fail = (message) => {
		if (view.finished) return;
		view.finished = true;
		msgEl.textContent = '';
		errorEl.innerHTML = '<div class="pop mt-4 flex items-center gap-3 border border-coral bg-coral/10 px-4 py-3.5 shadow-hard"><span class="grid place-items-center w-9 h-9 bg-coral text-ink shrink-0"><i data-lucide="alert-triangle" class="w-4 h-4"></i></span><div class="min-w-0"><span class="font-800 text-coral block">A geração falhou</span><span class="text-xs text-coral/80 break-words">' + escapeHTML(message) + '</span></div></div>';
		closeBtn.classList.remove('hidden');
		backgroundBtn.classList.add('hidden');
		refreshIcons();
	};
	backgroundBtn.addEventListener('click', () => {
		view.detached = true;
		root.remove();
		quizToast('Quiz rodando em segundo plano', 'Você pode continuar estudando. Avisaremos quando terminar.', null, 'success');
	});
	closeBtn.addEventListener('click', () => root.remove());
	return view;
}

function createQuizOverlay(baseURL, preset) {
	if (document.getElementById('quiz-overlay')) return null;
	const root = document.createElement('div');
	root.id = 'quiz-overlay';
	root.className = 'fixed inset-0 z-[60] grid place-items-center bg-ink/95 p-4';
	root.innerHTML = '<div class="pop w-full max-w-lg border-2 border-line bg-surface shadow-hard p-7">' +
		'<div class="flex items-center gap-3.5"><span class="grid place-items-center w-14 h-14 bg-lamp text-ink border border-ink shadow-hard shrink-0"><i data-lucide="lamp-desk" class="w-7 h-7 animate-pulse"></i></span><div class="min-w-0"><p class="font-display text-xl font-800 tracking-tight">Gerando seu quiz</p><p class="text-xs text-muted mt-0.5">' + escapeHTML(QUIZ_PRESETS[preset] || preset) + '</p></div></div>' +
		'<div class="mt-6 space-y-2.5">' + QUIZ_STAGES.map((s) => '<div data-stage="' + s.key + '" class="flex items-center gap-3 border border-line bg-ink px-4 py-3 transition-all duration-300"><span data-dot class="grid place-items-center w-8 h-8 bg-surface2 border border-line text-muted shrink-0"><i data-lucide="' + s.icon + '" class="w-4 h-4"></i></span><span data-label class="text-sm font-700 text-muted">' + s.label + '</span></div>').join('') + '</div>' +
		'<div data-stats class="mt-5 grid grid-cols-2 sm:grid-cols-3 gap-2 text-[11px] text-muted"></div>' +
		'<p data-msg class="mt-5 min-h-4 text-center text-xs text-muted/80">Preparando tudo...</p>' +
		'<div data-activity class="mt-3 max-h-40 space-y-1.5 overflow-y-auto no-scrollbar"></div><div data-err></div>' +
		'<button type="button" data-background class="btn-secondary mt-4 w-full px-4 py-3 text-sm"><i data-lucide="book-open" class="w-4 h-4"></i>Continuar estudando</button>' +
		'<button type="button" data-close class="hidden mt-3 w-full flex items-center justify-center gap-2 border border-coral bg-coral/5 text-coral text-sm font-800 px-4 py-3 hover:bg-coral/10 transition"><i data-lucide="x" class="w-4 h-4"></i>Fechar</button>' +
		'</div>';
	document.body.appendChild(root);
	refreshIcons();
	return quizStatusView(root, preset);
}

function finishQuizJob(job, data, view) {
	clearActiveQuizJob(job.job_id);
	if (data.status === 'succeeded') {
		if (view && !view.detached && !view.finished) {
			view.finished = true;
			view.render(data);
			view.root.querySelector('[data-msg]').textContent = 'Pronto! Redirecionando...';
			setTimeout(() => { window.location.href = data.redirect || job.base_url || '/'; }, 250);
		} else {
			quizToast('Seu quiz está pronto', 'A geração terminou enquanto você estudava.', data.redirect || job.base_url || '/', 'success');
		}
		return;
	}
	const error = data.error || 'A geração falhou sem informar o motivo.';
	if (view && !view.detached) view.fail(error);
	else quizToast('A geração falhou', error, null, 'error');
}

async function watchQuizJob(job, view) {
	if (quizWatcher && quizWatcher.jobID === job.job_id) return;
	quizWatcher = { jobID: job.job_id };
	let failures = 0;
	try {
		while (true) {
			let response;
			let data;
			try {
				response = await fetch(job.status_url, { cache: 'no-store' });
				data = await response.json();
				failures = 0;
			} catch {
				failures++;
				if (view && !view.detached) view.render({ ...job, message: failures >= 3 ? 'Reconectando ao progresso...' : 'Consultando o progresso...' });
				await new Promise((resolve) => setTimeout(resolve, Math.min(5000, 1000 * failures)));
				continue;
			}
			if (response.status === 404) {
				clearActiveQuizJob(job.job_id);
				const message = 'A geração foi interrompida antes de terminar, provavelmente por um reinício do servidor.';
				if (view && !view.detached) view.fail(message); else quizToast('Geração interrompida', message, null, 'error');
				return;
			}
			if (!response.ok || !data) {
				if (view && !view.detached) view.render({ ...job, message: 'O servidor respondeu com erro; tentando novamente...' });
				await new Promise((resolve) => setTimeout(resolve, 3000));
				continue;
			}
			if (view && !view.detached) view.render(data);
			if (data.status === 'succeeded' || data.status === 'failed') {
				finishQuizJob(job, data, view);
				return;
			}
			await new Promise((resolve) => setTimeout(resolve, 1000));
		}
	} finally {
		if (quizWatcher && quizWatcher.jobID === job.job_id) quizWatcher = null;
	}
}

async function startQuiz(baseURL, preset, web, mode, exam) {
	const view = createQuizOverlay(baseURL, preset);
	if (!view) return;
	try {
		const res = await fetch(baseURL + '/quiz/start', {
			method: 'POST', headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ preset, mode: mode || 'practice', web: web !== false, exam: exam === true }),
		});
		let started;
		try { started = await res.json(); } catch { started = null; }
		if (!res.ok || !started || !started.status_url) {
			view.fail((started && started.error) || 'Falha ao iniciar a geração (' + res.status + ')');
			return;
		}
		const job = { job_id: started.job_id, status_url: started.status_url, base_url: baseURL, preset, mode: mode || 'practice', started_at: new Date().toISOString() };
		writeActiveQuizJob(job);
		watchQuizJob(job, view);
	} catch (e) {
		view.fail(e.message || 'Erro de rede');
	}
}

async function startStandaloneTest(baseURL, preset, mode) {
	const view = createQuizOverlay(baseURL, preset);
	if (!view) return;
	try {
		const res = await fetch(baseURL + '/start', { method: 'POST' });
		let started;
		try { started = await res.json(); } catch { started = null; }
		if (!res.ok || !started || !started.status_url) {
			view.fail((started && started.error) || 'Falha ao iniciar a tentativa (' + res.status + ')');
			return;
		}
		const job = { job_id: started.job_id, status_url: started.status_url, base_url: baseURL, preset, mode: mode || 'practice', started_at: new Date().toISOString() };
		writeActiveQuizJob(job);
		watchQuizJob(job, view);
	} catch (e) {
		view.fail(e.message || 'Erro de rede');
	}
}

function initTestStart() {
	document.querySelectorAll('[data-test-start]').forEach((button) => {
		button.addEventListener('click', () => startStandaloneTest(button.dataset.testUrl, button.dataset.preset || 'moderate', button.dataset.mode || 'practice'));
	});
}

function resumeActiveQuiz() {
	const job = readActiveQuizJob();
	if (job && job.job_id && job.status_url) watchQuizJob(job, null);
}

/* ============================================================
 * Mobile shell: bottom navigation, keyboard-aware viewport and pane state
 * ============================================================ */

function mobileShell() {
	return {
		mobileMenuOpen: false,
		activePane: 'chat',
		init() {
			this.setViewportHeight();
			const viewport = window.visualViewport;
			if (viewport) viewport.addEventListener('resize', () => this.setViewportHeight(), { passive: true });
			window.addEventListener('resize', () => this.setViewportHeight(), { passive: true });
			document.addEventListener('focusin', () => this.setViewportHeight(), { passive: true });
			document.addEventListener('focusout', () => window.setTimeout(() => this.setViewportHeight(), 0), { passive: true });
			window.addEventListener('mobile-pane-change', (event) => {
				if (event.detail && event.detail.pane) this.activePane = event.detail.pane;
			});
			this.$watch('mobileMenuOpen', (open) => {
				if (open) this.$nextTick(() => document.querySelector('[role="dialog"] button[aria-label="Fechar"]')?.focus());
			});
		},
		selectMobilePane(pane) {
			this.activePane = pane;
			this.mobileMenuOpen = false;
			window.dispatchEvent(new CustomEvent('mobile-pane', { detail: { pane } }));
		},
		setViewportHeight() {
			const height = window.visualViewport ? window.visualViewport.height : window.innerHeight;
			document.documentElement.style.setProperty('--learnix-viewport-height', Math.round(height) + 'px');
			const active = document.activeElement;
			const editable = active && (active.matches('input, textarea, select, [contenteditable="true"]') || active.isContentEditable);
			const keyboardOpen = Boolean(editable && window.innerHeight - height > 120);
			document.body.classList.toggle('keyboard-open', keyboardOpen);
		},
	};
}

function workspaceMobile() {
	return {
		pane: 'chat',
		studyID: '',
		init() {
			const data = this.$el.dataset || {};
			this.studyID = data.mobileStudyId || '';
			const allowed = ['chat', 'files', 'editor'];
			const requested = allowed.includes(data.mobilePane) ? data.mobilePane : '';
			const saved = this.studyID ? readMigratedStorage('learnix:mobile-pane:' + this.studyID) : null;
			this.pane = requested || (allowed.includes(saved) ? saved : 'chat');
			this.$watch('pane', (value) => {
				if (!allowed.includes(value)) { this.pane = 'chat'; return; }
				try { if (this.studyID) localStorage.setItem('learnix:mobile-pane:' + this.studyID, value); } catch { /* silent */ }
				window.dispatchEvent(new CustomEvent('mobile-pane-change', { detail: { pane: value } }));
			});
			window.dispatchEvent(new CustomEvent('mobile-pane-change', { detail: { pane: this.pane } }));
		},
	};
}

function chatClientKey() {
		try {
			if (window.crypto && window.crypto.randomUUID) return window.crypto.randomUUID();
		} catch { /* private browsing */ }
		return String(Date.now()) + '-' + Math.random().toString(16).slice(2);
}

function chatPendingKey(chatID) {
	return 'learnix:chat-pending:' + chatID;
}

function readPendingChat(chatID) {
	try {
		const raw = readMigratedStorage(chatPendingKey(chatID));
		return raw ? JSON.parse(raw) : null;
	} catch { return null; }
}

function writePendingChat(chatID, value) {
	try { localStorage.setItem(chatPendingKey(chatID), JSON.stringify(value)); } catch { /* private browsing */ }
}

function clearPendingChat(chatID, clientKey) {
	try {
		const current = readPendingChat(chatID);
		if (!clientKey || !current || current.client_key === clientKey) localStorage.removeItem(chatPendingKey(chatID));
	} catch { /* private browsing */ }
}

function workspaceChat() {
	return {
		draft: '',
		busy: false,
		stickToBottom: true,
		showSaved: false,
		turnsURL: '',
		chatListURL: '',
		scroller: window,
		scrollCleanup: null,
		pollers: {},
		refreshing: false,
		init() {
			const d = this.$el && this.$el.dataset ? this.$el.dataset : {};
			if (d.initialPrompt) this.draft = d.initialPrompt;
			this.turnsURL = d.turnsUrl || '';
			this.chatListURL = d.chatListUrl || '';
			this._detectScroller();
			this._bindScrollGuard();
			this._bindLifecycle();
			this.$el.addEventListener('chat-pane-updated', () => {
				this._detectScroller();
				this._bindScrollGuard();
				this.reconcile();
			});
			this.reconcile();
		},
		_bindLifecycle() {
			this._onVisible = () => { if (document.visibilityState === 'visible') this.reconcile(); };
			this._onOnline = () => this.reconcile();
			this._onPageShow = () => this.reconcile();
			document.addEventListener('visibilitychange', this._onVisible);
			window.addEventListener('online', this._onOnline);
			window.addEventListener('pageshow', this._onPageShow);
		},
		_turnURL(turnID) { return this.turnsURL + '/' + encodeURIComponent(turnID); },
		_retryURL(turnID) { return this._turnURL(turnID) + '/retry'; },
		_setBusy(value) { this.busy = value; },
		async reconcile() {
			if (!this.turnsURL || this.refreshing) return;
			const pending = readPendingChat(this.$el.dataset.chatId || '');
			if (pending && pending.client_key && pending.message) {
				this.submitTurn(pending.message, pending.client_key, true);
			}
			const active = this.$el.querySelectorAll('[data-turn-id][data-turn-status="queued"], [data-turn-id][data-turn-status="running"]');
			if (active.length) this.appendThinking();
			active.forEach((el) => this.pollTurn(el.dataset.turnId));
		},
		async submitTurn(message, clientKey, fromRecovery) {
			if (!message || !clientKey || this.pollers[clientKey]) return;
			this.pollers[clientKey] = true;
			this._setBusy(true);
			try {
				const res = await fetch(this.turnsURL, {
					method: 'POST',
					headers: { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' },
					body: JSON.stringify({ message, client_key: clientKey, web: readWebOn() }),
				});
				let data = null;
				try { data = await res.json(); } catch { /* empty response */ }
				if (!res.ok || !data || !data.turn_id) {
					const error = new Error((data && data.error) || friendlyChatHTTPError(res.status));
					error.status = res.status;
					throw error;
				}
				writePendingChat(this.$el.dataset.chatId || '', { client_key: clientKey, message });
				this.pollTurn(data.turn_id, clientKey, message);
			} catch (e) {
				this._setBusy(false);
				if (e.status >= 400 && e.status < 500) clearPendingChat(this.$el.dataset.chatId || '', clientKey);
				if (!fromRecovery) this.showTurnError(e.message || 'Não foi possível enviar a mensagem.', message, clientKey);
			} finally {
				delete this.pollers[clientKey];
			}
		},
		pollTurn(turnID, clientKey, message) {
			if (!turnID || this.pollers['turn:' + turnID]) return;
			this.pollers['turn:' + turnID] = { started: Date.now(), delay: 800 };
			const poll = async () => {
				const state = this.pollers['turn:' + turnID];
				if (!state) return;
				try {
					const res = await fetch(this._turnURL(turnID), { cache: 'no-store', headers: { Accept: 'application/json' } });
					const data = await res.json();
					if (res.status === 404) throw new Error('Este turno não está mais disponível.');
					if (!res.ok) throw new Error(data.error || friendlyChatHTTPError(res.status));
					if (data.status === 'succeeded') {
						clearPendingChat(this.$el.dataset.chatId || '', data.client_key || clientKey);
						delete this.pollers['turn:' + turnID];
						this._setBusy(false);
						this.removeThinking();
						this.refreshChatPane();
						if (window.htmx) htmx.trigger(document.body, 'refreshFiles');
						return;
					}
					if (data.status === 'failed') {
						delete this.pollers['turn:' + turnID];
						this._setBusy(false);
						clearPendingChat(this.$el.dataset.chatId || '', data.client_key || clientKey);
						this.removeThinking();
						this.refreshChatPane();
						return;
					}
					state.delay = Math.min(4000, Math.round(state.delay * 1.35));
				} catch (e) {
					state.delay = Math.min(5000, Math.round(state.delay * 1.7));
					if (Date.now() - state.started > 45000) {
						this._setBusy(false);
						this.showTurnError('A conexão foi perdida. O turno continua salvo; tente atualizar ou repetir.', message, clientKey);
					}
				}
				if (this.pollers['turn:' + turnID]) setTimeout(poll, state.delay);
			};
			poll();
		},
		refreshChatPane() {
			if (this.refreshing || !this.chatListURL || !window.htmx) return;
			this.refreshing = true;
			Promise.resolve(htmx.ajax('GET', this.chatListURL, { target: '#chat-pane', select: '#chat-pane > *', swap: 'innerHTML' }))
				.finally(() => { this.refreshing = false; });
		},
		showTurnError(message, original, clientKey) {
			this.removeThinking();
			const chat = this.$el.querySelector('#chat');
			if (!chat) return;
			const existing = Array.from(chat.querySelectorAll('[data-client-key]')).find((el) => el.dataset.clientKey === (clientKey || ''));
			if (existing) return;
			const wrap = document.createElement('div');
			wrap.className = 'flex justify-end rise';
			wrap.dataset.clientKey = clientKey || '';
			wrap.innerHTML = '<div class="max-w-[88%] border border-coral bg-coral/10 px-4 py-3 text-xs text-coral"><p>' + escapeHTML(message) + '</p><button type="button" data-retry class="mt-2 inline-flex items-center gap-1 border border-coral px-2.5 py-1 font-800 hover:bg-coral/20"><i data-lucide="refresh-cw" class="w-3 h-3"></i>Tentar novamente</button></div>';
			wrap.querySelector('[data-retry]').addEventListener('click', () => this.submitTurn(original, clientKey, false));
			chat.appendChild(wrap);
			refreshIcons();
			this.scrollToBottom();
		},
		retryTurn(turnID) {
			if (!turnID || this.busy) return;
			this._setBusy(true);
			this.appendThinking();
			fetch(this._retryURL(turnID), { method: 'POST', headers: { 'Cache-Control': 'no-store' } })
				.then(async (res) => {
					const data = await res.json().catch(() => null);
					if (!res.ok || !data) throw new Error((data && data.error) || friendlyChatHTTPError(res.status));
					this.pollTurn(data.turn_id);
				})
				.catch((e) => { this._setBusy(false); this.removeThinking(); quizToast('Não foi possível repetir', e.message || 'Tente novamente em instantes.', null, 'error'); });
		},
		_detectScroller() {
			const pane = this.$el && this.$el.querySelector ? this.$el.querySelector('[data-chat-scroll]') : null;
			if (pane) { this.scroller = pane; return; }
			if (this.$el) {
				const ov = getComputedStyle(this.$el).overflowY;
				if (ov === 'auto' || ov === 'scroll') { this.scroller = this.$el; return; }
			}
			this.scroller = window;
		},
		_bindScrollGuard() {
			if (this.scrollCleanup) {
				this.scrollCleanup();
				this.scrollCleanup = null;
			}
			if (this.scroller === window) {
				this._lastY = window.scrollY;
				const onScroll = () => {
					const y = window.scrollY;
					if (this.atBottom()) this.stickToBottom = true;
					else if (y < this._lastY) this.stickToBottom = false;
					this._lastY = y;
				};
				window.addEventListener('scroll', onScroll, { passive: true });
				this.scrollCleanup = () => window.removeEventListener('scroll', onScroll);
				return;
			}
			const scroller = this.scroller;
			this._lastY = scroller.scrollTop;
			const onScroll = () => {
				const y = scroller.scrollTop;
				if (this.atBottom()) this.stickToBottom = true;
				else if (y < this._lastY) this.stickToBottom = false;
				this._lastY = y;
			};
			scroller.addEventListener('scroll', onScroll, { passive: true });
			this.scrollCleanup = () => scroller.removeEventListener('scroll', onScroll);
		},
		atBottom() {
			if (this.scroller === window) {
				return window.innerHeight + window.scrollY >= document.documentElement.scrollHeight - 60;
			}
			const el = this.scroller;
			return el.scrollTop + el.clientHeight >= el.scrollHeight - 60;
		},
		scrollToBottom() {
			if (!this.stickToBottom) return;
			if (this.scroller === window) {
				window.scrollTo({ top: document.documentElement.scrollHeight, behavior: 'instant' });
			} else {
				this.scroller.scrollTop = this.scroller.scrollHeight;
			}
		},
		send() {
			const text = this.draft.trim();
			if (!text || this.busy) return;
			const clientKey = chatClientKey();
			writePendingChat(this.$el.dataset.chatId || '', { client_key: clientKey, message: text });
			this.appendUser(text);
			this.appendThinking();
			this.draft = '';
			this.stickToBottom = true;
			this.submitTurn(text, clientKey, false);
		},
		useSuggestion(text) {
			if (this.busy) return;
			this.draft = text;
			this.send();
		},
		clearAnchor() {
			const a = document.getElementById('stream-anchor');
			if (a) a.remove();
		},
		appendUser(text) {
			const chat = document.getElementById('chat');
			this.clearAnchor();
			const wrap = document.createElement('div');
			wrap.className = 'flex justify-end rise';
			const b = document.createElement('div');
			b.className = 'max-w-[80%] bg-surface2 border border-line px-4 py-3 text-cream whitespace-pre-wrap shadow-hard';
			b.textContent = text;
			wrap.appendChild(b);
			chat.appendChild(wrap);
		},
		// Visible "the model is working" signal while a turn is queued/running:
		// three bouncing dots reuse the brutalist .tdot animation.
		appendThinking() {
			const chat = this.$el.querySelector('#chat');
			if (!chat || chat.querySelector('#chat-thinking')) return;
			const wrap = document.createElement('div');
			wrap.id = 'chat-thinking';
			wrap.className = 'flex justify-start rise';
			wrap.innerHTML = '<div class="flex items-center gap-3 border border-line bg-surface2/50 px-4 py-3">' +
				'<span class="flex items-center gap-1"><span class="tdot"></span><span class="tdot"></span><span class="tdot"></span></span>' +
				'<span class="text-[10px] font-800 uppercase tracking-[0.16em] text-muted">Pensando…</span>' +
				'</div>';
			chat.appendChild(wrap);
			this.scrollToBottom();
		},
		removeThinking() {
			const el = this.$el.querySelector('#chat-thinking');
			if (el) el.remove();
		},
	};
}

function friendlyChatHTTPError(status) {
	if (status === 401 || status === 403) return 'Sua sessão expirou. Recarregue a página e entre novamente.';
	if (status === 429) return 'Aguarde um instante antes de enviar outra pergunta.';
	if (status >= 500) return 'O servidor está indisponível no momento. Tente novamente.';
	return 'Não foi possível confirmar a mensagem.';
}

/* ============================================================
 * Workspace: markdown note editor with autosave + live preview
 * ============================================================ */

function fmtTime(d) {
	return d.toLocaleTimeString('pt-BR', { hour: '2-digit', minute: '2-digit' });
}

function noteEditor() {
	return {
		content: '',
		preview: '',
		dirty: false,
		saving: false,
		savedAt: null,
		showHistory: false,
		mode: 'split',
		desktop: true,
		_timer: null,
		init() {
			this.content = this.$el.dataset.content || '';
			this.renderPreview();
			const mq = window.matchMedia('(min-width: 768px)');
			this.desktop = mq.matches;
			mq.addEventListener('change', (e) => { this.desktop = e.matches; if (!this.desktop && this.mode === 'split') this.mode = 'preview'; });
			try {
				const m = readMigratedStorage('learnix:editor-view');
				if (m === 'split' || m === 'preview' || m === 'source') this.mode = m;
			} catch { /* defaults */ }
			if (!this.desktop && this.mode === 'split') this.mode = 'preview';
			this.$watch('content', () => {
				this.dirty = true;
				this.renderPreview();
				this.scheduleSave();
			});
			window.addEventListener('pagehide', () => this.save(), { once: true });
		},
		cycleMode() {
			this.mode = !this.desktop
				? (this.mode === 'preview' ? 'source' : 'preview')
				: (this.mode === 'split' ? 'preview' : (this.mode === 'preview' ? 'source' : 'split'));
			try { localStorage.setItem('learnix:editor-view', this.mode); } catch { /* silent */ }
		},
		modeLabel() {
			return this.mode === 'split' ? 'Dividido' : (this.mode === 'preview' ? 'Preview' : 'Fonte');
		},
		gridStyle() {
			const v = this.mode === 'preview' ? '0fr 1fr' : (this.mode === 'source' ? '1fr 0fr' : '1fr 1fr');
			return this.desktop ? 'grid-template-columns:' + v : 'grid-template-rows:' + v;
		},
		renderPreview() {
			this.preview = window.marked ? safeMarkdown(this.content || '') : this.content;
		},
		scheduleSave() {
			clearTimeout(this._timer);
			this._timer = setTimeout(() => this.save(), 800);
		},
		save() {
			if (!this.dirty || this.saving) return;
			this.saving = true;
			fetch(this.$el.dataset.saveUrl, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ content: this.content }),
			})
				.then((r) => {
					if (!r.ok) throw new Error('save failed');
					return r.json();
				})
				.then(() => {
					this.dirty = false;
					this.saving = false;
					this.savedAt = new Date();
				})
				.catch(() => { this.saving = false; });
		},
		onKeydown(e) {
			if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 's') {
				e.preventDefault();
				clearTimeout(this._timer);
				this.save();
			}
		},
	};
}

/* ============================================================
 * Workspace: drag-to-reorder + drag-to-resize panes
 * ============================================================ */

const WS_DEFAULTS = { order: ['files', 'editor', 'chat'], widths: { files: 0.22, editor: 0.50, chat: 0.28 } };
const WS_MIN = 0.12;
const WS_KEY = 'learnix:ws-layout';
const WS_META = { files: { icon: 'folder-tree', label: 'Arquivos' }, editor: { icon: 'notebook-pen', label: 'Editor' }, chat: { icon: 'message-circle', label: 'Chat' } };

function workspaceLayout() {
	return {
		order: [...WS_DEFAULTS.order],
		widths: { ...WS_DEFAULTS.widths },
		desktop: false,
		drag: null,
		resizing: null,
		dropX: null,
		dropSlot: null,
		_saveTimer: null,
		_pending: null,
		_ghost: null,
		_inited: false,

		init() {
			if (this._inited) return;
			this._inited = true;
			this._load();
			const mq = window.matchMedia('(min-width: 1024px)');
			this.desktop = mq.matches;
			mq.addEventListener('change', (e) => { this.desktop = e.matches; });
			window.addEventListener('keydown', (e) => { if (e.key === 'Escape') this.cancelDrag(); });
			window.addEventListener('pointermove', (e) => this._onPointerMove(e));
			window.addEventListener('pointerup', () => this._onPointerUp());
		},

		_load() {
			try {
				const raw = readMigratedStorage(WS_KEY);
				if (!raw) return;
				const data = JSON.parse(raw);
				const panels = ['files', 'editor', 'chat'];
				if (!Array.isArray(data.order) || data.order.length !== 3) return;
				if ([...data.order].sort().join() !== panels.slice().sort().join()) return;
				this.order = data.order;
				const w = {};
				let sum = 0;
				for (const p of panels) {
					let v = typeof data.widths[p] === 'number' ? data.widths[p] : WS_DEFAULTS.widths[p];
					v = Math.max(WS_MIN, Math.min(0.76, v));
					w[p] = v;
					sum += v;
				}
				for (const p of panels) w[p] = w[p] / sum;
				this.widths = w;
			} catch { /* defaults */ }
		},

		paneStyle(panel) {
			if (!this.desktop) return '';
			const idx = this.order.indexOf(panel);
			const w = this.widths[panel];
			return 'order:' + (idx * 2) + ';flex:' + w + ' ' + w + ' 0%;min-width:0';
		},

		handleStyle(slot) {
			return 'order:' + (slot * 2 + 1);
		},

		startResize(slot, e) {
			e.preventDefault();
			this.resizing = {
				slot: slot,
				startX: e.clientX,
				leftPanel: this.order[slot],
				rightPanel: this.order[slot + 1],
				left0: this.widths[this.order[slot]],
				right0: this.widths[this.order[slot + 1]],
			};
			try { e.target.setPointerCapture(e.pointerId); } catch { /* synthetic pointers */ }
			document.body.classList.add('ws-no-select', 'ws-col-resize');
		},

		resizeMove(e) {
			if (!this.resizing) return;
			const row = this.$refs.row;
			if (!row) return;
			const rowRect = row.getBoundingClientRect();
			const df = (e.clientX - this.resizing.startX) / rowRect.width;
			const total = this.resizing.left0 + this.resizing.right0;
			const nl = Math.max(WS_MIN, Math.min(total - WS_MIN, this.resizing.left0 + df));
			this.widths[this.resizing.leftPanel] = nl;
			this.widths[this.resizing.rightPanel] = total - nl;
			this.queueSave();
		},

		resizeEnd() {
			document.body.classList.remove('ws-no-select', 'ws-col-resize');
			this.resizing = null;
			this.saveNow();
		},

		resetWidths() {
			this.widths = { ...WS_DEFAULTS.widths };
			this.saveNow();
		},

		panePointerDown(e) {
			const handle = e.target.closest('[data-drag-handle]');
			const paneEl = e.target.closest('[data-panel]');
			if (!handle || !paneEl || !this.desktop || e.button !== 0) return;
			this._pending = { panel: paneEl.dataset.panel, startX: e.clientX, startY: e.clientY, el: paneEl };
		},

		_onPointerMove(e) {
			if (this.resizing) return;
			if (this._pending && !this.drag) {
				const dx = e.clientX - this._pending.startX;
				const dy = e.clientY - this._pending.startY;
				if (Math.sqrt(dx * dx + dy * dy) > 6) this._startDrag(e);
			}
			if (this.drag) {
				this._moveGhost(e);
				this._computeDropSlot(e);
			}
		},

		_onPointerUp() {
			if (this.drag && this.dropSlot != null) this._applyOrder(this.dropSlot);
			this._endDrag();
			this._pending = null;
		},

		_startDrag(e) {
			const p = this._pending;
			this.drag = { panel: p.panel, el: p.el };
			p.el.classList.add('ws-dragging');
			const meta = WS_META[p.panel];
			const ghost = document.createElement('div');
			ghost.className = 'ws-ghost';
			ghost.innerHTML = '<i data-lucide="' + meta.icon + '" class="w-3.5 h-3.5"></i><span>' + meta.label + '</span>';
			document.body.appendChild(ghost);
			refreshIcons();
			this._ghost = ghost;
			this._moveGhost(e);
		},

		_moveGhost(e) {
			if (!this._ghost) return;
			this._ghost.style.left = (e.clientX + 14) + 'px';
			this._ghost.style.top = (e.clientY + 10) + 'px';
		},

		_computeDropSlot(e) {
			const row = this.$refs.row;
			if (!row) return;
			const others = [...row.querySelectorAll('[data-panel]')]
				.filter((el) => el.dataset.panel !== this.drag.panel)
				.sort((a, b) => a.getBoundingClientRect().left - b.getBoundingClientRect().left);
			if (others.length < 2) return;
			const r0 = others[0].getBoundingClientRect();
			const r1 = others[1].getBoundingClientRect();
			const mid0 = r0.left + r0.width / 2;
			const mid1 = r1.left + r1.width / 2;
			let slot;
			if (e.clientX < mid0) slot = 0;
			else if (e.clientX > mid1) slot = 2;
			else slot = 1;
			if (slot === this.order.indexOf(this.drag.panel)) {
				this.dropX = null;
				this.dropSlot = null;
				return;
			}
			this.dropSlot = slot;
			const container = row.getBoundingClientRect();
			let boundary;
			if (slot === 0) boundary = r0.left;
			else if (slot === 2) boundary = r1.right;
			else boundary = (r0.right + r1.left) / 2;
			this.dropX = boundary - container.left;
		},

		_applyOrder(slot) {
			this.flip(() => {
				const o = this.order.filter((p) => p !== this.drag.panel);
				o.splice(slot, 0, this.drag.panel);
				this.order = o;
			});
		},

		flip(mutate) {
			const row = this.$refs.row;
			if (!row) { mutate(); return; }
			const panes = [...row.querySelectorAll('[data-panel]')];
			const first = new Map(panes.map((el) => [el, el.getBoundingClientRect()]));
			mutate();
			this.$nextTick(() => {
				for (const el of panes) {
					const f = first.get(el);
					const n = el.getBoundingClientRect();
					const dx = f.left - n.left;
					if (Math.abs(dx) < 2) continue;
					el.style.transition = 'none';
					el.style.transform = 'translateX(' + dx + 'px)';
					requestAnimationFrame(() => {
						el.style.transition = 'transform .28s cubic-bezier(.22,1,.36,1)';
						el.style.transform = '';
					});
				}
			});
		},

		_endDrag() {
			if (this.drag) this.drag.el.classList.remove('ws-dragging');
			if (this._ghost) { this._ghost.remove(); this._ghost = null; }
			this.drag = null;
			this.dropX = null;
			this.dropSlot = null;
			this.saveNow();
		},

		cancelDrag() {
			if (this.drag) {
				this.drag.el.classList.remove('ws-dragging');
				if (this._ghost) { this._ghost.remove(); this._ghost = null; }
				this.drag = null;
				this.dropX = null;
				this.dropSlot = null;
			}
			this._pending = null;
		},

		queueSave() {
			clearTimeout(this._saveTimer);
			this._saveTimer = setTimeout(() => this.saveNow(), 300);
		},

		saveNow() {
			try { localStorage.setItem(WS_KEY, JSON.stringify({ order: this.order, widths: this.widths })); } catch { /* silent */ }
		},

		resetLayout() {
			this.flip(() => {
				this.order = [...WS_DEFAULTS.order];
				this.widths = { ...WS_DEFAULTS.widths };
			});
			this.saveNow();
		},
	};
}

// ---- text highlighter: select text in a chat message or note preview ----
function initHighlighter() {
	let btn = null;
	const removeBtn = () => {
		if (btn) { btn.remove(); btn = null; }
	};
	const studyIDFor = (el) => {
		const holder = el.closest('[data-study-id]');
		return holder ? holder.dataset.studyId : '';
	};
	document.addEventListener('mouseup', (e) => {
		setTimeout(() => {
			const sel = window.getSelection();
			const text = sel && sel.rangeCount ? sel.toString().trim() : '';
			if (!text || text.length < 3) { removeBtn(); return; }
			const node = sel.anchorNode;
			const anchor = node && node.parentElement
				? node.parentElement.closest('[data-msg-id],[data-file-id]')
				: null;
			if (!anchor || !studyIDFor(anchor)) { removeBtn(); return; }
			removeBtn();
			const rect = sel.getRangeAt(0).getBoundingClientRect();
			btn = document.createElement('button');
			btn.type = 'button';
			btn.textContent = 'Destacar';
			btn.className = 'fixed z-50 bg-lamp text-ink text-[11px] font-800 px-3 py-1.5 border border-ink shadow-hard transition';
			btn.style.top = Math.max(8, rect.top - 38) + 'px';
			btn.style.left = Math.max(8, rect.left + rect.width / 2 - 34) + 'px';
			btn.addEventListener('mousedown', (ev) => ev.preventDefault());
			btn.addEventListener('click', () => {
				const kind = anchor.dataset.msgId ? 'message' : 'note';
				const sourceID = anchor.dataset.msgId || anchor.dataset.fileId;
				const sid = studyIDFor(anchor);
				fetch('/study/' + sid + '/highlights', {
					method: 'POST',
					headers: { 'Content-Type': 'application/json' },
					body: JSON.stringify({ source_kind: kind, source_id: Number(sourceID), excerpt: text }),
				}).then((r) => {
					if (!r.ok) throw new Error('falha ao destacar');
					btn.textContent = 'Destacado!';
					setTimeout(removeBtn, 700);
					sel.removeAllRanges();
				}).catch(() => {
					if (btn) btn.textContent = 'Erro';
					setTimeout(removeBtn, 900);
				});
			});
			document.body.appendChild(btn);
		}, 10);
	});
	document.addEventListener('mousedown', (e) => {
		if (btn && e.target !== btn) removeBtn();
	});
	window.addEventListener('scroll', removeBtn, { passive: true });
}

function leaderboardValue(metric, value) {
	if (metric === 'nota') return Number(value || 0).toFixed(2).replace('.', ',');
	return Number(value || 0).toLocaleString('pt-BR');
}

function leaderboardRankClass(key) {
	return {
		bronze: 'text-[#d69a61] bg-[#d69a61]/10 border-[#d69a61]',
		prata: 'text-[#c8d0d9] bg-[#c8d0d9]/10 border-[#c8d0d9]',
		ouro: 'text-lamp bg-lamp/10 border-lamp',
		'token-maxxer': 'text-coral bg-coral/10 border-coral',
		'knowledge-maxer': 'text-lamp bg-lamp/10 border-lamp',
		'study-maxer': 'text-lamp bg-lamp/10 border-lamp',
	}[key] || 'text-muted bg-surface2 border-line';
}

function initLeaderboard() {
	const root = document.querySelector('[data-leaderboard]');
	if (!root) return;
	let period = 'all';
	const error = root.querySelector('[data-leaderboard-error]');
	const buttons = [...root.querySelectorAll('[data-period]')];
	const load = async () => {
		error.classList.add('hidden');
		root.querySelectorAll('[data-board-body]').forEach((body) => {
			body.innerHTML = '<div data-board-loading class="px-5 py-10 text-center text-xs text-muted">Carregando...</div>';
		});
		try {
			const metrics = ['nota', 'quizzes', 'tokens'];
			const responses = await Promise.all(metrics.map(async (metric) => {
				const response = await fetch('/leaderboard/' + metric + '?period=' + encodeURIComponent(period), { headers: { Accept: 'application/json' } });
				if (!response.ok) throw new Error('Não foi possível carregar o leaderboard.');
				return [metric, await response.json()];
			}));
			responses.forEach(([metric, data]) => {
				const body = root.querySelector('[data-board="' + metric + '"] [data-board-body]');
				if (!body) return;
				if (!data.entries || data.entries.length === 0) {
					body.innerHTML = '<div class="px-5 py-10 text-center text-xs text-muted">Ainda não há resultados neste período.</div>';
					return;
				}
				body.innerHTML = data.entries.map((entry) => {
					const handle = entry.username && entry.tag ? entry.username + '#' + entry.tag : (entry.slug || 'usuário');
					const avatar = entry.avatar_url
						? '<img src="' + escapeHTML(entry.avatar_url) + '" alt="" class="w-8 h-8 object-cover border border-line shrink-0" />'
						: '<span class="grid place-items-center w-8 h-8 bg-surface2 border border-line text-muted shrink-0"><i data-lucide="user-round" class="w-4 h-4"></i></span>';
					const rank = entry.rank_label
						? '<span class="inline-flex items-center border px-2 py-0.5 text-[10px] font-800 ' + leaderboardRankClass(entry.rank_key) + '">' + escapeHTML(entry.rank_label) + '</span>'
						: '';
					return '<div class="flex items-center gap-3 px-5 py-3.5">' +
						'<span class="grid place-items-center w-7 h-7 bg-surface2 border border-line text-[11px] font-800 text-muted shrink-0">' + entry.rank + '</span>' +
						'<a href="/profile/' + encodeURIComponent(entry.slug || '') + '" class="flex-1 min-w-0 flex items-center gap-2.5 text-cream hover:text-lamp transition">' +
							avatar + '<span class="min-w-0 flex items-center gap-2"><span class="text-sm font-700 truncate">' + escapeHTML(handle) + '</span>' + rank + '</span>' +
						'</a>' +
						'<span class="text-sm font-800 text-lamp tabular-nums">' + leaderboardValue(metric, entry.value) + '</span>' +
					'</div>';
				}).join('');
			});
			refreshIcons();
		} catch (err) {
			error.textContent = err.message || 'Não foi possível carregar o leaderboard.';
			error.classList.remove('hidden');
		}
	};
	buttons.forEach((button) => button.addEventListener('click', () => {
		period = button.dataset.period || 'all';
		buttons.forEach((item) => {
			const active = item === button;
			item.classList.toggle('bg-lamp/15', active);
			item.classList.toggle('text-lamp', active);
			item.classList.toggle('text-muted', !active);
		});
		load();
	}));
	load();
}

function disableMiddleClickPaste() {
	document.addEventListener('auxclick', (event) => {
		if (event.button !== 1) return;
		const el = event.target instanceof Element ? event.target : null;
		if (el && el.closest('input, textarea, [contenteditable="true"]')) event.preventDefault();
	}, { capture: true });
}

document.addEventListener('DOMContentLoaded', () => {
	renderMarkdown();
	if (window.hydrateLearningElements) window.hydrateLearningElements(document);
	refreshIcons();
	paintWebToggle();
	initHighlighter();
	disableMiddleClickPaste();
	initLeaderboard();
	initTestStart();
	initQuizKeyboard();
	resumeActiveQuiz();
	initBuildVersionCheck();
	document.querySelectorAll('[data-web-toggle], #web-toggle').forEach((webToggle) => webToggle.addEventListener('click', toggleWeb));
});

document.addEventListener('htmx:afterSettle', () => {
	renderMarkdown();
	if (window.hydrateLearningElements) window.hydrateLearningElements(document);
	refreshIcons();
	// The answer landed: drop the draft for the question that was just submitted.
	if (quizDraftClearTarget && document.getElementById('quiz-area')) {
		clearQuizDrafts(quizDraftClearTarget);
		quizDraftClearTarget = null;
	}
	const chatPane = document.getElementById('chat-pane');
	if (chatPane) chatPane.dispatchEvent(new Event('chat-pane-updated'));
});

document.addEventListener('htmx:afterSettle', (event) => {
	if (event.target && event.target.id === 'main-pane') {
		window.dispatchEvent(new CustomEvent('mobile-pane', { detail: { pane: 'editor' } }));
	}
});

// A file was renamed/moved: refresh the editor pane so its title stays in sync.
document.body.addEventListener('refreshEditor', () => {
	const editor = document.querySelector('#main-pane [data-save-url]');
	if (!editor || !window.htmx) return;
	const saveURL = editor.dataset.saveUrl || '';
	const editURL = saveURL.replace(/\/content$/, '/edit');
	if (!editURL || editURL === saveURL) return;
	const data = window.Alpine ? Alpine.$data(editor) : null;
	const flush = () => htmx.ajax('GET', editURL, { target: '#main-pane', swap: 'innerHTML' });
	if (data && data.dirty && typeof data.save === 'function') {
		data.save(); // never clobber unsaved typing; give the autosave a beat
		setTimeout(flush, 600);
		return;
	}
	flush();
});

document.addEventListener('submit', (e) => {
	const f = e.target;
	if (!f || f.tagName !== 'FORM') return;
	const area = document.getElementById('quiz-area');
	const quizID = area ? area.dataset.quizId : '';
	if (quizID && (f.hasAttribute('data-quiz-answer-form') || f.hasAttribute('data-quiz-reanswer-form'))) {
		const idxInput = f.querySelector('input[name="index"]');
		if (idxInput) quizDraftClearTarget = 'learnix:draft:' + quizID + ':' + idxInput.value;
	}
	if (quizID && f.hasAttribute('data-quiz-result-form')) {
		clearQuizDrafts('learnix:draft:' + quizID + ':*');
		clearQuizDrafts('learnix:quiz-started:' + quizID);
	}
	if (!f.hasAttribute('hx-post') && !f.hasAttribute('hx-get')) {
		const action = f.getAttribute('action') || '';
		if (action.includes('quiz/result')) showLoader('Calculando resultado...');
		else showLoader('Carregando...');
	}
});
