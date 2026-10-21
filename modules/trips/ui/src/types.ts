// The trips API as the pages use it (modules/trips/api.go).
export type TripStatus = 'visited' | 'current' | 'upcoming';
export type TransportMode = '' | 'train' | 'flight' | 'car';

export interface TripPhoto {
	url: string;
	caption: string;
	alt: string;
	tint: string;
}

// An intermediate point along a transport leg's route: not a stop, just
// geometry the website draws the trail through.
export interface TripWaypoint {
	lat: number;
	lng: number;
}

export interface TripRoute {
	key: string;
	status: 'ok' | 'failed' | 'pending';
	source?: string;
	error?: string;
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
	// Read-only: whether the companion has routed the leg into this stop.
	route?: TripRoute;
}

export interface Trip {
	id: number;
	slug: string;
	title: string;
	published: boolean;
	// The only manual stat; the others come from the stops.
	daysTotal: number | null;
	stops: TripStop[];
}

export interface TripListItem {
	id: number;
	slug: string;
	title: string;
	published: boolean;
	stopCount: number;
}

export interface RouteStatus {
	ok: number;
	failed: number;
	pending: number;
	router: boolean;
}
