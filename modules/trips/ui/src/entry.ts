import { injectStyles } from '@companion/ui';
import css from './styles.css?inline';
import leaflet from 'leaflet/dist/leaflet.css?inline';
import './TripsPage.ce.svelte';

injectStyles('trips', css);
injectStyles('leaflet', leaflet);
