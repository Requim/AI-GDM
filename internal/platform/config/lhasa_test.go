package config

import (
	"testing"
	"time"
)

func TestLoadPortalRefreshBudget(t *testing.T) {
	t.Setenv("LHASA_PORTAL_URL", "https://example.test/nrt/")
	t.Setenv("LHASA_REFRESH_INTERVAL", "3h")
	t.Setenv("LHASA_REFRESH_TIMEOUT", "75m")
	value, err := loadLHASA()
	if err != nil {
		t.Fatal(err)
	}
	if value.PortalURL != "https://example.test/nrt/" || value.Interval != 3*time.Hour || value.Timeout != 75*time.Minute {
		t.Fatalf("%+v", value)
	}
	t.Setenv("LHASA_REFRESH_TIMEOUT", "3h")
	if _, err = loadLHASA(); err == nil {
		t.Fatal("越界长任务超时未拒绝")
	}
}
