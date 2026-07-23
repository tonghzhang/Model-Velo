package provider

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// AdapterRegistry 保存启动时构造完成的 Provider Adapter，只提供只读查询。
type AdapterRegistry struct {
	adapters map[string]Adapter
	ids      []string
}

// NewAdapterRegistry 校验 Provider ID，并从配置创建不可变的 Adapter 索引。
func NewAdapterRegistry(configured []AdapterConfig) (*AdapterRegistry, error) {
	adapters := make(map[string]Adapter, len(configured))
	for index, config := range configured {
		providerID := strings.TrimSpace(config.ProviderID)
		if providerID == "" {
			return nil, fmt.Errorf("provider adapter ID at index %d is empty", index)
		}
		if _, exists := adapters[providerID]; exists {
			return nil, fmt.Errorf("provider adapter ID %q is duplicated", providerID)
		}
		adapter, err := NewAdapter(config)
		if err != nil {
			return nil, fmt.Errorf("configure provider adapter %q: %w", providerID, err)
		}
		adapters[providerID] = adapter
	}
	return NewAdapterRegistryFromAdapters(adapters)
}

// NewAdapterRegistryFromAdapters 用于注入已经构造好的 Adapter，也会拒绝接口中的类型化 nil。
func NewAdapterRegistryFromAdapters(configured map[string]Adapter) (*AdapterRegistry, error) {
	registry := &AdapterRegistry{adapters: make(map[string]Adapter, len(configured))}
	for configuredID, adapter := range configured {
		providerID := strings.TrimSpace(configuredID)
		if providerID == "" {
			return nil, errors.New("provider adapter ID is empty")
		}
		if nilAdapter(adapter) {
			return nil, fmt.Errorf("provider adapter %q is nil", providerID)
		}
		if _, exists := registry.adapters[providerID]; exists {
			return nil, fmt.Errorf("provider adapter ID %q is duplicated", providerID)
		}
		registry.adapters[providerID] = adapter
		registry.ids = append(registry.ids, providerID)
	}
	if len(registry.adapters) == 0 {
		return nil, errors.New("provider adapter registry requires at least one provider")
	}
	sort.Strings(registry.ids)
	return registry, nil
}

// nilAdapter 处理 interface 本身非 nil、内部指针却为 nil 的 Go 边界情况。
func nilAdapter(adapter Adapter) bool {
	if adapter == nil {
		return true
	}
	value := reflect.ValueOf(adapter)
	return value.Kind() == reflect.Pointer && value.IsNil()
}

// Adapter 按 Provider ID 查找对应的协议实现。
func (registry *AdapterRegistry) Adapter(providerID string) (Adapter, bool) {
	if registry == nil {
		return nil, false
	}
	adapter := registry.adapters[strings.TrimSpace(providerID)]
	return adapter, adapter != nil
}

// ProviderIDs 返回副本，避免调用方修改注册表内部的稳定顺序。
func (registry *AdapterRegistry) ProviderIDs() []string {
	if registry == nil {
		return nil
	}
	return append([]string(nil), registry.ids...)
}

// KeyedProviderIDs 只返回需要 Provider Key 选择器参与的上游。
func (registry *AdapterRegistry) KeyedProviderIDs() []string {
	if registry == nil {
		return nil
	}
	providerIDs := make([]string, 0, len(registry.ids))
	for _, providerID := range registry.ids {
		if registry.adapters[providerID].Authentication() == AuthenticationAPIKey {
			providerIDs = append(providerIDs, providerID)
		}
	}
	return providerIDs
}
