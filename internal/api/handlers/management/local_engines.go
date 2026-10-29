package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/localdiscovery"
)

// SetLocalDiscovery binds the service-owned controller before HTTP requests begin.
func (h *Handler) SetLocalDiscovery(controller *localdiscovery.Controller) {
	h.localDiscovery = controller
}

// GetLocalEngines returns metadata-only status, including unavailable stopped engines.
func (h *Handler) GetLocalEngines(c *gin.Context) {
	states := []localdiscovery.Status{}
	if h.localDiscovery != nil {
		states = h.localDiscovery.Snapshot()
	}
	c.JSON(http.StatusOK, gin.H{"providers": states})
}

// RefreshLocalEngines requests bounded catalog probes without making inference calls.
func (h *Handler) RefreshLocalEngines(c *gin.Context) {
	if h.localDiscovery != nil {
		h.localDiscovery.Refresh(c.Request.Context())
	}
	h.GetLocalEngines(c)
}
