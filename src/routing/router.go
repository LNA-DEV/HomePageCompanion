package routing

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/config"
)

const (
	defaultOSRM       = "https://router.project-osrm.org/route/v1"
	defaultTransitous = "https://api.transitous.org/api/v1/plan"

	// simplifyEpsilon (degrees, ~5 m) thins OSRM's full-resolution geometry:
	// an 800 km drive comes back with tens of thousands of points, which would
	// make the public trip payload megabytes large for no visible difference.
	simplifyEpsilon = 0.00005
)

// Router asks the routing services. Wait is called before every request.
type Router struct {
	OSRMURL       string
	TransitousURL string
	Client        *http.Client
	Now           func() time.Time
}

// NewRouter builds a router from the config, with the public services as
// defaults.
func NewRouter(c config.Routing) *Router {
	r := &Router{
		OSRMURL:       strings.TrimRight(c.OSRMURL, "/"),
		TransitousURL: c.TransitousURL,
		Client:        &http.Client{Timeout: 30 * time.Second},
		Now:           time.Now,
	}
	if r.OSRMURL == "" {
		r.OSRMURL = defaultOSRM
	}
	if r.TransitousURL == "" {
		r.TransitousURL = defaultTransitous
	}
	return r
}

// Route computes the track for a leg, as travel.js did: car → OSRM driving;
// train → Transitous rail per segment, else the OSRM road corridor. Returns
// [lat, lng] points rounded to 5 decimals and simplified, plus the source.
func (r *Router) Route(mode string, points []Point, wait func()) ([][2]float64, string, error) {
	if len(points) < 2 {
		return nil, "", errors.New("a leg needs two points")
	}
	if mode == "train" {
		pts, err := r.train(points, wait)
		if err == nil {
			return finish(pts), "transitous", nil
		}
		road, roadErr := r.osrm(points, wait)
		if roadErr != nil {
			return nil, "", fmt.Errorf("rail: %v; road: %v", err, roadErr)
		}
		return finish(road), "osrm", nil
	}
	pts, err := r.osrm(points, wait)
	if err != nil {
		return nil, "", err
	}
	return finish(pts), "osrm", nil
}

func (r *Router) get(u string, out any) error {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", config.UserAgent())
	req.Header.Set("Accept", "application/json")
	resp, err := r.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(out)
}

// osrm asks for the driving route through every point in order.
func (r *Router) osrm(points []Point, wait func()) ([][2]float64, error) {
	coords := make([]string, len(points))
	for i, p := range points {
		coords[i] = fmt.Sprintf("%.6f,%.6f", p.Lng, p.Lat)
	}
	u := fmt.Sprintf("%s/driving/%s?overview=full&geometries=geojson", r.OSRMURL, strings.Join(coords, ";"))
	var data struct {
		Code   string `json:"code"`
		Routes []struct {
			Geometry struct {
				Coordinates [][2]float64 `json:"coordinates"`
			} `json:"geometry"`
		} `json:"routes"`
	}
	if wait != nil {
		wait()
	}
	if err := r.get(u, &data); err != nil {
		return nil, fmt.Errorf("osrm: %w", err)
	}
	if len(data.Routes) == 0 || len(data.Routes[0].Geometry.Coordinates) < 2 {
		return nil, fmt.Errorf("osrm: no route (%s)", data.Code)
	}
	out := make([][2]float64, len(data.Routes[0].Geometry.Coordinates))
	for i, c := range data.Routes[0].Geometry.Coordinates {
		out[i] = [2]float64{c[1], c[0]} // [lng, lat] → [lat, lng]
	}
	return out, nil
}

// train traces rail through every consecutive segment and joins them. Any
// segment without a rail itinerary fails the whole leg, so the caller falls
// back to the road corridor.
func (r *Router) train(points []Point, wait func()) ([][2]float64, error) {
	var all [][2]float64
	for i := 1; i < len(points); i++ {
		seg, err := r.trainSegment(points[i-1], points[i], wait)
		if err != nil {
			return nil, err
		}
		if len(all) > 0 && len(seg) > 0 {
			seg = seg[1:] // drop the duplicated junction point
		}
		all = append(all, seg...)
	}
	if len(all) < 2 {
		return nil, errors.New("transitous: no rail geometry")
	}
	return all, nil
}

func (r *Router) trainSegment(a, b Point, wait func()) ([][2]float64, error) {
	q := url.Values{}
	q.Set("fromPlace", fmt.Sprintf("%.6f,%.6f", a.Lat, a.Lng))
	q.Set("toPlace", fmt.Sprintf("%.6f,%.6f", b.Lat, b.Lng))
	q.Set("time", railQueryTime(r.Now()))
	q.Set("transitModes", "RAIL")
	q.Set("numItineraries", "1")
	var data struct {
		Itineraries []struct {
			Legs []struct {
				LegGeometry struct {
					Points    string `json:"points"`
					Precision int    `json:"precision"`
				} `json:"legGeometry"`
			} `json:"legs"`
		} `json:"itineraries"`
	}
	if wait != nil {
		wait()
	}
	if err := r.get(r.TransitousURL+"?"+q.Encode(), &data); err != nil {
		return nil, fmt.Errorf("transitous: %w", err)
	}
	if len(data.Itineraries) == 0 || len(data.Itineraries[0].Legs) == 0 {
		return nil, errors.New("transitous: no itinerary")
	}
	var pts [][2]float64
	for _, leg := range data.Itineraries[0].Legs {
		g := leg.LegGeometry
		if g.Points == "" {
			continue
		}
		dec := DecodePolyline(g.Points, g.Precision)
		if len(pts) > 0 && len(dec) > 0 {
			dec = dec[1:]
		}
		pts = append(pts, dec...)
	}
	if len(pts) < 2 {
		return nil, errors.New("transitous: empty geometry")
	}
	return pts, nil
}

// railQueryTime is a near-future morning: only a date with live service is
// needed to trace the track geometry, not the stop's display date.
func railQueryTime(now time.Time) string {
	d := now.UTC().AddDate(0, 0, 1)
	return time.Date(d.Year(), d.Month(), d.Day(), 7, 0, 0, 0, time.UTC).Format(time.RFC3339)
}

// DecodePolyline decodes a Google encoded polyline; precision is the number
// of decimals (Transitous / MOTIS uses 7; 0 means 7, as travel.js assumed).
func DecodePolyline(s string, precision int) [][2]float64 {
	if precision <= 0 {
		precision = 7
	}
	factor := math.Pow(10, float64(precision))
	var out [][2]float64
	index, lat, lng := 0, 0, 0
	next := func() (int, bool) {
		result, shift := 0, 0
		for {
			if index >= len(s) {
				return 0, false
			}
			b := int(s[index]) - 63
			index++
			result |= (b & 0x1f) << shift
			shift += 5
			if b < 0x20 {
				break
			}
		}
		if result&1 != 0 {
			return ^(result >> 1), true
		}
		return result >> 1, true
	}
	for index < len(s) {
		dlat, ok := next()
		if !ok {
			break
		}
		dlng, ok := next()
		if !ok {
			break
		}
		lat += dlat
		lng += dlng
		out = append(out, [2]float64{float64(lat) / factor, float64(lng) / factor})
	}
	return out
}

// finish rounds to 5 decimals (~1 m) and simplifies.
func finish(pts [][2]float64) [][2]float64 {
	s := Simplify(pts, simplifyEpsilon)
	for i := range s {
		s[i][0] = math.Round(s[i][0]*1e5) / 1e5
		s[i][1] = math.Round(s[i][1]*1e5) / 1e5
	}
	return s
}

// Simplify is Ramer–Douglas–Peucker in plain degrees: good enough for
// thinning a drawn line, and it always keeps both ends.
func Simplify(pts [][2]float64, eps float64) [][2]float64 {
	if len(pts) < 3 {
		return append([][2]float64(nil), pts...)
	}
	keep := make([]bool, len(pts))
	keep[0], keep[len(pts)-1] = true, true
	type span struct{ a, b int }
	stack := []span{{0, len(pts) - 1}}
	for len(stack) > 0 {
		s := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		maxD, idx := 0.0, -1
		for i := s.a + 1; i < s.b; i++ {
			if d := segDist(pts[i], pts[s.a], pts[s.b]); d > maxD {
				maxD, idx = d, i
			}
		}
		if idx >= 0 && maxD > eps {
			keep[idx] = true
			stack = append(stack, span{s.a, idx}, span{idx, s.b})
		}
	}
	out := make([][2]float64, 0, len(pts))
	for i, k := range keep {
		if k {
			out = append(out, pts[i])
		}
	}
	return out
}

func segDist(p, a, b [2]float64) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	if dx == 0 && dy == 0 {
		return math.Hypot(p[0]-a[0], p[1]-a[1])
	}
	t := ((p[0]-a[0])*dx + (p[1]-a[1])*dy) / (dx*dx + dy*dy)
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(p[0]-(a[0]+t*dx), p[1]-(a[1]+t*dy))
}
