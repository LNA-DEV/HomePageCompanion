// The shell's side of @companion/ui: module elements navigate, notify and
// report an expired session through window.companionShell.
import { goto } from '$app/navigation';
import { toast } from './toast';
import { authenticated } from './session';

export function installBridge(): void {
	window.companionShell = {
		navigate: (to: string) => void goto(to),
		toast,
		unauthorized: () => {
			authenticated.set(false);
			void goto('/login');
		}
	};
}
