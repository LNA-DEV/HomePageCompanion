// The autoupload API as the pages use it (modules/autoupload/api.go).
export type CellState = 'published' | 'failed' | 'next' | 'skipped' | 'open' | 'none';

export interface Cell {
	state: CellState;
	likes?: number;
	url?: string;
	reason?: string;
	time?: string;
	cause?: string;
}

export interface Column {
	id: string;
	platform: string;
}

export interface Row {
	id: string;
	source: string;
	kind: 'photo' | 'article' | 'note';
	title: string;
	summary?: string;
	link?: string;
	imageUrl?: string;
	categories?: string[];
	published: string;
	inFeed: boolean;
	websiteLikes: number;
	cells: Record<string, Cell>;
	likes: number;
}

export interface Counts {
	all: number;
	photos: number;
	blog: number;
	microblog: number;
	gone: number;
	problems: number;
}

export interface ItemsResponse {
	items: Row[];
	columns: Column[];
	total: number;
	page: number;
	limit: number;
	counts: Counts;
}

export interface Publication {
	id: number;
	itemId: string;
	target: string;
	platform: string;
	source: string;
	connection?: string;
	status: string;
	reason?: string;
	postUrl?: string;
	likes: number;
	likesAt?: string;
	publishedAt: string;
}

export interface Attempt {
	id: number;
	itemId: string;
	target: string;
	platform: string;
	source: string;
	connection?: string;
	success: boolean;
	cause?: 'account' | 'content' | 'transient';
	code?: string;
	httpStatus?: number;
	error?: string;
	resolvedAt?: string;
	createdAt: string;
	title?: string;
	imageUrl?: string;
}

export interface LikeDay {
	itemId: string;
	target: string;
	day: string;
	likes: number;
}

export interface ItemDetail {
	post: Row;
	columns: Column[];
	publications: Publication[] | null;
	attempts: Attempt[] | null;
	likeDays: LikeDay[] | null;
}

export interface Rendered {
	text: string;
	images?: { url: string; alt?: string }[];
	link?: { url: string; title?: string; description?: string; imageUrl?: string };
	warnings?: string[];
}

export interface Run {
	id: number;
	connection: string;
	trigger: string;
	startedAt: string;
	finishedAt?: string;
	result: 'running' | 'published' | 'skipped' | 'failed' | 'nothing' | 'paused';
	itemId?: string;
	title?: string;
	target: string;
	postUrl?: string;
	error?: string;
}

export interface ScheduleRow {
	connection: string;
	source: string;
	target: string;
	strategy: 'rotate' | 'announce';
	cron?: string;
	paused: boolean;
	pauseReason?: string;
	nextRun?: string;
	next?: { id: string; title: string; kind: string; published: string; imageUrl?: string; preview?: Rendered };
	error?: string;
	waiting?: number;
	window?: string;
	delay?: string;
	lastRun?: Run;
}

export interface Problem {
	itemId: string;
	title: string;
	imageUrl?: string;
	source: string;
	target: string;
	cause: 'account' | 'content' | 'transient';
	error: string;
	attempts: number;
	lastAt: string;
	nextRetry?: string;
}

export interface Problems {
	groups: Record<'account' | 'content' | 'transient', Problem[]>;
	total: number;
}

export interface Dashboard {
	targets: { id: string; platform: string; status: 'ok' | 'account' | 'paused'; published: number; error?: string; lastPublished?: string }[] | null;
	top: { itemId: string; title: string; imageUrl?: string; likes: number; websiteLikes: number }[] | null;
	problems: Record<string, number>;
}

export const causeLabel: Record<string, string> = {
	account: 'Account',
	content: 'Content',
	transient: 'Transient'
};

export const causeHelp: Record<string, string> = {
	account: 'Token expired or no permission; the connection pauses until it is fixed. Retried only by hand.',
	content: 'Image too large, a format the network refuses. Retried only by hand.',
	transient: 'Network, 5xx or a rate limit; retried by itself after 1, 3 and 7 days.'
};

export const resultLabel: Record<string, string> = {
	running: 'Running',
	published: 'Published',
	skipped: 'Skipped',
	failed: 'Failed',
	nothing: 'Nothing left to post',
	paused: 'Paused'
};
