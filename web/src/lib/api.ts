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

	// Publications
	async getPublications(platform?: string): Promise<AutoUploadItem[]> {
		const query = platform ? `?platform=${platform}` : '';
		return this.request<AutoUploadItem[]>(`/admin/publications${query}`);
	}

	async deletePublication(id: number): Promise<void> {
		await this.request(`/admin/publications/${id}`, { method: 'DELETE' });
	}

	// Interactions
	async getInteractions(platform?: string, itemName?: string): Promise<Interaction[]> {
		const params = new URLSearchParams();
		if (platform) params.set('platform', platform);
		if (itemName) params.set('itemName', itemName);
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

	// Broadcast
	async broadcast(notification: BroadcastNotification): Promise<void> {
		await this.request('/webpush/broadcast', {
			method: 'POST',
			body: JSON.stringify(notification)
		});
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
	items: FeedItem[];
	total: number;
	page: number;
	limit: number;
}

export interface AutoUploadItem {
	ID: number;
	Platform: string;
	ItemName: string;
	PostUrl: string | null;
	VersionId: string | null;
	PostId: string | null;
	CreatedAt: string;
}

export interface Interaction {
	ID: number;
	ItemName: string;
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
	itemName: string;
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
}

export interface BroadcastNotification {
	title: string;
	body: string;
	url?: string;
	icon?: string;
}
