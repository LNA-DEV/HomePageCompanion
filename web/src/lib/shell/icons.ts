// The icons building blocks may name in nav entries (lucide names). A
// curated set keeps the shell small; an unknown name gets a neutral icon.
import {
	AtSign,
	Bell,
	Boxes,
	CalendarClock,
	DatabaseBackup,
	Heart,
	LayoutDashboard,
	LayoutGrid,
	Layers,
	Link2,
	ListChecks,
	Map,
	MapPinned,
	Megaphone,
	MessageSquare,
	PenSquare,
	Puzzle,
	Rss,
	ScrollText,
	Send,
	Settings,
	TriangleAlert,
	Users
} from 'lucide-svelte';

const icons: Record<string, typeof Puzzle> = {
	'at-sign': AtSign,
	bell: Bell,
	boxes: Boxes,
	'calendar-clock': CalendarClock,
	'database-backup': DatabaseBackup,
	heart: Heart,
	'layout-dashboard': LayoutDashboard,
	'layout-grid': LayoutGrid,
	layers: Layers,
	'link-2': Link2,
	'list-checks': ListChecks,
	map: Map,
	'map-pinned': MapPinned,
	megaphone: Megaphone,
	'message-square': MessageSquare,
	'pen-square': PenSquare,
	rss: Rss,
	'scroll-text': ScrollText,
	send: Send,
	settings: Settings,
	'triangle-alert': TriangleAlert,
	users: Users
};

export function icon(name?: string): typeof Puzzle {
	return (name && icons[name]) || Puzzle;
}
