package hazard

import "errors"

var (
	// ErrHazardNotSupported 表示服务尚未注册请求灾种的实时能力。
	ErrHazardNotSupported = errors.New("灾种尚未注册")
)
