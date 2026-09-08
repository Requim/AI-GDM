package webui

import (
	"net/http"
	"strings"
	"testing"
)

func TestBasemapScriptIsEmbeddedAndLoadedBeforeMaps(t *testing.T) {
	handler := newTestHandler(t, &serviceStub{})
	body := serve(t, handler, "/").Body.String()
	base := strings.Index(body, `src="/assets/basemap.js"`)
	if base < 0 || base > strings.Index(body, `src="/assets/risk-map.js"`) ||
		base > strings.Index(body, `src="/assets/evacuation.js"`) {
		t.Fatal("共享底图脚本必须在两个地图入口前加载")
	}
	response := serve(t, handler, "/assets/basemap.js")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "backup.opentopomap.org") {
		t.Fatal("共享底图脚本未嵌入发布产物")
	}
}
