package domain

import "errors"

var (
	// ErrInvalidInput 表示输入不满足领域约束。
	ErrInvalidInput = errors.New("输入不满足领域约束")
	// ErrNotFound 表示请求的领域对象不存在。
	ErrNotFound = errors.New("领域对象不存在")
	// ErrInsufficientData 表示缺少产生可靠结论所需的数据。
	ErrInsufficientData = errors.New("数据不足")
	// ErrProviderUnavailable 表示外部数据供应商暂时不可用。
	ErrProviderUnavailable = errors.New("外部供应商不可用")
)
