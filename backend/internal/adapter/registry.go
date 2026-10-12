package adapter

import (
	"fmt"
	"sort"
	"sync"
)

// Registry 适配器注册中心
type Registry struct {
	mu       sync.RWMutex
	adapters map[DiskType]DiskAdapter
}

var defaultRegistry = &Registry{adapters: make(map[DiskType]DiskAdapter)}

// Register 注册适配器（在 init 中调用）
func Register(a DiskAdapter) {
	defaultRegistry.mu.Lock()
	defer defaultRegistry.mu.Unlock()
	defaultRegistry.adapters[a.Type()] = a
}

// Get 获取指定网盘的适配器
func Get(t DiskType) (DiskAdapter, error) {
	defaultRegistry.mu.RLock()
	defer defaultRegistry.mu.RUnlock()
	a, ok := defaultRegistry.adapters[t]
	if !ok {
		return nil, fmt.Errorf("不支持的网盘类型: %s", t)
	}
	return a, nil
}

// MustGet 获取适配器，不存在则 panic（仅用于启动时校验）
func MustGet(t DiskType) DiskAdapter {
	a, err := Get(t)
	if err != nil {
		panic(err)
	}
	return a
}

// List 列出所有已注册的适配器
func List() []DiskAdapter {
	defaultRegistry.mu.RLock()
	defer defaultRegistry.mu.RUnlock()
	out := make([]DiskAdapter, 0, len(defaultRegistry.adapters))
	for _, a := range defaultRegistry.adapters {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type() < out[j].Type() })
	return out
}

// IsSupported 判断是否支持某网盘
func IsSupported(t DiskType) bool {
	defaultRegistry.mu.RLock()
	defer defaultRegistry.mu.RUnlock()
	_, ok := defaultRegistry.adapters[t]
	return ok
}
