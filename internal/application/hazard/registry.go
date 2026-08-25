package hazard

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/Requim/AI-GDM/internal/domain"
	hazarddomain "github.com/Requim/AI-GDM/internal/domain/hazard"
)

// Registry 保存灾种到专用能力的线程安全映射。
type Registry struct {
	mu        sync.RWMutex
	providers map[hazarddomain.Type]*HazardProvider
}

// NewRegistry 创建注册表并拒绝重复灾种。
func NewRegistry(providers ...*HazardProvider) (*Registry, error) {
	registry := &Registry{providers: make(map[hazarddomain.Type]*HazardProvider, len(providers))}
	for _, provider := range providers {
		if err := registry.Register(provider); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

// Register 注册一个灾种；已有能力不会被静默覆盖。
func (r *Registry) Register(provider *HazardProvider) error {
	if r == nil || provider == nil {
		return fmt.Errorf("%w: 灾种注册表或能力为空", domain.ErrInvalidInput)
	}
	if err := validateHazardType(provider.Type()); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providers == nil {
		r.providers = make(map[hazarddomain.Type]*HazardProvider)
	}
	if _, exists := r.providers[provider.Type()]; exists {
		return fmt.Errorf("%w: 灾种 %q 已注册", domain.ErrInvalidInput, provider.Type())
	}
	r.providers[provider.Type()] = provider
	return nil
}

// Resolve 返回指定灾种能力。
func (r *Registry) Resolve(hazardType hazarddomain.Type) (*HazardProvider, error) {
	if r == nil {
		return nil, fmt.Errorf("%w: 灾种注册表为空", domain.ErrInvalidInput)
	}
	if err := validateHazardType(hazardType); err != nil {
		return nil, err
	}
	r.mu.RLock()
	provider, exists := r.providers[hazardType]
	r.mu.RUnlock()
	if !exists {
		return nil, fmt.Errorf("%w: %w: 灾种 %q", ErrHazardNotSupported, domain.ErrNotFound, hazardType)
	}
	return provider, nil
}

// Types 返回按稳定标识排序的已注册灾种。
func (r *Registry) Types() []hazarddomain.Type {
	if r == nil {
		return []hazarddomain.Type{}
	}
	r.mu.RLock()
	values := make([]hazarddomain.Type, 0, len(r.providers))
	for value := range r.providers {
		values = append(values, value)
	}
	r.mu.RUnlock()
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return values
}

func validateHazardType(value hazarddomain.Type) error {
	raw := string(value)
	if raw == "" || raw != strings.TrimSpace(raw) || len(raw) > 64 || !validTypeStart(raw[0]) {
		return fmt.Errorf("%w: 灾种标识 %q 无效", domain.ErrInvalidInput, value)
	}
	for index := 1; index < len(raw); index++ {
		if !validTypePart(raw[index]) {
			return fmt.Errorf("%w: 灾种标识 %q 无效", domain.ErrInvalidInput, value)
		}
	}
	return nil
}

func validTypeStart(value byte) bool {
	return value >= 'a' && value <= 'z'
}

func validTypePart(value byte) bool {
	return validTypeStart(value) || value >= '0' && value <= '9' || value == '_'
}
