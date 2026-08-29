package survival

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

const (
	// ReplayModeHistorical 是历史回放响应唯一允许的用途模式。
	ReplayModeHistorical = "historical_replay"
	// ReplayDisclaimer 是所有回放详情和结果必须携带的安全声明。
	ReplayDisclaimer = "仅用于合成输入的历史案例回放和人工辅助，不得用于实时人员评估或自动放弃搜救"
)

// ReplayUsage 明确历史回放不可作为实时个体生还评估使用。
type ReplayUsage struct {
	Mode           string `json:"mode"`
	SyntheticInput bool   `json:"syntheticInput"`
	LiveUseAllowed bool   `json:"liveUseAllowed"`
	Disclaimer     string `json:"disclaimer"`
}

// HistoricalReplayUsage 返回不可变的历史回放用途声明。
func HistoricalReplayUsage() ReplayUsage {
	return ReplayUsage{
		Mode: ReplayModeHistorical, SyntheticInput: true, LiveUseAllowed: false,
		Disclaimer: ReplayDisclaimer,
	}
}

// Digest 返回已校验场景的稳定 SHA-256 摘要。
func (s Scenario) Digest() (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	payload, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("编码回放场景摘要: %w", err)
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
