package service

import (
	"testing"

	"infinite-canvas/backend/internal/payment"
	"infinite-canvas/backend/internal/protocol"
)

func TestBundledPaymentPluginsMatchHostProviders(t *testing.T) {
	registry := payment.Builtins()
	manifests := bundledPaymentPluginManifests()
	if len(manifests) != 2 {
		t.Fatalf("payment plugin manifests = %d", len(manifests))
	}
	for _, manifest := range manifests {
		if err := protocol.ValidateManifest(manifest); err != nil {
			t.Fatalf("validate %s: %v", manifest.Metadata.ID, err)
		}
		management := pluginManagement(manifest.Metadata.ID, PluginOriginSystem)
		if management.Kind != PluginKindPayment || management.Origin != PluginOriginSystem || management.ActivationScope != PluginScopeSystem {
			t.Fatalf("plugin %s management = %#v", manifest.Metadata.ID, management)
		}
		if len(manifest.Contributes.PaymentProviders) != 1 {
			t.Fatalf("plugin %s payment contributions = %d", manifest.Metadata.ID, len(manifest.Contributes.PaymentProviders))
		}
		contribution := manifest.Contributes.PaymentProviders[0]
		provider, ok := registry.Get(contribution.ID)
		if !ok {
			t.Fatalf("plugin %s has no host provider %s", manifest.Metadata.ID, contribution.ID)
		}
		descriptor := provider.Descriptor()
		if descriptor.PluginID != manifest.Metadata.ID || descriptor.Icon != contribution.Icon || descriptor.CheckoutMode != contribution.CheckoutMode {
			t.Fatalf("plugin/provider mismatch: manifest=%#v descriptor=%#v", contribution, descriptor)
		}
	}
}

func TestBundledPaymentPluginsLoadFromSystemSource(t *testing.T) {
	center, err := newPluginRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	plugins := make(map[string]PluginView)
	for _, plugin := range center.list() {
		plugins[plugin.Manifest.ID] = plugin
	}
	for _, manifest := range bundledPaymentPluginManifests() {
		plugin, ok := plugins[manifest.Metadata.ID]
		if !ok {
			t.Fatalf("system payment plugin %s is missing", manifest.Metadata.ID)
		}
		if plugin.Source != PluginOriginSystem || !plugin.Manifest.Trusted {
			t.Fatalf("system payment plugin %s = %#v", manifest.Metadata.ID, plugin)
		}
	}
}

func TestTopupProductCreditAmountStaysWithinSafeLimit(t *testing.T) {
	if _, err := topupProductFromRequest("product", "admin", TopupProductRequest{
		Name: "过大积分商品", AmountFen: 1, CreditsMicrocredits: maxTopupCreditsMicrocredits + 1,
	}); err == nil {
		t.Fatal("expected oversized top-up credits to be rejected")
	}
	if _, err := topupProductFromRequest("product", "admin", TopupProductRequest{
		Name: "有效积分商品", AmountFen: 1, CreditsMicrocredits: maxTopupCreditsMicrocredits,
	}); err != nil {
		t.Fatalf("maximum safe top-up credits: %v", err)
	}
}
