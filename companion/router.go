package companion

import "github.com/gin-gonic/gin"

// Router is where a building block mounts its HTTP API.
type Router struct {
	// Admin is /api/b/<id>/, reachable only with the admin session (or the
	// API key header).
	Admin *gin.RouterGroup
	// Public is /api/b/<id>/public/, reachable by anyone.
	Public *gin.RouterGroup
	// RequireAdmin is the admin check, for public-tree routes that need it
	// (a legacy alias such as POST /api/upload/:connection).
	RequireAdmin gin.HandlerFunc

	aliases map[string]*gin.RouterGroup
}

// NewRouter is used by the core.
func NewRouter(admin, public *gin.RouterGroup, requireAdmin gin.HandlerFunc, aliases map[string]*gin.RouterGroup) *Router {
	return &Router{Admin: admin, Public: public, RequireAdmin: requireAdmin, aliases: aliases}
}

// Alias returns the public group for a legacy prefix declared in
// Info.Aliases. It panics for an undeclared prefix, which is a bug in the
// module.
func (r *Router) Alias(prefix string) *gin.RouterGroup {
	g, ok := r.aliases[prefix]
	if !ok {
		panic("companion: alias " + prefix + " is not declared in Info.Aliases")
	}
	return g
}
