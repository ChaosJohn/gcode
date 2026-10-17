package gemini

import (
	"fmt"
	"strconv"
	"time"

	"github.com/ChaosJohn/gcode/internal/base/config"
)

// Small helpers shared by the provider and its tests.

func itoa(n int) string { return strconv.Itoa(n) }

func timeNowMS() int64 { return time.Now().UnixMilli() }

var timeSecond = time.Second

// configAuthSource adapts the loaded config to the auth route.
type configAuthSource struct {
	cfg *config.Config
}

func (s configAuthSource) ForceOAuth() bool {
	return s.cfg.Provider.GeminiForceOAuth
}

func (s configAuthSource) CloudProject() string {
	if s.cfg.Provider.GeminiProject == nil {
		return ""
	}
	project := fmt.Sprintf("%s", *s.cfg.Provider.GeminiProject)
	return trimSpace(project)
}
