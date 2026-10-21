import { connect } from '@companion/ui';

export const wp = connect('webpush');

export interface Audience {
	name: string;
	subscribers: number;
}

export const audiences = () => wp.api.get<Audience[]>('audiences');
