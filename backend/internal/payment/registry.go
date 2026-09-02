package payment

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

type Registry struct {
	providers map[string]Provider
}

func NewRegistry(providers ...Provider) (*Registry, error) {
	registry := &Registry{providers: make(map[string]Provider, len(providers))}
	for _, provider := range providers {
		if provider == nil {
			return nil, fmt.Errorf("payment provider is nil")
		}
		id := strings.TrimSpace(provider.Descriptor().ID)
		if id == "" {
			return nil, fmt.Errorf("payment provider id is empty")
		}
		if _, exists := registry.providers[id]; exists {
			return nil, fmt.Errorf("duplicate payment provider %q", id)
		}
		registry.providers[id] = provider
	}
	return registry, nil
}

func Builtins() *Registry {
	client := &http.Client{Timeout: 60 * time.Second}
	registry, err := NewRegistry(
		NewWeChatProvider(client, "https://api.mch.weixin.qq.com"),
		NewAlipayProvider(client),
	)
	if err != nil {
		panic(err)
	}
	return registry
}

func (r *Registry) Get(id string) (Provider, bool) {
	if r == nil {
		return nil, false
	}
	provider, ok := r.providers[strings.TrimSpace(id)]
	return provider, ok
}

func (r *Registry) Descriptors() []Descriptor {
	if r == nil {
		return []Descriptor{}
	}
	items := make([]Descriptor, 0, len(r.providers))
	for _, provider := range r.providers {
		items = append(items, provider.Descriptor())
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}
