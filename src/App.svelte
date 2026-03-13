<script lang="ts">
	import { onMount } from 'svelte';
	import { subscribeToLiveMessages, type LogMessage } from './store.js';
	import { Badge } from '$lib/components/ui/badge/index.js';
	import { Input } from '$lib/components/ui/input/index.js';
	import * as Table from '$lib/components/ui/table/index.js';
	import Sidebar from './Sidebar.svelte';

	const STORAGE_KEY = 'leno:visibleKeys';
	const FILTERS_STORAGE_KEY = 'leno:fieldFilters';
	const DARK_MODE_KEY = 'leno:darkMode';
	const AUTH_STORAGE_KEY = 'leno:authenticated';
	const DEFAULT_HISTORY_PAGE_SIZE = 1000;
	const HISTORY_SCROLL_THRESHOLD = 160;
	const COLLAPSIBLE_CELL_MIN_LENGTH = 160;
	const ACCOUNT = {
		username: 'monitor',
		password: 'gorilla@esim#',
	};
	const LEVEL_FILTER_ORDER = ['error', 'warn', 'info', 'debug', 'trace', 'fatal', 'unknown'];

	type HistoryResponse = {
		items: LogMessage[];
		next_before?: number;
		has_more: boolean;
		page_size: number;
	};

	let messages = $state<LogMessage[]>([]);
	let filteredMessages = $state<LogMessage[]>([]);
	let keys = $state<string[]>([]);
	let keysSet = new Set<string>();
	let messageIds = new Set<number>();
	let visibleKeys = $state<Record<string, boolean>>(
		JSON.parse(localStorage.getItem(STORAGE_KEY) ?? '{}')
	);
	let searchTerm = $state('');
	let selectedSource = $state('all');
	let selectedLevel = $state('all');
	let sidebarVisible = $state(true);
	let darkMode = $state(localStorage.getItem(DARK_MODE_KEY) === 'true');
	let isAuthenticated = $state(sessionStorage.getItem(AUTH_STORAGE_KEY) === 'true');
	let username = $state('');
	let password = $state('');
	let loginError = $state('');
	let historyPageSize = $state(DEFAULT_HISTORY_PAGE_SIZE);
	let hasMoreHistory = $state(true);
	let nextBefore = $state<number | null>(null);
	let isLoadingHistory = $state(false);
	let historyError = $state('');
	let viewportEl = $state<HTMLElement | null>(null);
	let expandedRows = $state<Record<string, boolean>>({});
	let fieldFilters = $state<Record<string, string[]>>(
		JSON.parse(localStorage.getItem(FILTERS_STORAGE_KEY) ?? '{}')
	);

	$effect(() => {
		localStorage.setItem(STORAGE_KEY, JSON.stringify(visibleKeys));
	});

	$effect(() => {
		localStorage.setItem(DARK_MODE_KEY, String(darkMode));
		document.documentElement.classList.toggle('dark', darkMode);
	});

	$effect(() => {
		sessionStorage.setItem(AUTH_STORAGE_KEY, String(isAuthenticated));
	});

	$effect(() => {
		localStorage.setItem(FILTERS_STORAGE_KEY, JSON.stringify(fieldFilters));
	});

	const topValuesCache = $derived.by(() => {
		const result: Record<string, string[]> = {};
		for (const field of Object.keys(fieldFilters)) {
			result[field] = computeTopValues(field);
		}
		return result;
	});

	const sources = $derived.by(() => {
		const counts = new Map<string, number>();
		for (const msg of messages) {
			const source = getMessageSource(msg);
			counts.set(source, (counts.get(source) ?? 0) + 1);
		}
		return [...counts.entries()]
			.sort((a, b) => a[0].localeCompare(b[0]))
			.map(([name, count]) => ({ name, count }));
	});

	const levels = $derived.by(() => {
		const counts = new Map<string, number>();
		for (const msg of messages) {
			const level = getMessageLevel(msg);
			counts.set(level, (counts.get(level) ?? 0) + 1);
		}

		const options = [{ name: 'all', count: messages.length }];
		for (const level of LEVEL_FILTER_ORDER) {
			const count = counts.get(level);
			if (count) options.push({ name: level, count });
		}
		return options;
	});

	function computeTopValues(field: string): string[] {
		const counts = new Map<string, number>();
		for (const msg of messages) {
			if (field in msg && msg[field] !== undefined) {
				const val = String(msg[field]);
				if (val.trim() === '') continue;
				counts.set(val, (counts.get(val) ?? 0) + 1);
			}
		}
		return [...counts.entries()]
			.sort((a, b) => b[1] - a[1])
			.slice(0, 5)
			.map(([val]) => val);
	}

	function addKeys(newKeys: string[]) {
		for (const k of newKeys) {
			if (k.startsWith('_leno_')) continue;
			if (!keysSet.has(k)) {
				keysSet.add(k);
				keys.push(k);
			}
			if (!(k in visibleKeys)) visibleKeys[k] = true;
		}
	}

	function selectAll() {
		for (const key of keys) visibleKeys[key] = true;
	}

	function selectNone() {
		for (const key of keys) visibleKeys[key] = false;
	}

	function addFilter(field: string) {
		if (field in fieldFilters) return;
		const topValues = computeTopValues(field);
		fieldFilters = { ...fieldFilters, [field]: topValues };
		applyFilters();
	}

	function removeFilter(field: string) {
		const { [field]: _removed, ...rest } = fieldFilters;
		fieldFilters = rest;
		applyFilters();
	}

	function filterMessage(currentMessage: LogMessage): boolean {
		if (searchTerm !== '') {
			const lowerSearch = searchTerm.toLowerCase();
			const matchesSearch = keys.some(
				(key) =>
					key in currentMessage && String(currentMessage[key]).toLowerCase().includes(lowerSearch)
			);
			if (!matchesSearch) return false;
		}

		if (selectedSource !== 'all' && getMessageSource(currentMessage) !== selectedSource) {
			return false;
		}

		if (selectedLevel !== 'all' && getMessageLevel(currentMessage) !== selectedLevel) {
			return false;
		}

		for (const [field, selectedValues] of Object.entries(fieldFilters)) {
			if (selectedValues.length === 0) continue;
			const msgVal =
				currentMessage[field] !== undefined ? String(currentMessage[field]) : undefined;
			if (msgVal === undefined || !selectedValues.includes(msgVal)) return false;
		}

		return true;
	}

	let pendingMessages: LogMessage[] = [];
	let rafPending = false;

	function scheduleFlush() {
		if (rafPending) return;
		rafPending = true;
		requestAnimationFrame(() => {
			rafPending = false;
			const batch = pendingMessages.splice(0);
			if (batch.length === 0) return;

			const uniqueBatch = batch.filter(registerMessage);
			if (uniqueBatch.length === 0) return;

			messages.unshift(...uniqueBatch);

			const matching = uniqueBatch.filter(filterMessage);
			if (matching.length > 0) {
				filteredMessages.unshift(...matching);
			}
		});
	}

	function queueMessage(currentMessage: LogMessage | null) {
		if (!currentMessage) return;
		pendingMessages.push(currentMessage);
		scheduleFlush();
	}

	function applyFilters() {
		const result = messages.filter(filterMessage);
		filteredMessages.length = 0;
		filteredMessages.push(...result);
	}

	let liveUnsubscribe: () => void = () => {};
	let sessionInitialized = false;

	async function initializeSession() {
		if (!isAuthenticated || sessionInitialized) return;

		sessionInitialized = true;
		await loadHistory(true);
		liveUnsubscribe();
		liveUnsubscribe = subscribeToLiveMessages(queueMessage);
	}

	function teardownSession() {
		liveUnsubscribe();
		liveUnsubscribe = () => {};
		sessionInitialized = false;
	}

	onMount(() => {
		if (isAuthenticated) {
			void initializeSession();
		}

		return () => {
			teardownSession();
		};
	});

	function handleLogin() {
		if (username === ACCOUNT.username && password === ACCOUNT.password) {
			isAuthenticated = true;
			loginError = '';
			password = '';
			void initializeSession();
			return;
		}

		loginError = 'Invalid username or password.';
		password = '';
	}

	function handleLogout() {
		teardownSession();
		isAuthenticated = false;
		username = '';
		password = '';
		loginError = '';
		resetMessages();
	}

	function registerMessage(message: LogMessage): boolean {
		const id = getMessageId(message);
		if (id !== null) {
			if (messageIds.has(id)) return false;
			messageIds.add(id);
		}
		addKeys(Object.keys(message));
		return true;
	}

	function getMessageId(message: LogMessage): number | null {
		return typeof message._leno_id === 'number' ? message._leno_id : null;
	}

	function getMessageSource(message: LogMessage): string {
		const raw = message.source;
		if (raw === undefined || raw === null) return 'unknown';
		const value = String(raw).trim();
		return value === '' ? 'unknown' : value;
	}

	function normalizeLevelName(raw: unknown): string {
		if (raw === undefined || raw === null) return 'unknown';
		const value = String(raw).trim().toLowerCase();
		if (value === '') return 'unknown';
		if (value === 'warning') return 'warn';
		return value;
	}

	function getMessageLevel(message: LogMessage): string {
		return normalizeLevelName(message.level);
	}

	function getLevelLabel(level: string): string {
		if (level === 'all') return 'All levels';
		if (level === 'unknown') return 'Unknown';
		return level.toUpperCase();
	}

	function getLevelDotClass(level: string): string {
		switch (level) {
			case 'error':
			case 'fatal':
				return 'bg-destructive';
			case 'warn':
				return 'bg-yellow-500';
			case 'info':
				return 'bg-sky-500';
			case 'debug':
				return 'bg-violet-500';
			case 'trace':
				return 'bg-cyan-500';
			default:
				return 'bg-muted-foreground/50';
		}
	}

	function getCellValue(value: unknown): string {
		if (value === undefined || value === null) return '';
		if (typeof value === 'string') return value;
		if (typeof value === 'object') {
			try {
				return JSON.stringify(value, null, 2);
			} catch {
				return String(value);
			}
		}
		return String(value);
	}

	function isCollapsibleCell(key: string, value: string): boolean {
		return (
			key === 'message' ||
			value.length > COLLAPSIBLE_CELL_MIN_LENGTH ||
			value.includes('\n') ||
			value.includes('\tat ') ||
			value.startsWith('at ')
		);
	}

	function getColumnClass(key: string): string {
		if (key === 'message') return 'w-[34rem] min-w-[20rem]';
		if (key === 'source') return 'w-44';
		return 'w-40';
	}

	function getCellClass(key: string): string {
		if (key === 'message') return 'max-w-[34rem] whitespace-normal align-top';
		return 'max-w-64 whitespace-normal align-top';
	}

	function getCollapseMeta(value: string): string {
		const lineCount = value.split('\n').length;
		if (lineCount > 1) return `${lineCount} lines`;
		return `${value.length} chars`;
	}

	function getCollapsePreview(value: string): string {
		const firstLine = value.split('\n', 1)[0]?.trim() ?? '';
		if (firstLine === '') return '(empty message)';
		if (firstLine.length <= 140) return firstLine;
		return `${firstLine.slice(0, 137)}...`;
	}

	function getRowStateKey(message: LogMessage): string {
		const id = getMessageId(message);
		if (id !== null) return String(id);
		return JSON.stringify(message).slice(0, 160);
	}

	function isMessageExpanded(message: LogMessage): boolean {
		return Boolean(expandedRows[getRowStateKey(message)]);
	}

	function toggleMessageExpanded(message: LogMessage) {
		const rowKey = getRowStateKey(message);
		expandedRows = {
			...expandedRows,
			[rowKey]: !expandedRows[rowKey],
		};
	}

	function handleRowClick(event: MouseEvent, message: LogMessage, rowExpandable: boolean) {
		if (!rowExpandable) return;

		const target = event.target;
		if (target instanceof Element && target.closest('.log-message-panel')) {
			return;
		}

		const selection = window.getSelection();
		if (selection && !selection.isCollapsed && selection.toString().trim() !== '') {
			return;
		}

		toggleMessageExpanded(message);
	}

	async function loadHistory(reset = false) {
		if (isLoadingHistory) return;
		isLoadingHistory = true;
		historyError = '';

		try {
			const params = new URLSearchParams();
			if (!reset && nextBefore !== null) {
				params.set('before', String(nextBefore));
			}
			if (historyPageSize > 0) {
				params.set('limit', String(historyPageSize));
			}

			const response = await fetch(`/history?${params.toString()}`);
			if (!response.ok) {
				throw new Error(`History request failed with ${response.status}`);
			}

			const payload = (await response.json()) as HistoryResponse;
			historyPageSize = payload.page_size || historyPageSize;
			hasMoreHistory = payload.has_more;
			nextBefore = payload.next_before ?? null;

			const items = payload.items.filter(registerMessage);
			if (reset) {
				messages = [...items];
				filteredMessages = [];
				applyFilters();
				return;
			}

			if (items.length > 0) {
				messages.push(...items);
				const matching = items.filter(filterMessage);
				if (matching.length > 0) {
					filteredMessages.push(...matching);
				}
			}
		} catch (error) {
			historyError = error instanceof Error ? error.message : 'Failed to load log history';
		} finally {
			isLoadingHistory = false;
		}
	}

	function handleViewportScroll() {
		if (!viewportEl || isLoadingHistory || !hasMoreHistory) return;
		const distanceFromBottom =
			viewportEl.scrollHeight - viewportEl.scrollTop - viewportEl.clientHeight;
		if (distanceFromBottom > HISTORY_SCROLL_THRESHOLD) return;
		void loadHistory();
	}

	function resetMessages() {
		messages = [];
		filteredMessages = [];
		keys = [];
		keysSet = new Set<string>();
		messageIds = new Set<number>();
		expandedRows = {};
		selectedSource = 'all';
		selectedLevel = 'all';
		hasMoreHistory = true;
		nextBefore = null;
		historyError = '';
	}

	function getLevelVariant(level: unknown): 'default' | 'destructive' | 'secondary' | 'outline' {
		switch (String(level).toLowerCase()) {
			case 'error':
			case 'fatal':
				return 'destructive';
			case 'warn':
			case 'warning':
				return 'secondary';
			default:
				return 'outline';
		}
	}

	function getLevelClass(level: string): string {
		switch (level) {
			case 'error':
			case 'fatal':
				return 'bg-destructive/8 hover:bg-destructive/15 border-l-2 border-l-destructive/50';
			case 'warn':
			case 'warning':
				return 'bg-yellow-500/8 hover:bg-yellow-500/15 border-l-2 border-l-yellow-500/50';
			case 'info':
				return 'bg-sky-500/8 hover:bg-sky-500/15 border-l-2 border-l-sky-500/40';
			case 'debug':
				return 'bg-violet-500/8 hover:bg-violet-500/15 border-l-2 border-l-violet-500/40';
			case 'trace':
				return 'bg-cyan-500/8 hover:bg-cyan-500/15 border-l-2 border-l-cyan-500/40';
			default:
				return 'hover:bg-muted/50';
		}
	}
</script>

{#if !isAuthenticated}
	<div
		class="relative flex min-h-screen items-center justify-center overflow-hidden bg-background px-4 py-10"
	>
		<div
			class="absolute inset-0 bg-[radial-gradient(circle_at_top_left,_color-mix(in_oklab,var(--color-sidebar-primary)_16%,transparent),transparent_34%),radial-gradient(circle_at_bottom_right,_color-mix(in_oklab,var(--color-chart-2)_18%,transparent),transparent_30%)]"
		></div>
		<div
			class="absolute inset-x-0 top-0 h-px bg-gradient-to-r from-transparent via-border to-transparent"
		></div>
		<div
			class="relative grid w-full max-w-5xl overflow-hidden rounded-3xl border border-border bg-card/90 shadow-2xl shadow-black/10 backdrop-blur md:grid-cols-[1.15fr_0.85fr]"
		>
			<section
				class="flex flex-col justify-between border-b border-border bg-sidebar px-8 py-8 md:border-b-0 md:border-r md:px-10 md:py-10"
			>
				<div class="space-y-6">
					<div
						class="inline-flex w-fit items-center gap-3 rounded-full border border-sidebar-border bg-background/70 px-4 py-2 text-sm font-medium text-foreground shadow-sm"
					>
						<span
							class="flex h-8 w-8 items-center justify-center rounded-full bg-sidebar-primary text-sidebar-primary-foreground"
						>
							<svg
								xmlns="http://www.w3.org/2000/svg"
								class="h-4 w-4"
								viewBox="0 0 64 64"
								fill="currentColor"
							>
								<g>
									<polygon
										points="53.414 37 51.414 35 39 35 39 37 50.586 37 52.586 39 58 39 58 37 53.414 37"
									/>
									<polygon
										points="32.586 42 24 42 24 44 33.414 44 35.414 42 41 42 41 40 34.586 40 32.586 42"
									/>
									<rect x="33" y="30" width="4" height="2" />
									<path
										d="M11,30c-2.8,0-5,3.075-5,7s2.2,7,5,7,5-3.075,5-7S13.8,30,11,30Zm0,12c-1.626,0-3-2.29-3-5s1.374-5,3-5,3,2.29,3,5S12.626,42,11,42Z"
									/>
									<path
										d="M53,26H50.109a6.433,6.433,0,0,1,1.331-3H55a5.006,5.006,0,0,0,5-5V17H55a4.99,4.99,0,0,0-4.956,4.565A8.545,8.545,0,0,0,48.086,26H43.721l-2-6H34.279l-2,6H25.118a6.284,6.284,0,0,0-3.181-5.62A4.989,4.989,0,0,0,17,16H12v1a5.006,5.006,0,0,0,5,5h3.738a4.284,4.284,0,0,1,2.373,4H11c-4.962,0-9,4.935-9,11s4.038,11,9,11H53c4.962,0,9-4.935,9-11S57.962,26,53,26Zm2-7h2.829A3.006,3.006,0,0,1,55,21H52.171A3.006,3.006,0,0,1,55,19ZM17,20a3.006,3.006,0,0,1-2.829-2H17a3.006,3.006,0,0,1,2.829,2ZM4,37c0-4.963,3.14-9,7-9s7,4.037,7,9-3.14,9-7,9S4,41.963,4,37Zm55.261,4H46v2H58.2A6.434,6.434,0,0,1,53,46H16.158a11.664,11.664,0,0,0,3.8-8h8.455l2-2H34V34H29.586l-2,2H19.959a12.79,12.79,0,0,0-.953-4H29V30H17.937a10.07,10.07,0,0,0-1.779-2H33.721l2-6h4.558l1.334,4H38v2H53a6.1,6.1,0,0,1,4.39,2H43v2H58.816A10.833,10.833,0,0,1,60,37,11.048,11.048,0,0,1,59.261,41Z"
									/>
								</g>
							</svg>
						</span>
						<span>Leño Monitor Access</span>
					</div>

					<div class="space-y-3">
						<h1 class="max-w-md text-3xl font-semibold tracking-tight text-foreground md:text-4xl">
							Log streaming, filtered to operators who can act on it.
						</h1>
						<p class="max-w-lg text-sm leading-6 text-muted-foreground md:text-base">
							Use the dedicated monitor account to enter the dashboard and inspect live events,
							levels, and structured fields.
						</p>
					</div>
				</div>

				<div class="grid gap-3 pt-8 text-sm text-muted-foreground sm:grid-cols-3">
					<div class="rounded-2xl border border-sidebar-border bg-background/70 p-4">
						<p class="font-medium text-foreground">Latest backlog</p>
						<p class="mt-1 text-xs leading-5">Loads the newest page immediately after login.</p>
					</div>
					<div class="rounded-2xl border border-sidebar-border bg-background/70 p-4">
						<p class="font-medium text-foreground">Service filters</p>
						<p class="mt-1 text-xs leading-5">Keeps per-service navigation in the sidebar.</p>
					</div>
					<div class="rounded-2xl border border-sidebar-border bg-background/70 p-4">
						<p class="font-medium text-foreground">Live stream</p>
						<p class="mt-1 text-xs leading-5">
							Continues delivering new lines over the live event stream.
						</p>
					</div>
				</div>
			</section>

			<section class="flex items-center bg-card px-6 py-8 md:px-10">
				<form
					class="w-full space-y-6"
					onsubmit={(event) => {
						event.preventDefault();
						handleLogin();
					}}
				>
					<div class="space-y-2">
						<p class="text-sm font-medium uppercase tracking-[0.2em] text-muted-foreground">
							Sign in
						</p>
						<h2 class="text-2xl font-semibold tracking-tight text-foreground">Operator login</h2>
						<p class="text-sm leading-6 text-muted-foreground">
							Only the monitor account can access this panel.
						</p>
					</div>

					<div class="space-y-4">
						<div class="space-y-2">
							<label class="text-sm font-medium text-foreground" for="username">Username</label>
							<Input
								id="username"
								bind:value={username}
								placeholder="Enter username"
								autocomplete="username"
								class="h-11 bg-background"
							/>
						</div>

						<div class="space-y-2">
							<label class="text-sm font-medium text-foreground" for="password">Password</label>
							<Input
								id="password"
								type="password"
								bind:value={password}
								placeholder="Enter password"
								autocomplete="current-password"
								class="h-11 bg-background"
							/>
						</div>
					</div>

					{#if loginError}
						<p
							class="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
						>
							{loginError}
						</p>
					{/if}

					<button
						type="submit"
						class="inline-flex h-11 w-full items-center justify-center rounded-xl bg-primary px-4 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90"
					>
						Enter dashboard
					</button>
				</form>
			</section>
		</div>
	</div>
{:else}
	<div class="flex h-screen w-full overflow-hidden bg-background">
		{#if sidebarVisible}
			<Sidebar
				{sources}
				bind:selectedSource
				{keys}
				bind:visibleKeys
				bind:fieldFilters
				{topValuesCache}
				totalMessages={messages.length}
				filteredCount={filteredMessages.length}
				callbacks={{
					applyFilters,
					selectAll,
					selectNone,
					addFilter,
					removeFilter,
					selectSource: (source: string) => {
						selectedSource = source;
						applyFilters();
					},
				}}
				onToggle={() => (sidebarVisible = false)}
				{darkMode}
				onToggleDarkMode={() => (darkMode = !darkMode)}
			/>
		{:else}
			<button
				onclick={() => (sidebarVisible = true)}
				class="flex h-full w-9 shrink-0 items-start justify-center border-r border-border bg-sidebar pt-[17px] text-sidebar-foreground/40 transition-colors hover:text-sidebar-foreground"
				title="Show sidebar"
			>
				<svg
					xmlns="http://www.w3.org/2000/svg"
					class="h-4 w-4"
					viewBox="0 0 24 24"
					fill="none"
					stroke="currentColor"
					stroke-width="2"
					stroke-linecap="round"
					stroke-linejoin="round"
				>
					<polyline points="9 18 15 12 9 6" />
				</svg>
			</button>
		{/if}

		<div class="flex flex-1 flex-col overflow-hidden">
			<header
				class="flex h-14 shrink-0 items-center justify-between border-b border-border bg-background px-4"
			>
				<div class="flex items-center gap-3">
					<div class="flex items-center gap-1.5">
						<svg
							xmlns="http://www.w3.org/2000/svg"
							class="h-4 w-4 text-muted-foreground"
							viewBox="0 0 24 24"
							fill="none"
							stroke="currentColor"
							stroke-width="2"
							stroke-linecap="round"
							stroke-linejoin="round"
						>
							<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z" />
							<polyline points="14 2 14 8 20 8" />
							<line x1="16" y1="13" x2="8" y2="13" />
							<line x1="16" y1="17" x2="8" y2="17" />
							<polyline points="10 9 9 9 8 9" />
						</svg>
						<h1 class="text-sm font-medium">Log Stream</h1>
					</div>
					{#if messages.length > 0}
						<div class="flex items-center gap-1.5 rounded-md bg-muted/60 px-2 py-0.5">
							<div class="h-1.5 w-1.5 rounded-full bg-green-500 animate-pulse"></div>
							<span class="font-mono text-xs text-muted-foreground">
								{filteredMessages.length.toLocaleString()}
								{#if filteredMessages.length !== messages.length}
									<span class="text-muted-foreground/50">/ {messages.length.toLocaleString()}</span>
								{/if}
							</span>
						</div>
					{/if}
				</div>
				<div class="flex items-center gap-3">
					{#if selectedSource !== 'all' || selectedLevel !== 'all' || searchTerm || Object.keys(fieldFilters).length > 0}
						<div class="flex items-center gap-2 text-xs text-muted-foreground">
							{#if selectedSource !== 'all'}
								<span class="flex items-center gap-1 rounded-md bg-muted px-2 py-0.5">
									Service: {selectedSource}
								</span>
							{/if}
							{#if selectedLevel !== 'all'}
								<span class="flex items-center gap-1 rounded-md bg-muted px-2 py-0.5">
									<span class={`h-2 w-2 rounded-full ${getLevelDotClass(selectedLevel)}`}></span>
									{getLevelLabel(selectedLevel)}
								</span>
							{/if}
							{#if searchTerm}
								<span class="flex items-center gap-1 rounded-md bg-muted px-2 py-0.5">
									<svg
										xmlns="http://www.w3.org/2000/svg"
										class="h-3 w-3"
										viewBox="0 0 24 24"
										fill="none"
										stroke="currentColor"
										stroke-width="2"
										stroke-linecap="round"
										stroke-linejoin="round"
									>
										<circle cx="11" cy="11" r="8" /><path d="m21 21-4.35-4.35" />
									</svg>
									"{searchTerm}"
								</span>
							{/if}
							{#if Object.keys(fieldFilters).length > 0}
								<span class="flex items-center gap-1 rounded-md bg-muted px-2 py-0.5">
									<svg
										xmlns="http://www.w3.org/2000/svg"
										class="h-3 w-3"
										viewBox="0 0 24 24"
										fill="none"
										stroke="currentColor"
										stroke-width="2"
										stroke-linecap="round"
										stroke-linejoin="round"
									>
										<polygon points="22 3 2 3 10 12.46 10 19 14 21 14 12.46 22 3" />
									</svg>
									{Object.keys(fieldFilters).length} filter{Object.keys(fieldFilters).length !== 1
										? 's'
										: ''}
								</span>
							{/if}
						</div>
					{/if}
					<button
						type="button"
						onclick={handleLogout}
						class="inline-flex h-8 items-center justify-center rounded-md border border-border bg-background px-3 text-xs font-medium text-foreground transition-colors hover:bg-muted"
					>
						Log out
					</button>
				</div>
			</header>
			<section class="border-b border-border bg-background px-4 py-3">
				<div class="flex flex-col gap-3">
					<div class="relative max-w-md">
						<svg
							xmlns="http://www.w3.org/2000/svg"
							class="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground/50"
							viewBox="0 0 24 24"
							fill="none"
							stroke="currentColor"
							stroke-width="2"
							stroke-linecap="round"
							stroke-linejoin="round"
						>
							<circle cx="11" cy="11" r="8" />
							<path d="m21 21-4.35-4.35" />
						</svg>
						<Input
							type="search"
							placeholder="Search logs..."
							bind:value={searchTerm}
							oninput={applyFilters}
							class="h-9 w-full pl-8"
						/>
					</div>

					<div class="flex flex-wrap items-center gap-2">
						{#each levels as levelOption}
							<button
								type="button"
								onclick={() => {
									selectedLevel = levelOption.name;
									applyFilters();
								}}
								class={`inline-flex items-center gap-2 rounded-full border px-3 py-1 text-xs font-medium transition-colors ${selectedLevel === levelOption.name ? 'border-primary bg-primary text-primary-foreground' : 'border-border bg-background text-muted-foreground hover:bg-muted hover:text-foreground'}`}
							>
								<span
									class={`h-2 w-2 rounded-full ${selectedLevel === levelOption.name ? 'bg-current' : getLevelDotClass(levelOption.name)}`}
								></span>
								<span>{getLevelLabel(levelOption.name)}</span>
								<span class="font-mono text-[11px] opacity-80">{levelOption.count}</span>
							</button>
						{/each}
					</div>
				</div>
			</section>
			<main bind:this={viewportEl} onscroll={handleViewportScroll} class="flex-1 overflow-auto">
				{#if messages.length === 0}
					<div class="flex h-full flex-col items-center justify-center gap-4 text-muted-foreground">
						<div class="flex h-12 w-12 items-center justify-center rounded-xl bg-muted">
							<svg
								xmlns="http://www.w3.org/2000/svg"
								class="h-6 w-6 text-muted-foreground/60"
								viewBox="0 0 24 24"
								fill="none"
								stroke="currentColor"
								stroke-width="1.5"
								stroke-linecap="round"
								stroke-linejoin="round"
							>
								<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z" />
								<polyline points="14 2 14 8 20 8" />
								<line x1="16" y1="13" x2="8" y2="13" />
								<line x1="16" y1="17" x2="8" y2="17" />
								<polyline points="10 9 9 9 8 9" />
							</svg>
						</div>
						<div class="text-center">
							<p class="text-sm font-medium text-foreground">Waiting for log messages</p>
							<p class="mt-1 text-xs text-muted-foreground">
								History loads from the server, then new entries stream in live
							</p>
						</div>
						<div class="rounded-md border border-border bg-muted/50 px-3 py-2">
							<code class="text-xs font-mono text-muted-foreground">tail -F app.log | leno</code>
						</div>
					</div>
				{:else}
					{#if historyError}
						<div
							class="border-b border-destructive/30 bg-destructive/10 px-4 py-2 text-sm text-destructive"
						>
							{historyError}
						</div>
					{/if}
					<Table.Root class="table-fixed">
						<Table.Header
							class="sticky top-0 z-10 bg-background/95 backdrop-blur supports-[backdrop-filter]:bg-background/80"
						>
							<Table.Row class="border-b border-border hover:bg-transparent">
								{#each keys as key}
									{#if visibleKeys[key]}
										<Table.Head
											class={`h-10 whitespace-nowrap px-3 text-xs font-medium uppercase tracking-wide text-muted-foreground ${getColumnClass(key)}`}
											>{key}</Table.Head
										>
									{/if}
								{/each}
							</Table.Row>
						</Table.Header>
						<Table.Body>
							{#each filteredMessages as message (message._leno_id ?? JSON.stringify(message))}
								{@const level = String(message.level ?? '').toLowerCase()}
								{@const messageValue = getCellValue(message.message)}
								{@const rowExpandable =
									message.message !== undefined && isCollapsibleCell('message', messageValue)}
								{@const rowExpanded = rowExpandable && isMessageExpanded(message)}
								<Table.Row
									class={`border-b border-border/50 ${getLevelClass(level)} transition-colors ${rowExpandable ? 'cursor-pointer' : ''}`}
									onclick={(event) => handleRowClick(event, message, rowExpandable)}
								>
									{#each keys as key}
										{#if visibleKeys[key]}
											<Table.Cell class={`px-3 py-1.5 text-xs font-mono ${getCellClass(key)}`}>
												{@const rawValue = message[key]}
												{@const cellValue = getCellValue(rawValue)}
												{@const collapsible = isCollapsibleCell(key, cellValue)}
												{#if key === 'level' && rawValue !== undefined}
													<Badge
														variant={getLevelVariant(rawValue)}
														class="px-1.5 py-0 text-xs font-medium"
													>
														{rawValue}
													</Badge>
												{:else if rawValue !== undefined}
													{#if key === 'message' && collapsible}
														<div
															class={`log-message-accordion ${rowExpanded ? 'is-open' : ''}`}
															title={cellValue}
														>
															<div class="log-message-summary">
																<span class="log-message-chevron" aria-hidden="true">
																	<svg
																		xmlns="http://www.w3.org/2000/svg"
																		class={`h-3.5 w-3.5 transition-transform ${rowExpanded ? 'rotate-90' : ''}`}
																		viewBox="0 0 24 24"
																		fill="none"
																		stroke="currentColor"
																		stroke-width="2"
																		stroke-linecap="round"
																		stroke-linejoin="round"
																	>
																		<polyline points="9 18 15 12 9 6" />
																	</svg>
																</span>
																<div class="log-message-summary-copy">
																	<span class="log-message-preview"
																		>{getCollapsePreview(cellValue)}</span
																	>
																	<span class="log-message-meta">{getCollapseMeta(cellValue)}</span>
																</div>
															</div>
															{#if rowExpanded}
																<div class="log-message-panel">
																	<pre
																		class="log-cell log-message-content text-foreground/80">{cellValue}</pre>
																</div>
															{/if}
														</div>
													{:else if collapsible}
														<pre class="log-cell text-foreground/80">{cellValue}</pre>
													{:else}
														<span class="block truncate text-foreground/80" title={cellValue}
															>{cellValue}</span
														>
													{/if}
												{/if}
											</Table.Cell>
										{/if}
									{/each}
								</Table.Row>
							{/each}
						</Table.Body>
					</Table.Root>
					<div
						class="border-t border-border bg-background/70 px-4 py-3 text-xs text-muted-foreground"
					>
						{#if isLoadingHistory}
							Loading older logs...
						{:else if hasMoreHistory}
							Scroll down to load {historyPageSize.toLocaleString()} more logs
						{:else}
							Loaded all buffered logs
						{/if}
					</div>
				{/if}
			</main>
		</div>
	</div>
{/if}
