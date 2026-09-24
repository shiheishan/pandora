package payment

import (
	"context"
	"errors"
	"testing"
)

func TestFactoryReportsDisabledProviderAsSentinel(t *testing.T) {
	built := false
	f := NewFactory(func(context.Context, string, string) (*ProviderRecord, error) {
		return &ProviderRecord{Code: "epay", Adapter: "epay", Enabled: false}, nil
	}, 0)
	f.RegisterAdapter("epay", func(ProviderRecord) (Provider, error) {
		built = true
		return nil, nil
	})
	_, _, err := f.Get(context.Background(), "tenant", "epay")
	if !errors.Is(err, ErrProviderDisabled) {
		t.Fatalf("disabled provider error=%v, want ErrProviderDisabled", err)
	}
	if built {
		t.Fatal("disabled provider must not be built or cached")
	}
}
