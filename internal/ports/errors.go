package ports

import "errors"

// ErrStoredAssessmentIntegrity 标识持久化评估与其不可变身份或领域结构不一致。
var ErrStoredAssessmentIntegrity = errors.New("已存评估完整性无效")
