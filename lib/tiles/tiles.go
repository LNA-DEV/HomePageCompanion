// Package tiles holds what the tile proxies share.
package tiles

import (
	"strconv"
	"strings"
)

// ParseZXY validates a tile address: integers, z ≤ maxZoom, x and y inside
// the zoom level's grid, and the expected extension on y.
func ParseZXY(zs, xs, ys, ext string, maxZoom int) (uint8, uint32, uint32, bool) {
	ys, found := strings.CutSuffix(ys, "."+ext)
	if !found {
		return 0, 0, 0, false
	}
	z, err1 := strconv.Atoi(zs)
	x, err2 := strconv.ParseUint(xs, 10, 32)
	y, err3 := strconv.ParseUint(ys, 10, 32)
	if err1 != nil || err2 != nil || err3 != nil || z < 0 || z > maxZoom {
		return 0, 0, 0, false
	}
	n := uint64(1) << uint(z)
	if x >= n || y >= n {
		return 0, 0, 0, false
	}
	return uint8(z), uint32(x), uint32(y), true
}
