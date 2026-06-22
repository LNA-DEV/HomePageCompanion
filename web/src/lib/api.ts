const API_BASE = '/api';

class ApiClient {
	private apiKey: string | null = null;

	setApiKey(key: string) {
		this.apiKey = key;
		if (typeof localStorage !== 'undefined') {
			localStorage.setItem('apiKey', key);
		}
	}

	loadApiKey(): string | null {
		if (typeof localStorage !== 'undefined') {
			this.apiKey = localStorage.getItem('apiKey');
		}
		return this.apiKey;
	}

	clearApiKey() {
		this.apiKey = null;
		if (typeof localStorage !== 'undefined') {
			localStorage.removeItem('apiKey');
		}
	}

	private async request<T>(path: string, options: RequestInit = {}): Promise<T> {
		const headers: Record<string, string> = {
			'Content-Type': 'application/json',
			...(options.headers as Record<string, string>)
		};

		if (this.apiKey) {
			headers['Authorization'] = `ApiKey ${this.apiKey}`;
		}

		const response = await fetch(`${API_BASE}${path}`, {
			...options,
			headers
		});

		if (response.status === 401) {
			this.clearApiKey();
			throw new Error('Unauthorized');
		}

		if (!response.ok) {
			throw new Error(`API Error: ${response.status}`);
		}

		return response.json();
	}

	// Auth
	async verifyAuth(): Promise<boolean> {
		try {
			await this.request('/admin/auth/verify');
			return true;
		} catch {
			return false;
		}
	}

	// Dashboard
	async getStats(): Promise<DashboardStats> {
		return this.request<DashboardStats>('/admin/stats');
	}

	// Feeds
	async getFeeds(): Promise<FeedWithCount[]> {
		return this.request<FeedWithCount[]>('/admin/feeds');
	}

	async getFeed(id: number): Promise<Feed> {
		return this.request<Feed>(`/admin/feeds/${id}`);
	}

	async getFeedItems(
		id: number,
		page: number = 1,
		limit: number = 20
	): Promise<PaginatedFeedItems> {
		return this.request<PaginatedFeedItems>(`/admin/feeds/${id}/items?page=${page}&limit=${limit}`);
	}

	async getFeedItemsLookup(guids: string[]): Promise<Record<string, MiniFeedItem>> {
		if (guids.length === 0) return {};
		return this.request<Record<string, MiniFeedItem>>('/admin/feed-items/lookup', {
			method: 'POST',
			body: JSON.stringify({ guids })
		});
	}

	async getFeedItemsLookupByLink(links: string[]): Promise<Record<string, MiniFeedItem>> {
		if (links.length === 0) return {};
		return this.request<Record<string, MiniFeedItem>>('/admin/feed-items/lookup', {
			method: 'POST',
			body: JSON.stringify({ links })
		});
	}

	// Publications
	async getPublications(platform?: string): Promise<AutoUploadItem[]> {
		const query = platform ? `?platform=${platform}` : '';
		return this.request<AutoUploadItem[]>(`/admin/publications${query}`);
	}

	async deletePublication(id: number): Promise<void> {
		await this.request(`/admin/publications/${id}`, { method: 'DELETE' });
	}

	// Interactions
	async getInteractions(platform?: string, itemId?: string): Promise<Interaction[]> {
		const params = new URLSearchParams();
		if (platform) params.set('platform', platform);
		if (itemId) params.set('itemId', itemId);
		const query = params.toString() ? `?${params.toString()}` : '';
		return this.request<Interaction[]>(`/admin/interactions${query}`);
	}

	async getInteractionsSummary(): Promise<InteractionSummary> {
		return this.request<InteractionSummary>('/admin/interactions/summary');
	}

	// Subscribers
	async getSubscribers(): Promise<Subscriber[]> {
		return this.request<Subscriber[]>('/admin/subscribers');
	}

	async deleteSubscriber(id: number): Promise<void> {
		await this.request(`/admin/subscribers/${id}`, { method: 'DELETE' });
	}

	// Webmentions
	async getWebmentions(): Promise<Webmention[]> {
		return this.request<Webmention[]>('/admin/webmentions');
	}

	// Connections
	async getConnections(): Promise<Connection[]> {
		return this.request<Connection[]>('/admin/connections');
	}

	// Upload trigger
	async triggerUpload(connectionName: string): Promise<void> {
		await this.request(`/upload/${connectionName}`, { method: 'POST' });
	}

	// Interactions fetch trigger
	async triggerInteractionsFetch(): Promise<void> {
		await this.request('/interactions/fetch', { method: 'POST' });
	}

	// Backfill trigger
	async triggerBackfill(): Promise<void> {
		await this.request('/backfill', { method: 'POST' });
	}

	// Broadcast
	async broadcast(notification: BroadcastNotification): Promise<void> {
		await this.request('/webpush/broadcast', {
			method: 'POST',
			body: JSON.stringify(notification)
		});
	}

	// Logs
	async getLogs(opts: { file?: string; tail?: number; search?: string } = {}): Promise<LogsResponse> {
		const params = new URLSearchParams();
		if (opts.file) params.set('file', opts.file);
		if (opts.tail) params.set('tail', String(opts.tail));
		if (opts.search) params.set('search', opts.search);
		const q = params.toString();
		return this.request<LogsResponse>(`/admin/logs${q ? `?${q}` : ''}`);
	}

	// Upload attempts
	async getUploadAttempts(
		opts: {
			status?: 'failed' | 'success' | 'all';
			platform?: string;
			targetName?: string;
			itemId?: string;
			page?: number;
			limit?: number;
		} = {}
	): Promise<PaginatedUploadAttempts> {
		const params = new URLSearchParams();
		if (opts.status && opts.status !== 'all') params.set('status', opts.status);
		if (opts.platform) params.set('platform', opts.platform);
		if (opts.targetName) params.set('targetName', opts.targetName);
		if (opts.itemId) params.set('itemId', opts.itemId);
		if (opts.page) params.set('page', String(opts.page));
		if (opts.limit) params.set('limit', String(opts.limit));
		const q = params.toString();
		return this.request<PaginatedUploadAttempts>(`/admin/upload-attempts${q ? `?${q}` : ''}`);
	}

	// Target health
	async getTargetHealth(): Promise<TargetHealth[]> {
		return this.request<TargetHealth[]>('/admin/targets/health');
	}

	// Client logs (browser → server)
	async sendClientLogs(
		clientId: string,
		entries: { level: string; source: string; url?: string; time: string; message: string }[]
	): Promise<void> {
		await this.request('/admin/client-logs', {
			method: 'POST',
			body: JSON.stringify({ clientId, entries })
		});
	}

	// Microblog (admin)
	async getMicroblogPosts(opts: { page?: number; limit?: number } = {}): Promise<PaginatedMicroblogPosts> {
		const params = new URLSearchParams();
		if (opts.page) params.set('page', String(opts.page));
		if (opts.limit) params.set('limit', String(opts.limit));
		const q = params.toString();
		return this.request<PaginatedMicroblogPosts>(`/admin/microblog/posts${q ? `?${q}` : ''}`);
	}

	async getMicroblogPost(id: number): Promise<MicroblogPostWithPublications> {
		return this.request<MicroblogPostWithPublications>(`/admin/microblog/posts/${id}`);
	}

	async createMicroblogPost(payload: {
		body: string;
		contentWarning?: string;
		imageUrl?: string;
		imageAltText?: string;
	}): Promise<MicroblogPostWithPublications> {
		return this.request<MicroblogPostWithPublications>('/admin/microblog/posts', {
			method: 'POST',
			body: JSON.stringify(payload)
		});
	}

	async deleteMicroblogPost(id: number): Promise<void> {
		await this.request(`/admin/microblog/posts/${id}`, { method: 'DELETE' });
	}

	async retryMicroblogPublication(postId: number, publicationId: number): Promise<MicroblogPublication> {
		return this.request<MicroblogPublication>(
			`/admin/microblog/posts/${postId}/retry/${publicationId}`,
			{ method: 'POST' }
		);
	}

	async refreshMicroblogPost(id: number): Promise<PublicMicroblogPost> {
		return this.request<PublicMicroblogPost>(`/admin/microblog/posts/${id}/refresh`, {
			method: 'POST'
		});
	}

	async uploadMicroblogImage(file: File): Promise<{ url: string; size: number }> {
		const form = new FormData();
		form.append('file', file);
		const headers: Record<string, string> = {};
		if (this.apiKey) headers['Authorization'] = `ApiKey ${this.apiKey}`;
		const resp = await fetch(`${API_BASE}/admin/microblog/upload`, {
			method: 'POST',
			headers,
			body: form
		});
		if (resp.status === 401) {
			this.clearApiKey();
			throw new Error('Unauthorized');
		}
		if (!resp.ok) {
			throw new Error(`Upload failed: ${resp.status}`);
		}
		return resp.json();
	}

	// Microblog (public — no API key needed, but we send it anyway since
	// the admin UI is authenticated)
	async getPublicMicroblogPost(slug: string): Promise<PublicMicroblogPost> {
		return this.request<PublicMicroblogPost>(`/microblog/posts/${slug}`);
	}

	async getMicroblogComments(slug: string): Promise<MicroblogComment[]> {
		return this.request<MicroblogComment[]>(`/microblog/posts/${slug}/comments`);
	}

	// Trips (admin)
	async getTrips(): Promise<{ items: TripListItem[] }> {
		return this.request<{ items: TripListItem[] }>('/admin/trips');
	}

	async getTrip(id: number): Promise<Trip> {
		return this.request<Trip>(`/admin/trips/${id}`);
	}

	async createTrip(payload: { title: string; slug?: string }): Promise<Trip> {
		return this.request<Trip>('/admin/trips', {
			method: 'POST',
			body: JSON.stringify(payload)
		});
	}

	async updateTrip(id: number, payload: TripUpdate): Promise<Trip> {
		return this.request<Trip>(`/admin/trips/${id}`, {
			method: 'PUT',
			body: JSON.stringify(payload)
		});
	}

	async deleteTrip(id: number): Promise<void> {
		await this.request(`/admin/trips/${id}`, { method: 'DELETE' });
	}

	async uploadTripImage(file: File): Promise<{ url: string; size: number }> {
		const form = new FormData();
		form.append('file', file);
		const headers: Record<string, string> = {};
		if (this.apiKey) headers['Authorization'] = `ApiKey ${this.apiKey}`;
		const resp = await fetch(`${API_BASE}/admin/trips/upload`, {
			method: 'POST',
			headers,
			body: form
		});
		if (resp.status === 401) {
			this.clearApiKey();
			throw new Error('Unauthorized');
		}
		if (!resp.ok) {
			throw new Error(`Upload failed: ${resp.status}`);
		}
		return resp.json();
	}
}

export const api = new ApiClient();

// Types
export interface DashboardStats {
	feedCount: number;
	feedItemCount: number;
	publicationCount: number;
	interactionCount: number;
	totalLikes: number;
	subscriberCount: number;
	webmentionCount: number;
	nativeLikeCount: number;
	connectionCount: number;
	platformBreakdown: Record<string, number>;
}

export interface Feed {
	ID: number;
	CreatedAt: string;
	UpdatedAt: string;
	FeedName: string;
	Title: string;
	Description: string;
	Link: string;
	FeedURL: string;
	Language: string;
	Copyright: string;
	Generator: string;
	ItemTypes: string;
}

export interface FeedWithCount extends Feed {
	itemCount: number;
}

export interface FeedItem {
	ID: number;
	CreatedAt: string;
	UpdatedAt: string;
	FeedID: number;
	Title: string;
	Description: string;
	Link: string;
	ItemType: string;
	ImageUrl: string;
	Published: string;
	GUID: string;
	Categories: Category[];
	Authors: Author[];
}

export interface FeedItemWithEngagement extends FeedItem {
	publications: AutoUploadItem[];
	interactions: Interaction[];
	nativeLikeCount: number;
	webmentionCount: number;
	recentFailures: RecentFailure[];
}

export interface RecentFailure {
	platform: string;
	targetName: string;
	errorCode: string;
	httpStatus?: number;
	createdAt: string;
}

export interface MiniFeedItem {
	id: number;
	feedId: number;
	title: string;
	imageUrl: string;
	link: string;
}

export interface Category {
	ID: number;
	Name: string;
}

export interface Author {
	ID: number;
	Name: string;
	Email: string;
}

export interface PaginatedFeedItems {
	items: FeedItemWithEngagement[];
	total: number;
	page: number;
	limit: number;
}

export interface AutoUploadItem {
	ID: number;
	Platform: string;
	ItemID: string;
	PostUrl: string | null;
	VersionId: string | null;
	PostId: string | null;
	CreatedAt: string;
}

export interface Interaction {
	ID: number;
	ItemID: string;
	Platform: string;
	TargetName: string;
	LikeCount: number;
	CreatedAt: string;
	UpdatedAt: string;
}

export interface InteractionSummary {
	totalLikes: number;
	totalNativeLikes: number;
	platformBreakdown: Record<string, number>;
	topItems: ItemLikes[];
}

export interface ItemLikes {
	itemId: string;
	totalLikes: number;
}

export interface Subscriber {
	id: number;
	endpoint: string;
	createdAt: string;
}

export interface Webmention {
	ID: number;
	Source: string;
	Target: string;
	CreatedAt: string;
}

export interface Connection {
	name: string;
	sourceName: string;
	targetName: string;
	caption: string;
	cron: string | null;
	platform: string;
	sourceFeedId?: number | null;
	targetUrl?: string;
}

export interface BroadcastNotification {
	title: string;
	body: string;
	url?: string;
	icon?: string;
}

export interface LogsResponse {
	files: string[];
	file: string;
	lines: string[];
}

export interface UploadAttempt {
	id: number;
	connectionName: string;
	itemId: string;
	platform: string;
	targetName: string;
	success: boolean;
	errorCode?: string;
	errorMessage?: string;
	httpStatus?: number;
	createdAt: string;
}

export interface PaginatedUploadAttempts {
	items: UploadAttempt[];
	total: number;
	page: number;
	limit: number;
}

export interface MicroblogPost {
	id: number;
	slug: string;
	body: string;
	contentWarning: string;
	imageUrl: string;
	imageAltText: string;
	createdAt: string;
	updatedAt: string;
}

export interface MicroblogPublication {
	id: number;
	postId: number;
	targetName: string;
	platform: string;
	postUrl?: string;
	externalPostId?: string;
	success: boolean;
	errorMessage?: string;
	likesRefreshedAt?: string;
	commentsRefreshedAt?: string;
	createdAt: string;
	updatedAt: string;
	errorOnAttempt?: string;
}

export interface MicroblogPostWithPublications extends MicroblogPost {
	publications: MicroblogPublication[];
}

export interface PaginatedMicroblogPosts {
	items: MicroblogPostWithPublications[];
	total: number;
	page: number;
	limit: number;
}

export interface MicroblogComment {
	id: number;
	postId: number;
	platform: string;
	externalId: string;
	author: string;
	authorUrl: string;
	avatarUrl: string;
	body: string;
	postedAt: string;
	importedAt: string;
}

export interface PublicMicroblogPost {
	id: number;
	slug: string;
	body: string;
	contentWarning?: string;
	imageUrl?: string;
	imageAltText?: string;
	createdAt: string;
	likeCount: number;
	commentCount: number;
	publications: { platform: string; targetName: string; postUrl?: string }[];
}

// Trips ---------------------------------------------------------------------
export type TripStatus = 'visited' | 'current' | 'upcoming';
export type TransportMode = '' | 'train' | 'flight' | 'car';

export interface TripPhoto {
	url: string;
	caption: string;
	alt: string;
	tint: string;
}

// An intermediate point along a transport leg's route — not a stop, just
// geometry the public site draws the trail through.
export interface TripWaypoint {
	lat: number;
	lng: number;
}

export interface TripStop {
	stopKey: string;
	name: string;
	startDate: string;
	endDate: string;
	lat: number;
	lng: number;
	status: TripStatus;
	note: string;
	country: string;
	transportMode: TransportMode;
	transportLabel: string;
	transportDuration: string;
	distanceKm: number | null;
	transportCountries: string[];
	transportWaypoints: TripWaypoint[];
	photos: TripPhoto[];
	transportPhotos: TripPhoto[];
}

export interface Trip {
	id: number;
	slug: string;
	title: string;
	published: boolean;
	// The only manual stat — the planned trip length. All others (daysElapsed,
	// cities, countries, distance) are derived from the stops by the backend.
	daysTotal: number | null;
	stops: TripStop[];
}

// The PUT payload — same as Trip minus the server-assigned id.
export type TripUpdate = Omit<Trip, 'id'>;

export interface TripListItem {
	id: number;
	slug: string;
	title: string;
	published: boolean;
	stopCount: number;
}

export type TargetHealthStatus = 'healthy' | 'degraded' | 'down' | 'unknown';

export interface TargetHealth {
	name: string;
	platform: string;
	status: TargetHealthStatus;
	lastAttemptAt?: string;
	lastSuccessAt?: string;
	lastFailureAt?: string;
	lastError?: string;
	lastErrorCode?: string;
	lastHttpStatus?: number;
	recentFailures: number;
	recentSuccesses: number;
}
